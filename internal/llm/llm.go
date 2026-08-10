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
}

// Result is the model's reply plus what it cost.
type Result struct {
	Text  string
	Usage provider.Usage
}

// Generate runs one non-streaming completion. The metaprompt is a single long
// turn with no tools and one answer, so there is nothing here to stream and no
// agentic loop to run.
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

	res, err := goai.GenerateText(ctx, anthropic.Chat(req.Model), opts...)
	if err != nil {
		return Result{}, fmt.Errorf("generating with %s: %w", req.Model, err)
	}
	if res.FinishReason == provider.FinishLength {
		return Result{Text: res.Text, Usage: res.TotalUsage}, ErrTruncated
	}
	return Result{Text: res.Text, Usage: res.TotalUsage}, nil
}
