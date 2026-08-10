// Command metaprompt improves an LLM prompt stored as a mustache template.
//
// It reads name.mustache, asks Claude — through Anthropic's metaprompt — to
// rewrite it, and writes the result beside the original as name.1.mustache. The
// rewrite keeps the original's mustache variables, so a new revision is a
// drop-in replacement for whatever already renders the prompt.
//
//	metaprompt summarize.mustache
//	metaprompt -g "make it more concise" summarize.1.mustache
//	metaprompt -n summarize.mustache | less   # see the request, spend nothing
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
	stdout      bool
	dryRun      bool
	verbose     bool
	noVerify    bool
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
	fs.StringVar(&opts.promptsDir, "prompts-dir", "", "directory of metaprompt/task/steering .mustache overrides")
	fs.IntVar(&opts.maxTokens, "max-tokens", defaultMaxTokens, "output token limit")
	fs.Float64Var(&opts.temperature, "temperature", -1, "sampling temperature; unset by default (Sonnet 5+ rejects 0)")
	boolVar(&opts.dryRun, []string{"dry-run", "n"}, "print the request that would be sent and exit")
	boolVar(&opts.verbose, []string{"verbose", "v"}, "print the full reply and token usage to stderr")
	fs.BoolVar(&opts.stdout, "stdout", false, "write the result to stdout instead of a file")
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
	request, err := tmpl.BuildRequest(string(existing), opts.guidance, tags)
	if err != nil {
		return err
	}

	if opts.dryRun {
		fmt.Fprintln(stdout, request)
		return nil
	}

	// Ctrl-C cancels the in-flight request rather than orphaning it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(stderr, "improving %s with %s (%d variables)...\n", path, opts.model, len(tags.Vars))
	res, err := generate(ctx, llm.Request{
		Model:       opts.model,
		Prompt:      request,
		MaxTokens:   opts.maxTokens,
		Temperature: temperature,
	})
	if opts.verbose {
		fmt.Fprintf(stderr, "\n--- reply ---\n%s\n--- end reply ---\n", res.Text)
		fmt.Fprintf(stderr, "tokens: %d in, %d out\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	if errors.Is(err, llm.ErrTruncated) {
		return fmt.Errorf("%w; raise -max-tokens above %d and try again", err, opts.maxTokens)
	}
	if err != nil {
		return err
	}

	improved, err := metaprompt.ExtractInstructions(res.Text)
	if err != nil {
		return err
	}
	improved = tags.ToMustache(improved)

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
