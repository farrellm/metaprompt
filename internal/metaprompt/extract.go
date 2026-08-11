package metaprompt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// The metaprompt answers with three blocks — <Inputs>, <Instructions Structure>
// and <Instructions> — of which only the last is the prompt template. This is a
// port of the extraction in metaprompt.ipynb cell 16.

// blockRes caches one compiled matcher per tag name. A matcher captures the first
// block with that exact name: <Instructions Structure> deliberately does not
// match <Instructions>, because the tag name there is followed by a space, not
// a '>'.
var (
	blockResMu sync.Mutex
	blockRes   = map[string]*regexp.Regexp{}
)

func blockRe(name string) *regexp.Regexp {
	blockResMu.Lock()
	defer blockResMu.Unlock()
	re, ok := blockRes[name]
	if !ok {
		re = regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(name) + `>(.+?)</` + regexp.QuoteMeta(name) + `>`)
		blockRes[name] = re
	}
	return re
}

// emptyTagRe matches an empty tag pair at the end of the text. Go's RE2 has no
// backreferences, so the two names are captured separately and compared in
// removeEmptyTags. The optional newline mirrors Python's `$`, which matches
// before a single trailing newline.
var emptyTagRe = regexp.MustCompile(`(?s)<(\w+)></(\w+)>\n?$`)

// ErrNoInstructions is returned when the reply has no <Instructions> block —
// usually because the model wandered off the requested format, or the response
// was cut short.
var ErrNoInstructions = errors.New("no <Instructions> block in the model's reply (re-run with -verbose to see it)")

// ExtractInstructions pulls the prompt template out of a reply. Every step of
// the chain that returns a template returns it this way.
func ExtractInstructions(response string) (string, error) {
	out, err := ExtractTag("Instructions", response)
	if err != nil {
		return "", ErrNoInstructions
	}
	return out, nil
}

// ExtractTag pulls the contents of the first <name> block out of a reply.
//
// The metaprompt is told to name opening tags without closing them, and models
// often close them anyway, leaving an empty pair dangling at the end. The
// double removal (with a trim between) is the notebook's, and clears the two
// that a nested pair leaves behind.
func ExtractTag(name, response string) (string, error) {
	m := blockRe(name).FindStringSubmatch(response)
	if m == nil {
		return "", fmt.Errorf("no <%s> block in the model's reply (re-run with -verbose to see it)", name)
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
