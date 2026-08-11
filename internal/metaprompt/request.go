package metaprompt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cbroglie/mustache"
)

//go:embed task.mustache
var defaultTask string

//go:embed steering.mustache
var defaultSteering string

//go:embed analyze.mustache
var defaultAnalyze string

//go:embed refine.mustache
var defaultRefine string

//go:embed polish.mustache
var defaultPolish string

//go:embed thinking.mustache
var defaultThinking string

// Templates are the pieces the four requests are assembled from.
//
// Metaprompt is upstream's multi-shot prompt with its {{TASK}} slot; Task is
// what goes in that slot — the existing prompt plus the instruction to rewrite
// it. Analyze, Refine and Polish are the other three steps of the chain.
// Steering is appended to every step that returns a template, pinning the
// response format and the variable set; Thinking is appended to the two steps
// that are allowed to undo the metaprompt's chain-of-thought scaffolding.
//
// Splitting them out means the parts worth tuning can be tuned without touching
// the verbatim text.
type Templates struct {
	Metaprompt string
	Task       string
	Steering   string
	Analyze    string
	Refine     string
	Polish     string
	Thinking   string
}

// DefaultTemplates returns the embedded templates.
func DefaultTemplates() Templates {
	return Templates{
		Metaprompt: Metaprompt,
		Task:       defaultTask,
		Steering:   defaultSteering,
		Analyze:    defaultAnalyze,
		Refine:     defaultRefine,
		Polish:     defaultPolish,
		Thinking:   defaultThinking,
	}
}

// LoadTemplates returns the defaults with any file present in dir substituted
// for its embedded counterpart. Each file falls back independently, so
// overriding the steering doesn't oblige you to copy the 25 KB metaprompt too.
// An empty dir means "defaults only".
func LoadTemplates(dir string) (Templates, error) {
	tm := DefaultTemplates()
	if dir == "" {
		return tm, nil
	}
	for name, dst := range map[string]*string{
		"metaprompt.mustache": &tm.Metaprompt,
		"task.mustache":       &tm.Task,
		"steering.mustache":   &tm.Steering,
		"analyze.mustache":    &tm.Analyze,
		"refine.mustache":     &tm.Refine,
		"polish.mustache":     &tm.Polish,
		"thinking.mustache":   &tm.Thinking,
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err == nil:
			*dst = string(b)
		case os.IsNotExist(err):
			// Keep the embedded default.
		default:
			return Templates{}, err
		}
	}
	return tm, nil
}

// The four builders below are the four steps of the chain. Each returns one
// complete user turn.
//
// Everything renders with forceRaw: the default mustache renderer HTML-escapes
// interpolated values, which would turn the XML tags these prompts are built
// from into &lt;…&gt;.

// BuildAnalyze asks for the brief: what the existing prompt requires, in a form
// the later steps can check a rewrite against. It is the one step that returns
// prose rather than a template, so no steering is appended.
func (tm Templates) BuildAnalyze(existing string) (string, error) {
	out, err := render(tm.Analyze, "analyze template", map[string]any{
		"EXISTING": strings.TrimSpace(existing),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// BuildDraft assembles the metaprompt request: the existing prompt wrapped as a
// task, substituted into the metaprompt, with the steering appended.
//
// guidance is an optional extra instruction from the caller ("make it more
// concise"); brief is the analyze step's output, empty under -single; tags are
// the mustache tags of the existing prompt, which the steering pins the rewrite
// to.
//
// The thinking requirements are deliberately not appended here. At this point
// they would be one paragraph arguing with 25 KB of multishot examples that
// demonstrate the opposite, and losing; the later steps strip the scaffolding
// instead.
func (tm Templates) BuildDraft(existing, guidance, brief string, tags Tags) (string, error) {
	taskCtx := map[string]any{"EXISTING": strings.TrimSpace(existing)}
	if guidance != "" {
		taskCtx["GUIDANCE"] = guidance
	}
	if brief != "" {
		taskCtx["BRIEF"] = strings.TrimSpace(brief)
	}
	task, err := render(tm.Task, "task template", taskCtx)
	if err != nil {
		return "", err
	}

	body, err := render(tm.Metaprompt, "metaprompt", map[string]any{"TASK": strings.TrimSpace(task)})
	if err != nil {
		return "", err
	}

	steering, err := tm.renderSteering(tags)
	if err != nil {
		return "", err
	}
	return body + "\n\n" + steering, nil
}

// BuildRefine asks for the draft corrected against the brief, with any
// placeholder drift repaired.
func (tm Templates) BuildRefine(guidance, brief, draft, drift string, tags Tags) (string, error) {
	return tm.buildReview(tm.Refine, "refine template", guidance, brief, draft, drift, tags)
}

// BuildPolish asks for the last editing pass: how the template reads, not what
// it asks for.
func (tm Templates) BuildPolish(guidance, brief, draft, drift string, tags Tags) (string, error) {
	return tm.buildReview(tm.Polish, "polish template", guidance, brief, draft, drift, tags)
}

// buildReview is the shape the refine and polish steps share: the brief and the
// template so far, then the thinking requirements, then the steering. Both
// return a template, so both get both trailers.
func (tm Templates) buildReview(tmpl, what, guidance, brief, draft, drift string, tags Tags) (string, error) {
	ctx := map[string]any{
		"BRIEF": strings.TrimSpace(brief),
		"DRAFT": strings.TrimSpace(draft),
	}
	if guidance != "" {
		ctx["GUIDANCE"] = guidance
	}
	if drift != "" {
		ctx["DRIFT"] = strings.TrimSpace(drift)
	}
	body, err := render(tmpl, what, ctx)
	if err != nil {
		return "", err
	}

	thinking, err := render(tm.Thinking, "thinking template", map[string]any{})
	if err != nil {
		return "", err
	}

	steering, err := tm.renderSteering(tags)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(body, "\n") + "\n\n" + strings.TrimRight(thinking, "\n") + "\n\n" + steering, nil
}

// renderSteering pins the response format and the tag set the reply has to come
// back with. The optional sections drop out when the prompt has no tags of that
// kind, rather than asking the model to preserve an empty list.
func (tm Templates) renderSteering(tags Tags) (string, error) {
	ctx := map[string]any{}
	if len(tags.Vars) > 0 {
		ctx["VARS"] = tags.DollarList()
	}
	if len(tags.Controls) > 0 {
		ctx["CONTROLS"] = tags.ControlList()
	}
	out, err := render(tm.Steering, "steering template", ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

func render(tmpl, what string, ctx map[string]any) (string, error) {
	out, err := mustache.RenderRaw(tmpl, true, ctx)
	if err != nil {
		return "", fmt.Errorf("rendering %s: %w", what, err)
	}
	return out, nil
}
