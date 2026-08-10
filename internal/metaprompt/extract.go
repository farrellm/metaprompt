package metaprompt

import (
	"errors"
	"regexp"
	"strings"
)

// The metaprompt answers with three blocks — <Inputs>, <Instructions Structure>
// and <Instructions> — of which only the last is the prompt template. This is a
// port of the extraction in metaprompt.ipynb cell 16.

// instructionsRe captures the first <Instructions> block. <Instructions
// Structure> deliberately does not match: the tag name is followed by a space,
// not a '>'.
var instructionsRe = regexp.MustCompile(`(?s)<Instructions>(.+?)</Instructions>`)

// emptyTagRe matches an empty tag pair at the end of the text. Go's RE2 has no
// backreferences, so the two names are captured separately and compared in
// removeEmptyTags. The optional newline mirrors Python's `$`, which matches
// before a single trailing newline.
var emptyTagRe = regexp.MustCompile(`(?s)<(\w+)></(\w+)>\n?$`)

// ErrNoInstructions is returned when the reply has no <Instructions> block —
// usually because the model wandered off the requested format, or the response
// was cut short.
var ErrNoInstructions = errors.New("no <Instructions> block in the model's reply (re-run with -verbose to see it)")

// ExtractInstructions pulls the prompt template out of a metaprompt reply.
//
// The metaprompt is told to name opening tags without closing them, and models
// often close them anyway, leaving an empty pair dangling at the end. The
// double removal (with a trim between) is the notebook's, and clears the two
// that a nested pair leaves behind.
func ExtractInstructions(response string) (string, error) {
	m := instructionsRe.FindStringSubmatch(response)
	if m == nil {
		return "", ErrNoInstructions
	}
	inner := removeEmptyTags(m[1])
	return strings.TrimSpace(removeEmptyTags(strings.TrimSpace(inner))), nil
}

// removeEmptyTags strips one trailing empty tag pair, e.g. "<answer></answer>".
func removeEmptyTags(s string) string {
	loc := emptyTagRe.FindStringSubmatchIndex(s)
	if loc == nil {
		return s
	}
	if s[loc[2]:loc[3]] != s[loc[4]:loc[5]] {
		return s
	}
	return s[:loc[0]]
}
