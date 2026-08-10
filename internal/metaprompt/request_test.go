package metaprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRequest(t *testing.T) {
	existing := "Summarize {{ARTICLE}} for {{&AUDIENCE}} & friends."
	tags := ParseTags(existing)

	req, err := DefaultTemplates().BuildRequest(existing, "Keep it under 200 words.", tags)
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}

	// The metaprompt survives intact on both sides of the substitution — in
	// particular its {$NAME} examples must not be mistaken for tags and eaten.
	before, after, _ := strings.Cut(Metaprompt, "{{TASK}}")
	if !strings.HasPrefix(req, before) {
		t.Error("the metaprompt text before {{TASK}} was altered")
	}
	if !strings.Contains(req, after) {
		t.Error("the metaprompt text after {{TASK}} was altered")
	}
	if strings.Contains(req, "{{TASK}}") {
		t.Error("{{TASK}} slot was not filled")
	}
	// forceRaw: the existing prompt goes in unescaped, ampersand and all.
	if !strings.Contains(req, "<existing_prompt>\n"+existing+"\n</existing_prompt>") {
		t.Errorf("existing prompt not embedded verbatim; got:\n%s", req[len(req)-2000:])
	}
	if strings.Contains(req, "&amp;") {
		t.Error("request was HTML-escaped; render with forceRaw")
	}
	if !strings.Contains(req, "Keep it under 200 words.") {
		t.Error("guidance missing from request")
	}
	// Steering, ported from the notebook, pins format and variables.
	if !strings.HasSuffix(req, "must use exactly these input variables and no others: {$ARTICLE}, {$AUDIENCE}.") {
		t.Errorf("request does not end with the variable steering:\n%s", req[len(req)-300:])
	}
	if !strings.Contains(req, "Start your response directly with the <Inputs> block.") {
		t.Error("format steering missing from request")
	}
}

// Without variables or control tags the optional steering lines disappear
// rather than pinning the rewrite to an empty list.
func TestBuildRequestNoTags(t *testing.T) {
	req, err := DefaultTemplates().BuildRequest("Write a haiku about Go.", "", Tags{})
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if strings.Contains(req, "input variables and no others") {
		t.Error("variable steering emitted for a template with no variables")
	}
	if strings.Contains(req, "mustache tags") {
		t.Error("control-tag steering emitted for a template with no control tags")
	}
	if strings.Contains(req, "Additionally, follow this guidance") {
		t.Error("guidance line emitted with no guidance")
	}
}

func TestBuildRequestControlTags(t *testing.T) {
	existing := "{{#ITEMS}}- {{ITEM}}\n{{/ITEMS}}"
	req, err := DefaultTemplates().BuildRequest(existing, "", ParseTags(existing))
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if !strings.Contains(req, "{{#ITEMS}}, {{/ITEMS}}. Reproduce every one of them verbatim") {
		t.Errorf("control tags not named in the steering:\n%s", req[len(req)-400:])
	}
}

func TestLoadTemplatesOverridesPerFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "steering.mustache"), []byte("ANSWER IN LATIN."), 0o644); err != nil {
		t.Fatal(err)
	}

	tm, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}
	if tm.Steering != "ANSWER IN LATIN." {
		t.Errorf("steering = %q, want the override", tm.Steering)
	}
	// The files absent from the dir keep their embedded defaults.
	if tm.Metaprompt != Metaprompt || tm.Task != defaultTask {
		t.Error("absent overrides did not fall back to the embedded defaults")
	}
}

func TestLoadTemplatesEmptyDir(t *testing.T) {
	tm, err := LoadTemplates("")
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}
	if tm != DefaultTemplates() {
		t.Error(`LoadTemplates("") differs from the defaults`)
	}
}
