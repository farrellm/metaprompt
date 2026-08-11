package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farrellm/metaprompt/internal/llm"
)

// This file runs offline: the paths below either stop before the API call or
// stub it out through the generate seam. Only the request/response wire itself
// needs a key, and that is the end-to-end run described in CLAUDE.md.

// stub records the requests the chain made and hands back the scripted replies
// in order. A test that scripts fewer replies than the run asks for fails,
// rather than quietly feeding one canned answer to every step.
type stub struct {
	t        *testing.T
	replies  []string
	requests []llm.Request
}

// stubReplies makes successive generate calls return successive replies, and
// restores the real one afterwards. It honours Request.Stream the way the real
// one does, so the live output is exercised through the same seam as everything
// else.
func stubReplies(t *testing.T, replies ...string) *stub {
	t.Helper()
	s := &stub{t: t, replies: replies}
	real := generate
	generate = func(_ context.Context, req llm.Request) (llm.Result, error) {
		s.requests = append(s.requests, req)
		if len(s.requests) > len(s.replies) {
			s.t.Errorf("generate called %d times, but only %d replies were scripted", len(s.requests), len(s.replies))
			return llm.Result{}, errors.New("unscripted call")
		}
		reply := s.replies[len(s.requests)-1]
		if req.Stream != nil {
			if _, err := io.WriteString(req.Stream, reply); err != nil {
				return llm.Result{}, err
			}
		}
		return llm.Result{Text: reply}, nil
	}
	t.Cleanup(func() { generate = real })
	return s
}

// prompts returns the request text of every call made, in order.
func (s *stub) prompts() []string {
	out := make([]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = r.Prompt
	}
	return out
}

// The analyze step's reply. Only the <brief> block survives into step 2.
const briefReply = `Here is what I found.
<brief>
R1. Answer only from the supplied context.
</brief>`

// chainOf scripts one full run: the brief, then the same template back from
// each of the three steps that return one.
func chainOf(reply string) []string {
	return []string{briefReply, reply, reply, reply}
}

// A well-formed reply, in the shape the metaprompt is asked to produce.
const goodReply = `<Inputs>
{$QUESTION}
{$CONTEXT}
</Inputs>
<Instructions Structure>
Context first, then the question.
</Instructions Structure>
<Instructions>
Here is the context: <context>{$CONTEXT}</context>

Answer this question using only that context: <question>{$QUESTION}</question>

Write your answer in <answer> tags.
<answer></answer>
</Instructions>`

// A reply that quietly drops {{&CONTEXT}}.
const driftedReply = "<Instructions>\nAnswer {$QUESTION}. Ignore the context.\n</Instructions>"

func TestImproveWritesNextRevision(t *testing.T) {
	// Two runs, four steps each.
	stubReplies(t, append(chainOf(goodReply), chainOf(goodReply)...)...)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stderr bytes.Buffer
	if err := run([]string{src}, io.Discard, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}

	got := read(t, filepath.Join(dir, "reply.1.mustache"))
	// Converted back to mustache, each variable in the sigil it arrived with.
	if !strings.Contains(got, "{{&CONTEXT}}") {
		t.Errorf("CONTEXT was not converted back to mustache:\n%s", got)
	}
	if !strings.Contains(got, "{{QUESTION}}") || strings.Contains(got, "{$") {
		t.Errorf("QUESTION was not converted back to mustache:\n%s", got)
	}
	// Extraction trimmings: the planning block and the dangling empty pair.
	if strings.Contains(got, "Context first") || strings.Contains(got, "<answer></answer>") {
		t.Errorf("reply was not trimmed to the instructions:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("written template has no trailing newline")
	}
	// The original is left alone.
	if read(t, src) != "Answer {{QUESTION}} using {{&CONTEXT}}.\n" {
		t.Error("the source template was modified")
	}

	// A second run continues the series rather than overwriting.
	if err := run([]string{src}, io.Discard, &stderr); err != nil {
		t.Fatalf("second run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reply.2.mustache")); err != nil {
		t.Errorf("second run did not write reply.2.mustache: %v", err)
	}
}

// The reply lands on stdout as it arrives — the whole reply, not the template
// extracted from it, so there is something to watch during a slow rewrite.
func TestStreamsReplyToStdout(t *testing.T) {
	stubReplies(t, chainOf(goodReply)...)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stdout, stderr bytes.Buffer
	if err := run([]string{src}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}

	got := stdout.String()
	// The wrapper and the planning block are both trimmed out of the written
	// template, so finding them proves this is the raw reply.
	if !strings.Contains(got, "<Instructions>") || !strings.Contains(got, "Context first") {
		t.Errorf("the full reply is not on stdout:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("streamed reply does not end on its own line:\n%q", got)
	}
	// The progress and result lines stay on stderr, out of the reply's way.
	if strings.Contains(got, "improving ") || strings.Contains(got, "wrote ") {
		t.Errorf("stderr chatter leaked into stdout:\n%s", got)
	}
}

// The sigil {{&CONTEXT}} is deliberate — it suppresses HTML escaping — so a
// rewrite that quietly demotes it to {{CONTEXT}} is a compatibility break and
// must not be written. The chain gets two chances to put it back; when it
// still hasn't by the last step, that is the end of the run.
func TestImproveRejectsDrift(t *testing.T) {
	stubReplies(t, append(chainOf(driftedReply), chainOf(driftedReply)...)...)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	err := run([]string{src}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run() = nil, want a drift error")
	}
	if !strings.Contains(err.Error(), "dropped {{&CONTEXT}}") {
		t.Errorf("run() = %q, want it to name the dropped variable", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reply.1.mustache")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected rewrite was written anyway")
	}

	// -no-verify is the escape hatch: warn, but keep the rewrite.
	var stderr bytes.Buffer
	if err := run([]string{"-no-verify", src}, io.Discard, &stderr); err != nil {
		t.Fatalf("run(-no-verify) error = %v", err)
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("-no-verify did not warn:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "reply.1.mustache")); err != nil {
		t.Errorf("-no-verify did not write the rewrite: %v", err)
	}
}

// -stdout is for piping, so the result goes to stdout and nothing is written —
// and the live reply is silenced, or it would corrupt the redirect.
func TestImproveToStdout(t *testing.T) {
	stubReplies(t, chainOf(goodReply)...)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stdout bytes.Buffer
	if err := run([]string{"-stdout", src}, &stdout, io.Discard); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "{{&CONTEXT}}") {
		t.Errorf("improved template not on stdout:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "<Instructions>") {
		t.Errorf("the raw reply was streamed into -stdout's output:\n%s", stdout.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("-stdout wrote files: %v", entries)
	}
}

// Each step is built out of the one before it. This is the chain's whole
// premise, so it is checked link by link.
func TestChainFeedsEachStepForward(t *testing.T) {
	refined := strings.Replace(goodReply, "Context first, then the question.", "Refined.", 1)
	polished := strings.Replace(goodReply, "Context first, then the question.", "Polished.", 1)
	s := stubReplies(t, briefReply, goodReply, refined, polished)

	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stderr bytes.Buffer
	if err := run([]string{src}, io.Discard, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}

	got := s.prompts()
	if len(got) != 4 {
		t.Fatalf("made %d calls, want 4 (stderr: %s)", len(got), stderr.String())
	}
	// 1: analyze sees the original and nothing else.
	if !strings.Contains(got[0], "Answer {{QUESTION}} using {{&CONTEXT}}.") || strings.Contains(got[0], "Today you will be writing instructions") {
		t.Error("the analyze step did not get the original prompt on its own")
	}
	// 2: draft is the metaprompt call, now carrying the brief.
	if !strings.Contains(got[1], "Today you will be writing instructions") {
		t.Error("the draft step is not the metaprompt call")
	}
	if !strings.Contains(got[1], "R1. Answer only from the supplied context.") {
		t.Error("the brief did not reach the draft step")
	}
	if strings.Contains(got[1], "Here is what I found.") {
		t.Error("the analyze reply went in unextracted, prose and all")
	}
	// 3 and 4: each review step sees the template the step before produced.
	if !strings.Contains(got[2], "Here is the context:") || strings.Contains(got[2], "Refined.") {
		t.Error("the refine step did not get the draft's template")
	}
	if !strings.Contains(got[3], "Here is the context:") {
		t.Error("the polish step did not get the refine step's template")
	}
	// The written file is the last step's work, not the first's.
	if strings.Contains(read(t, filepath.Join(dir, "reply.1.mustache")), "Polished.") {
		t.Error("the planning block was not trimmed out of the final step")
	}
	// Progress is numbered so a slow run says where it is.
	if !strings.Contains(stderr.String(), "step 4/4 polish:") {
		t.Errorf("steps are not numbered on stderr:\n%s", stderr.String())
	}
}

// -single is the escape hatch back to one call, for a cheap pass.
func TestSingleMakesOneCall(t *testing.T) {
	s := stubReplies(t, goodReply)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stderr bytes.Buffer
	if err := run([]string{"-single", src}, io.Discard, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}
	if len(s.requests) != 1 {
		t.Fatalf("-single made %d calls, want 1", len(s.requests))
	}
	if !strings.Contains(s.requests[0].Prompt, "Today you will be writing instructions") {
		t.Error("-single did not make the metaprompt call")
	}
	if strings.Contains(s.requests[0].Prompt, "<brief>") {
		t.Error("-single sent a brief it never asked for")
	}
	if _, err := os.Stat(filepath.Join(dir, "reply.1.mustache")); err != nil {
		t.Errorf("-single wrote nothing: %v", err)
	}
}

// Drift used to end the run. Now it is handed to the next step to repair, and
// only the last step's drift is fatal.
func TestDriftIsHandedForwardForRepair(t *testing.T) {
	s := stubReplies(t, briefReply, driftedReply, goodReply, goodReply)
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stderr bytes.Buffer
	if err := run([]string{src}, io.Discard, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}

	got := s.prompts()
	// The refine step is told exactly what the draft lost.
	if !strings.Contains(got[2], "<drift>") || !strings.Contains(got[2], "dropped {{&CONTEXT}}") {
		t.Errorf("the refine step was not told about the drift:\n%s", got[2])
	}
	// The polish step, handed a repaired template, has nothing to repair.
	if strings.Contains(got[3], "<drift>") {
		t.Error("the polish step was told about drift that had already been fixed")
	}
	if !strings.Contains(read(t, filepath.Join(dir, "reply.1.mustache")), "{{&CONTEXT}}") {
		t.Error("the repaired template was not written")
	}
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "reply.mustache")
	write(t, src, "Answer {{QUESTION}} using {{&CONTEXT}}.\n")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-n", src}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v (stderr: %s)", err, stderr.String())
	}

	req := stdout.String()
	if !strings.Contains(req, "Answer {{QUESTION}} using {{&CONTEXT}}.") {
		t.Error("the existing prompt is not in the request")
	}
	if !strings.Contains(req, "no others: {$CONTEXT}, {$QUESTION}.") {
		t.Error("the steering does not pin the prompt's variables")
	}
	// Every step is shown, in order.
	for _, want := range []string{"=== step 1/4 analyze ===", "=== step 2/4 draft", "=== step 3/4 refine", "=== step 4/4 polish"} {
		if !strings.Contains(req, want) {
			t.Errorf("dry run is missing %q", want)
		}
	}
	// Steps 2-4 quote outputs that do not exist yet, and say so rather than
	// pretending the request is what would really be sent.
	if !strings.Contains(req, "«output of step 1 (analyze) goes here»") {
		t.Error("unknown upstream output is not marked as a placeholder")
	}
	if !strings.Contains(req, "upstream output shown as a placeholder") {
		t.Error("the placeholder steps are not labelled")
	}
	// A dry run must not write anything.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dry run wrote files: %v", entries)
	}

	// -single prints the one request it would send, with no chain scaffolding.
	var single bytes.Buffer
	if err := run([]string{"-n", "-single", src}, &single, &stderr); err != nil {
		t.Fatalf("run(-n -single) error = %v", err)
	}
	if strings.Contains(single.String(), "=== step") || strings.Contains(single.String(), "«output") {
		t.Errorf("-n -single printed chain scaffolding:\n%s", single.String())
	}
	if !strings.Contains(single.String(), "Today you will be writing instructions") {
		t.Error("-n -single did not print the metaprompt call")
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.mustache")
	write(t, empty, "   \n")

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no argument", []string{"-n"}, "expected exactly one"},
		{"two arguments", []string{"a.mustache", "b.mustache"}, "expected exactly one"},
		{"missing file", []string{"-n", filepath.Join(dir, "nope.mustache")}, "no such file"},
		{"empty file", []string{"-n", empty}, "nothing to improve"},
		{"unknown flag", []string{"-nope", empty}, "not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args, io.Discard, io.Discard)
			if err == nil {
				t.Fatal("run() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("run() = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// -prompts-dir replaces one template at a time; the rest stay embedded.
func TestPromptsDirOverride(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.mustache")
	write(t, src, "Do the thing with {{INPUT}}.\n")
	overrides := filepath.Join(dir, "overrides")
	if err := os.Mkdir(overrides, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(overrides, "steering.mustache"), "RESPOND IN LATIN.\n")

	var stdout bytes.Buffer
	if err := run([]string{"-n", "-prompts-dir", overrides, src}, &stdout, io.Discard); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	req := stdout.String()
	if !strings.HasSuffix(strings.TrimSpace(req), "RESPOND IN LATIN.") {
		t.Error("steering override was not applied")
	}
	if !strings.Contains(req, "Today you will be writing instructions") {
		t.Error("the metaprompt should have stayed at its embedded default")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
