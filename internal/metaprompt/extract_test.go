package metaprompt

import (
	"errors"
	"strings"
	"testing"
)

// reply is shaped like a real metaprompt response: the three blocks it is asked
// for, and a dangling empty tag pair from the model closing a tag the
// metaprompt told it to leave open.
const reply = `<Inputs>
{$ARTICLE}
</Inputs>
<Instructions Structure>
First the article, then the directions.
</Instructions Structure>
<Instructions>
Here is the article: <article>{$ARTICLE}</article>

Summarize it inside <summary> tags.
<summary></summary>
</Instructions>`

func TestExtractInstructions(t *testing.T) {
	got, err := ExtractInstructions(reply)
	if err != nil {
		t.Fatalf("ExtractInstructions() error = %v", err)
	}
	if strings.Contains(got, "<summary></summary>") {
		t.Errorf("trailing empty tag pair survived:\n%s", got)
	}
	if !strings.HasPrefix(got, "Here is the article:") || !strings.HasSuffix(got, "inside <summary> tags.") {
		t.Errorf("extracted block not trimmed to the instructions:\n%s", got)
	}
	// The planning block must not leak into the template.
	if strings.Contains(got, "First the article") {
		t.Errorf("<Instructions Structure> leaked into the result:\n%s", got)
	}
}

func TestExtractInstructionsMissing(t *testing.T) {
	_, err := ExtractInstructions("<Inputs>\n{$ARTICLE}\n</Inputs>\nI cannot help with that.")
	if !errors.Is(err, ErrNoInstructions) {
		t.Errorf("error = %v, want ErrNoInstructions", err)
	}
}

func TestRemoveEmptyTags(t *testing.T) {
	tests := []struct{ in, want string }{
		{"body\n<answer></answer>", "body\n"},
		{"body\n<answer></answer>\n", "body\n"},
		// Mismatched names are not a pair, and content between them means the
		// tag is in use — neither should be touched.
		{"body\n<answer></response>", "body\n<answer></response>"},
		{"body\n<answer>text</answer>", "body\n<answer>text</answer>"},
		// Only a trailing pair is removed; one mid-template is deliberate.
		{"<answer></answer>\nbody", "<answer></answer>\nbody"},
	}
	for _, tt := range tests {
		if got := removeEmptyTags(tt.in); got != tt.want {
			t.Errorf("removeEmptyTags(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
