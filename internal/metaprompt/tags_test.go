package metaprompt

import (
	"strings"
	"testing"
)

const sample = `Summarize {{ARTICLE}} for {{&AUDIENCE}}.
{{! internal note }}
{{#SECTIONS}}
- {{{SECTION}}}
{{/SECTIONS}}
Mention {{ARTICLE}} again.`

func TestParseTags(t *testing.T) {
	tags := ParseTags(sample)

	wantVars := []string{"ARTICLE", "AUDIENCE", "SECTION"}
	if got := strings.Join(tags.Vars, ","); got != strings.Join(wantVars, ",") {
		t.Errorf("Vars = %v, want %v", tags.Vars, wantVars)
	}
	// SECTIONS is a section, not a variable, even though SECTION is.
	if _, ok := tags.Sigils["SECTIONS"]; ok {
		t.Error("section name SECTIONS was read as a variable")
	}
	for name, want := range map[string]Sigil{"ARTICLE": SigilPlain, "AUDIENCE": SigilAmp, "SECTION": SigilTriple} {
		if got := tags.Sigils[name]; got != want {
			t.Errorf("sigil of %s = %q, want %q", name, got, want)
		}
	}
	wantControls := []string{"{{! internal note }}", "{{#SECTIONS}}", "{{/SECTIONS}}"}
	if got := strings.Join(tags.Controls, ","); got != strings.Join(wantControls, ",") {
		t.Errorf("Controls = %v, want %v", tags.Controls, wantControls)
	}
	if got, want := tags.DollarList(), "{$ARTICLE}, {$AUDIENCE}, {$SECTION}"; got != want {
		t.Errorf("DollarList() = %q, want %q", got, want)
	}
}

// TestRoundTrip is the compatibility promise in miniature: a rewrite that uses
// the same variables in the metaprompt's {$NAME} form comes back spelled
// exactly as the source spelled it, and verifies clean.
func TestRoundTrip(t *testing.T) {
	tags := ParseTags(sample)
	rewritten := tags.ToMustache(`Read {$ARTICLE}, aimed at {$AUDIENCE}.
{{! internal note }}
{{#SECTIONS}}
- {$SECTION}
{{/SECTIONS}}
Cite {$ARTICLE}.`)

	for _, want := range []string{"{{ARTICLE}}", "{{&AUDIENCE}}", "{{{SECTION}}}"} {
		if !strings.Contains(rewritten, want) {
			t.Errorf("round trip lost %s:\n%s", want, rewritten)
		}
	}
	if err := tags.Verify(rewritten); err != nil {
		t.Errorf("Verify() = %v, want nil", err)
	}
}

func TestVerifyRejectsDrift(t *testing.T) {
	tags := ParseTags(sample)

	tests := []struct {
		name, out, wantErr string
	}{
		{
			name:    "dropped variable",
			out:     "Summarize {{ARTICLE}}.\n{{! internal note }}\n{{#SECTIONS}}\n- {{{SECTION}}}\n{{/SECTIONS}}",
			wantErr: "dropped {{&AUDIENCE}}",
		},
		{
			name:    "renamed variable",
			out:     "Summarize {{TEXT}} for {{&AUDIENCE}}.\n{{! internal note }}\n{{#SECTIONS}}\n- {{{SECTION}}}\n{{/SECTIONS}}",
			wantErr: "added {{TEXT}}",
		},
		{
			name:    "escaping changed",
			out:     "Summarize {{ARTICLE}} for {{AUDIENCE}}.\n{{! internal note }}\n{{#SECTIONS}}\n- {{{SECTION}}}\n{{/SECTIONS}}",
			wantErr: "dropped {{&AUDIENCE}}",
		},
		{
			name:    "section dropped",
			out:     "Summarize {{ARTICLE}} for {{&AUDIENCE}}. {{{SECTION}}}\n{{! internal note }}",
			wantErr: "mustache tags changed",
		},
		{
			name:    "unconverted placeholder",
			out:     "Summarize {{ARTICLE}} for {{&AUDIENCE}}, see {$ARTICLE}.\n{{! internal note }}\n{{#SECTIONS}}\n- {{{SECTION}}}\n{{/SECTIONS}}",
			wantErr: "unconverted metaprompt placeholders remain: {$ARTICLE}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tags.Verify(tt.out)
			if err == nil {
				t.Fatal("Verify() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Verify() = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// A template with no tags at all is legitimate — a fixed prompt can still be
// improved — and must not be treated as drift.
func TestVerifyPlainText(t *testing.T) {
	tags := ParseTags("Write a haiku about Go.")
	if err := tags.Verify("Write a haiku about the Go programming language."); err != nil {
		t.Errorf("Verify() = %v, want nil", err)
	}
}
