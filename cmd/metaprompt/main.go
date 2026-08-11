// Command metaprompt improves an LLM prompt stored as a mustache template.
//
// It reads name.mustache, asks Claude — through Anthropic's metaprompt — to
// rewrite it, and writes the result beside the original as name.1.mustache. The
// rewrite keeps the original's mustache variables, so a new revision is a
// drop-in replacement for whatever already renders the prompt.
//
// The rewrite runs as a four-step chain — analyze, draft, refine, polish — each
// step doing one job on the previous step's output. -single collapses it back to
// the one metaprompt call the chain grew out of.
//
//	metaprompt summarize.mustache
//	metaprompt -g "make it more concise" summarize.1.mustache
//	metaprompt -n summarize.mustache | less   # see the requests, spend nothing
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/farrellm/metaprompt/internal/llm"
	"github.com/farrellm/metaprompt/internal/metaprompt"
	"github.com/farrellm/metaprompt/internal/revision"
)

const (
	// defaultModel is a deliberate middle ground: the metaprompt is a 25 KB
	// prompt and rewriting is the whole point, so this is not a job for the
	// cheapest model. Override with -m for a cheaper check or a stronger pass.
	defaultModel = "claude-sonnet-5"
	// defaultMaxTokens comfortably fits a prompt template; ErrTruncated names
	// the flag when it doesn't.
	defaultMaxTokens = 8192
)

// generate is the one call that leaves the machine. It is a variable so the
// tests can drive the whole flow — extraction, conversion, verification, revision
// numbering, writing — against a canned reply without an API key.
var generate = llm.Generate

type options struct {
	model       string
	maxTokens   int
	temperature float64
	guidance    string
	out         string
	promptsDir  string
	single      bool
	stdout      bool
	dryRun      bool
	verbose     bool
	noVerify    bool
}

// steps returns how many model calls this run will make.
func (o options) steps() int {
	if o.single {
		return 1
	}
	return 4
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "metaprompt:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	var opts options
	fs := flag.NewFlagSet("metaprompt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: metaprompt [flags] <file.mustache>\n\n"+
			"Rewrites the prompt in <file.mustache> and writes the result as the next\n"+
			"revision beside it (foo.mustache -> foo.1.mustache). The original is never\n"+
			"modified, and the rewrite keeps its mustache variables.\n\n")
		fs.PrintDefaults()
	}

	// Go's flag package has no short-option concept; register the aliases
	// against the same variables so -m and -model both work.
	strVar := func(p *string, val string, names []string, usage string) {
		for i, n := range names {
			if i == 1 {
				usage = "alias for -" + names[0]
			}
			fs.StringVar(p, n, val, usage)
		}
	}
	boolVar := func(p *bool, names []string, usage string) {
		for i, n := range names {
			if i == 1 {
				usage = "alias for -" + names[0]
			}
			fs.BoolVar(p, n, false, usage)
		}
	}

	strVar(&opts.model, defaultModel, []string{"model", "m"}, "Anthropic model id")
	strVar(&opts.guidance, "", []string{"guidance", "g"}, "extra instruction for the rewrite, e.g. \"make it more concise\"")
	strVar(&opts.out, "", []string{"out", "o"}, "write to this path instead of the next revision")
	fs.StringVar(&opts.promptsDir, "prompts-dir", "", "directory of .mustache overrides (metaprompt, task, steering, analyze, refine, polish, thinking)")
	fs.IntVar(&opts.maxTokens, "max-tokens", defaultMaxTokens, "output token limit")
	fs.Float64Var(&opts.temperature, "temperature", -1, "sampling temperature; unset by default (Sonnet 5+ rejects 0)")
	fs.BoolVar(&opts.single, "single", false, "make one metaprompt call instead of the analyze/draft/refine/polish chain")
	boolVar(&opts.dryRun, []string{"dry-run", "n"}, "print the requests that would be sent and exit")
	boolVar(&opts.verbose, []string{"verbose", "v"}, "print the full reply and token usage to stderr")
	fs.BoolVar(&opts.stdout, "stdout", false, "write the result to stdout instead of a file (silences the live reply)")
	fs.BoolVar(&opts.noVerify, "no-verify", false, "downgrade variable-drift errors to warnings and write anyway")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("expected exactly one .mustache file")
	}
	// A flag left at its sentinel was never given, so no temperature is sent.
	var temperature *float64
	if isSet(fs, "temperature") {
		temperature = &opts.temperature
	}

	return improve(fs.Arg(0), opts, temperature, stdout, stderr)
}

func improve(path string, opts options, temperature *float64, stdout, stderr io.Writer) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(existing)) == "" {
		return fmt.Errorf("%s is empty; there is nothing to improve", path)
	}

	tmpl, err := metaprompt.LoadTemplates(opts.promptsDir)
	if err != nil {
		return fmt.Errorf("loading prompt overrides: %w", err)
	}
	tags := metaprompt.ParseTags(string(existing))

	if opts.dryRun {
		return dryRun(tmpl, string(existing), opts, tags, stdout)
	}

	// Ctrl-C cancels the in-flight request rather than orphaning it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The reply is printed as it arrives, except under -stdout: there stdout is
	// the finished template's channel, and a live copy would corrupt a redirect.
	// The request streams either way.
	sink := stdout
	if opts.stdout {
		sink = nil
	}
	c := &chain{opts: opts, temperature: temperature, path: path, tags: tags, stdout: stdout, stderr: stderr, sink: sink}

	// Step 1 works out what the existing prompt actually requires, so the steps
	// after the rewrite have something to check it against. -single skips it,
	// and the task template leaves its section out when the brief is empty.
	var brief string
	if !opts.single {
		request, err := tmpl.BuildAnalyze(string(existing))
		if err != nil {
			return err
		}
		reply, err := c.call(ctx, "analyze", request)
		if err != nil {
			return err
		}
		if brief, err = metaprompt.ExtractTag("brief", reply); err != nil {
			return err
		}
	}

	request, err := tmpl.BuildDraft(string(existing), opts.guidance, brief, tags)
	if err != nil {
		return err
	}
	reply, err := c.call(ctx, "draft", request)
	if err != nil {
		return err
	}
	// The whole chain stays in the metaprompt's {$NAME} syntax; the conversion
	// back to mustache happens once, at the end.
	draft, err := metaprompt.ExtractInstructions(reply)
	if err != nil {
		return err
	}

	// Steps 3 and 4 each get the previous step's drift as something to repair.
	// A rewrite that drops a variable used to end the run; now it gets a chance
	// to put it back, and only the last step's drift is fatal.
	for _, step := range []struct {
		name  string
		build func(guidance, brief, draft, drift string, tags metaprompt.Tags) (string, error)
	}{
		{"refine", tmpl.BuildRefine},
		{"polish", tmpl.BuildPolish},
	} {
		if opts.single {
			break
		}
		request, err := step.build(opts.guidance, brief, draft, driftReport(tags, draft), tags)
		if err != nil {
			return err
		}
		reply, err := c.call(ctx, step.name, request)
		if err != nil {
			return err
		}
		if draft, err = metaprompt.ExtractInstructions(reply); err != nil {
			return err
		}
	}

	if opts.verbose && opts.steps() > 1 {
		fmt.Fprintf(stderr, "total: %d in, %d out over %d steps\n", c.in, c.out, opts.steps())
	}

	improved := tags.ToMustache(draft)

	if err := tags.Verify(improved); err != nil {
		if !opts.noVerify {
			return fmt.Errorf("the rewrite is not a drop-in replacement:\n%w\n"+
				"re-run to get a different rewrite, or pass -no-verify to keep this one anyway", err)
		}
		fmt.Fprintf(stderr, "warning: the rewrite is not a drop-in replacement:\n%v\n", err)
	}

	// Templates are line-oriented text; the extraction trims the trailing
	// newline off, so put one back.
	improved += "\n"

	if opts.stdout {
		fmt.Fprint(stdout, improved)
		return nil
	}

	dst := opts.out
	if dst == "" {
		if dst, err = revision.NextPath(path); err != nil {
			return err
		}
	}
	if err := os.WriteFile(dst, []byte(improved), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "wrote", dst)
	return nil
}

// chain carries what every step of the chain needs and what accumulates across
// them. Each step is one model call; c.call is everything that surrounds it.
type chain struct {
	opts        options
	temperature *float64
	path        string
	tags        metaprompt.Tags
	stdout      io.Writer
	stderr      io.Writer
	sink        io.Writer

	step    int
	in, out int
}

// call runs one step and returns its raw reply.
func (c *chain) call(ctx context.Context, name, request string) (string, error) {
	c.step++
	fmt.Fprintf(c.stderr, "step %d/%d %s: improving %s with %s (%d variables)...\n",
		c.step, c.opts.steps(), name, c.path, c.opts.model, len(c.tags.Vars))

	res, err := generate(ctx, llm.Request{
		Model:       c.opts.model,
		Prompt:      request,
		MaxTokens:   c.opts.maxTokens,
		Temperature: c.temperature,
		Stream:      c.sink,
	})
	// Replies rarely end in a newline; without one the next line written would
	// continue the model's last.
	if c.sink != nil && res.Text != "" && !strings.HasSuffix(res.Text, "\n") {
		fmt.Fprintln(c.stdout)
	}
	c.in += res.Usage.InputTokens
	c.out += res.Usage.OutputTokens
	if c.opts.verbose {
		if res.Reasoning != "" {
			fmt.Fprintf(c.stderr, "\n--- %s thinking ---\n%s\n--- end thinking ---\n", name, res.Reasoning)
		}
		fmt.Fprintf(c.stderr, "\n--- %s reply ---\n%s\n--- end reply ---\n", name, res.Text)
		fmt.Fprintf(c.stderr, "tokens: %d in, %d out\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	if errors.Is(err, llm.ErrTruncated) {
		return "", fmt.Errorf("%s step: %w; raise -max-tokens above %d and try again", name, err, c.opts.maxTokens)
	}
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// driftReport describes how a step's output has drifted from the original's tag
// set, or returns "" when it hasn't. Verify wants mustache, so this converts a
// copy — the chain itself stays in {$NAME} until the end.
func driftReport(tags metaprompt.Tags, draft string) string {
	if err := tags.Verify(tags.ToMustache(draft)); err != nil {
		return err.Error()
	}
	return ""
}

// dryRun prints the requests without sending any of them. Only the first is
// fully honest: the rest quote outputs that do not exist yet, so those are
// shown as placeholders and labelled as such.
func dryRun(tmpl metaprompt.Templates, existing string, opts options, tags metaprompt.Tags, stdout io.Writer) error {
	if opts.single {
		request, err := tmpl.BuildDraft(existing, opts.guidance, "", tags)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, request)
		return nil
	}

	brief := placeholder(1, "analyze")
	requests := []struct {
		name  string
		build func() (string, error)
	}{
		{"analyze", func() (string, error) { return tmpl.BuildAnalyze(existing) }},
		{"draft", func() (string, error) { return tmpl.BuildDraft(existing, opts.guidance, brief, tags) }},
		{"refine", func() (string, error) {
			return tmpl.BuildRefine(opts.guidance, brief, placeholder(2, "draft"), driftPlaceholder(2), tags)
		}},
		{"polish", func() (string, error) {
			return tmpl.BuildPolish(opts.guidance, brief, placeholder(3, "refine"), driftPlaceholder(3), tags)
		}},
	}
	for i, r := range requests {
		request, err := r.build()
		if err != nil {
			return err
		}
		note := ""
		if i > 0 {
			note = " (upstream output shown as a placeholder)"
		}
		fmt.Fprintf(stdout, "=== step %d/%d %s%s ===\n\n%s\n\n", i+1, len(requests), r.name, note, request)
	}
	return nil
}

func placeholder(step int, name string) string {
	return fmt.Sprintf("«output of step %d (%s) goes here»", step, name)
}

// driftPlaceholder stands in for a drift report that a real run may not produce
// at all: the whole section is left out when the step before came back clean.
func driftPlaceholder(step int) string {
	return fmt.Sprintf("«what step %d's output changed about the placeholder set — this whole section is absent when it changed nothing»", step)
}

// isSet reports whether name was given on the command line, as opposed to
// sitting at its default.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
