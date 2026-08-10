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

// Templates are the three pieces of the request. Metaprompt is upstream's
// multi-shot prompt with its {{TASK}} slot; Task is what goes in that slot —
// the existing prompt plus the instruction to rewrite it; Steering is appended
// after the metaprompt to pin the response format and the variable set.
//
// Splitting them out means the parts worth tuning can be tuned without touching
// the verbatim text.
type Templates struct {
	Metaprompt string
	Task       string
	Steering   string
}

// DefaultTemplates returns the embedded templates.
func DefaultTemplates() Templates {
	return Templates{Metaprompt: Metaprompt, Task: defaultTask, Steering: defaultSteering}
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

// BuildRequest assembles the full user turn: the existing prompt wrapped as a
// task, substituted into the metaprompt, with the steering appended.
//
// guidance is an optional extra instruction from the caller ("make it more
// concise"); tags are the mustache tags of the existing prompt, which the
// steering pins the rewrite to.
//
// Everything renders with forceRaw: the default mustache renderer HTML-escapes
// interpolated values, which would turn the XML tags this whole prompt is built
// from into &lt;…&gt;.
func (tm Templates) BuildRequest(existing, guidance string, tags Tags) (string, error) {
	taskCtx := map[string]any{"EXISTING": strings.TrimSpace(existing)}
	if guidance != "" {
		taskCtx["GUIDANCE"] = guidance
	}
	task, err := mustache.RenderRaw(tm.Task, true, taskCtx)
	if err != nil {
		return "", fmt.Errorf("rendering task template: %w", err)
	}

	body, err := mustache.RenderRaw(tm.Metaprompt, true, map[string]any{"TASK": strings.TrimSpace(task)})
	if err != nil {
		return "", fmt.Errorf("rendering metaprompt: %w", err)
	}

	steerCtx := map[string]any{}
	if len(tags.Vars) > 0 {
		steerCtx["VARS"] = tags.DollarList()
	}
	if len(tags.Controls) > 0 {
		steerCtx["CONTROLS"] = tags.ControlList()
	}
	steering, err := mustache.RenderRaw(tm.Steering, true, steerCtx)
	if err != nil {
		return "", fmt.Errorf("rendering steering template: %w", err)
	}

	return body + "\n\n" + strings.TrimRight(steering, "\n"), nil
}
