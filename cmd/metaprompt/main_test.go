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

// stubReply makes generate return reply, and restores the real one afterwards.
// It honours Request.Stream the way the real one does, so the live output is
// exercised through the same seam as everything else.
func stubReply(t *testing.T, reply string) {
	t.Helper()
	real := generate
	generate = func(_ context.Context, req llm.Request) (llm.Result, error) {
		if req.Stream != nil {
			if _, err := io.WriteString(req.Stream, reply); err != nil {
				return llm.Result{}, err
			}
		}
		return llm.Result{Text: reply}, nil
	}
	t.Cleanup(func() { generate = real })
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

func TestImproveWritesNextRevision(t *testing.T) {
	stubReply(t, goodReply)
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
	stubReply(t, goodReply)
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
// must not be written.
func TestImproveRejectsDrift(t *testing.T) {
	stubReply(t, "<Instructions>\nAnswer {$QUESTION}. Ignore the context.\n</Instructions>")
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
	stubReply(t, goodReply)
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
	// A dry run must not write anything.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dry run wrote files: %v", entries)
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
