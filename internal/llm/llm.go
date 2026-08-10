// Package llm wraps the GoAI SDK (github.com/zendev-sh/goai) so the rest of the
// tool never depends on a specific LLM vendor. Everything vendor-aware lives
// here; no other package imports goai.
//
// There is no fixed model list: the CLI passes whatever model id you give it
// straight through to Anthropic, so a model released tomorrow works without a
// code change. The key comes from ANTHROPIC_API_KEY, read by GoAI from the
// environment.
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/anthropic"
)

// APIKeyEnv is the environment variable GoAI reads the Anthropic key from.
const APIKeyEnv = "ANTHROPIC_API_KEY"

// ErrNoAPIKey is returned before any request is made when the key is unset.
// GoAI would otherwise surface this as an authentication failure from the API,
// several seconds and one confusing error message later.
var ErrNoAPIKey = errors.New(APIKeyEnv + " is not set")

// ErrTruncated is returned when the model stopped because it ran out of output
// tokens. The reply is then a half-written prompt template, so it is an error
// rather than a warning — same fail-fast as metaprompt.ipynb cell 12.
var ErrTruncated = errors.New("the model hit the output token limit before finishing")

// Request is one generation. Temperature is a pointer because "unset" and
// "zero" are different requests: Claude Sonnet 5 and later reject temperature=0
// outright, so the parameter is only sent when explicitly asked for.
type Request struct {
	Model       string
	Prompt      string
	MaxTokens   int
	Temperature *float64

	// Stream, when non-nil, receives the reply text as it arrives. The request
	// is streamed either way; this is only where the live copy goes.
	Stream io.Writer
}

// Result is the model's reply plus what it cost.
type Result struct {
	Text  string
	Usage provider.Usage
}

// Generate runs one completion, streaming it. The metaprompt is a single long
// turn with no tools and one answer, so there is no agentic loop to run — but
// writing that answer takes a while, and req.Stream lets the caller show it as
// it comes rather than after. The returned Result is the whole reply either
// way, and it is returned even alongside an error: by then some of it has
// usually already been printed.
func Generate(ctx context.Context, req Request) (Result, error) {
	if os.Getenv(APIKeyEnv) == "" {
		return Result{}, ErrNoAPIKey
	}

	opts := []goai.Option{
		goai.WithPrompt(req.Prompt),
		goai.WithMaxOutputTokens(req.MaxTokens),
	}
	if req.Temperature != nil {
		opts = append(opts, goai.WithTemperature(*req.Temperature))
	}

	ts, err := goai.StreamText(ctx, anthropic.Chat(req.Model), opts...)
	if err != nil {
		return Result{}, fmt.Errorf("generating with %s: %w", req.Model, err)
	}

	// A stream has to be drained to the end or its consume goroutine leaks, so
	// a failing sink stops the copying, not the reading. With no sink there is
	// nothing to copy and Result drains it for us.
	var writeErr error
	if req.Stream != nil {
		for chunk := range ts.TextStream() {
			if writeErr != nil {
				continue
			}
			if _, err := io.WriteString(req.Stream, chunk); err != nil {
				writeErr = fmt.Errorf("writing the reply: %w", err)
			}
		}
	}

	res := ts.Result()
	out := Result{Text: res.Text, Usage: res.TotalUsage}
	if writeErr != nil {
		return out, writeErr
	}
	// Errors part-way through a stream arrive as chunks rather than from the
	// call above, so this is the only place they surface.
	if err := ts.Err(); err != nil {
		return out, fmt.Errorf("generating with %s: %w", req.Model, err)
	}
	if res.FinishReason == provider.FinishLength {
		return out, ErrTruncated
	}
	return out, nil
}
