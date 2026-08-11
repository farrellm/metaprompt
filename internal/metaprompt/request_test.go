package metaprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDraft(t *testing.T) {
	existing := "Summarize {{ARTICLE}} for {{&AUDIENCE}} & friends."
	tags := ParseTags(existing)

	req, err := DefaultTemplates().BuildDraft(existing, "Keep it under 200 words.", "", tags)
	if err != nil {
		t.Fatalf("BuildDraft() error = %v", err)
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
func TestBuildDraftNoTags(t *testing.T) {
	req, err := DefaultTemplates().BuildDraft("Write a haiku about Go.", "", "", Tags{})
	if err != nil {
		t.Fatalf("BuildDraft() error = %v", err)
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

func TestBuildDraftControlTags(t *testing.T) {
	existing := "{{#ITEMS}}- {{ITEM}}\n{{/ITEMS}}"
	req, err := DefaultTemplates().BuildDraft(existing, "", "", ParseTags(existing))
	if err != nil {
		t.Fatalf("BuildDraft() error = %v", err)
	}
	if !strings.Contains(req, "{{#ITEMS}}, {{/ITEMS}}. Reproduce every one of them verbatim") {
		t.Errorf("control tags not named in the steering:\n%s", req[len(req)-400:])
	}
}

// The brief is what the draft step gets out of the chain that -single doesn't.
func TestBuildDraftWithBrief(t *testing.T) {
	existing := "Summarize {{ARTICLE}}."
	tags := ParseTags(existing)

	plain, err := DefaultTemplates().BuildDraft(existing, "", "", tags)
	if err != nil {
		t.Fatalf("BuildDraft() error = %v", err)
	}
	if strings.Contains(plain, "<brief>") {
		t.Error("an empty brief still produced a <brief> block")
	}

	withBrief, err := DefaultTemplates().BuildDraft(existing, "", "R1. Stay under 200 words.", tags)
	if err != nil {
		t.Fatalf("BuildDraft() error = %v", err)
	}
	if !strings.Contains(withBrief, "<brief>\nR1. Stay under 200 words.\n</brief>") {
		t.Error("the brief is not in the draft request")
	}
	// The thinking requirements are held back until the metaprompt's examples
	// have had their say — arguing with them here is a fight they win.
	if strings.Contains(withBrief, "extended thinking enabled") {
		t.Error("the thinking requirements leaked into the draft request")
	}
}

// The two review steps take the same inputs and wear the same trailers.
func TestBuildReviewSteps(t *testing.T) {
	existing := "Summarize {{ARTICLE}} for {{&AUDIENCE}}."
	tags := ParseTags(existing)
	tm := DefaultTemplates()

	for _, tt := range []struct {
		name  string
		build func(guidance, brief, draft, drift string, tags Tags) (string, error)
	}{
		{"refine", tm.BuildRefine},
		{"polish", tm.BuildPolish},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := tt.build("Keep it short.", "R1. Name the audience.", "Summarize {$ARTICLE}.", "dropped {{&AUDIENCE}}", tags)
			if err != nil {
				t.Fatalf("build error = %v", err)
			}
			for _, want := range []string{
				"<brief>\nR1. Name the audience.\n</brief>",
				"<draft>\nSummarize {$ARTICLE}.\n</draft>",
				"<drift>\ndropped {{&AUDIENCE}}\n</drift>",
				"Keep it short.",
				"extended thinking enabled",
			} {
				if !strings.Contains(req, want) {
					t.Errorf("request is missing %q", want)
				}
			}
			// The metaprompt is the draft step's alone; resending 25 KB of it
			// here would be the chain's whole cost.
			if strings.Contains(req, "Today you will be writing instructions") {
				t.Error("the metaprompt leaked into a review step")
			}
			// Steering goes last, so the format instruction is the final word.
			if !strings.HasSuffix(req, "no others: {$ARTICLE}, {$AUDIENCE}.") {
				t.Errorf("request does not end with the variable steering:\n%s", req[len(req)-300:])
			}
		})
	}

	// With nothing to repair, the drift block stays out of the way.
	req, err := tm.BuildRefine("", "R1.", "Summarize {$ARTICLE}.", "", tags)
	if err != nil {
		t.Fatalf("BuildRefine() error = %v", err)
	}
	if strings.Contains(req, "<drift>") {
		t.Error("a clean draft still produced a <drift> block")
	}
}

func TestBuildAnalyze(t *testing.T) {
	existing := "Summarize {{ARTICLE}} for {{&AUDIENCE}} & friends."
	req, err := DefaultTemplates().BuildAnalyze(existing)
	if err != nil {
		t.Fatalf("BuildAnalyze() error = %v", err)
	}
	if !strings.Contains(req, "<existing_prompt>\n"+existing+"\n</existing_prompt>") {
		t.Error("existing prompt not embedded verbatim")
	}
	if strings.Contains(req, "&amp;") {
		t.Error("request was HTML-escaped; render with forceRaw")
	}
	if !strings.Contains(req, "<brief>") {
		t.Error("the analyze step does not name the block it wants back")
	}
	// It returns prose, not a template, so the template steering has no business
	// being here.
	if strings.Contains(req, "Start your response directly with the <Inputs> block.") {
		t.Error("template steering leaked into the analyze request")
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
	if tm.Analyze != defaultAnalyze || tm.Refine != defaultRefine || tm.Polish != defaultPolish || tm.Thinking != defaultThinking {
		t.Error("the chain templates did not fall back to the embedded defaults")
	}
}

// Every step is overridable, not just the ones that predate the chain.
func TestLoadTemplatesOverridesChainSteps(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"analyze.mustache":  "ANALYZE {{{EXISTING}}}",
		"refine.mustache":   "REFINE {{{DRAFT}}}",
		"polish.mustache":   "POLISH {{{DRAFT}}}",
		"thinking.mustache": "THINK HARD.",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tm, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	req, err := tm.BuildAnalyze("the prompt")
	if err != nil {
		t.Fatalf("BuildAnalyze() error = %v", err)
	}
	if req != "ANALYZE the prompt" {
		t.Errorf("analyze override not applied: %q", req)
	}

	req, err = tm.BuildPolish("", "brief", "the draft", "", Tags{})
	if err != nil {
		t.Fatalf("BuildPolish() error = %v", err)
	}
	if !strings.HasPrefix(req, "POLISH the draft\n\nTHINK HARD.") {
		t.Errorf("polish and thinking overrides not applied: %q", req)
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
