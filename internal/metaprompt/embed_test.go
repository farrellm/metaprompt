package metaprompt

import "strings"

import "testing"

// TestMetapromptIsVerbatim pins the embedded metaprompt to the exact bytes of
// the string literal in metaprompt.ipynb cell 6. The whole tool rests on that
// text being upstream's, unmodified, so any accidental reformat — an editor
// adding a trailing newline, a well-meaning rewrap, a "fixed" typo — has to fail
// loudly here rather than quietly change what the model is asked to do.
//
// To regenerate the file after a deliberate upstream change:
//
//	python3 -c 'import json; src="".join(json.load(open("metaprompt.ipynb"))["cells"][6]["source"]); \
//	  open("internal/metaprompt/metaprompt.mustache","w").write(src[src.index(chr(34)*3)+3:src.rindex(chr(34)*3)])'
func TestMetapromptIsVerbatim(t *testing.T) {
	const (
		wantLen  = 25536
		wantHead = "Today you will be writing instructions to an eager, helpful, but inexperienced a"
		wantTail = `gs") but do not include closing tags or unnecessary open-and-close tag sections.`
	)

	if got := len(Metaprompt); got != wantLen {
		t.Errorf("length = %d, want %d (metaprompt.mustache was modified)", got, wantLen)
	}
	if got := Metaprompt[:len(wantHead)]; got != wantHead {
		t.Errorf("head = %q, want %q", got, wantHead)
	}
	if got := Metaprompt[len(Metaprompt)-len(wantTail):]; got != wantTail {
		t.Errorf("tail = %q, want %q", got, wantTail)
	}
	// No trailing newline: the literal ends mid-line, and adding one would be a
	// silent edit of upstream text.
	if strings.HasSuffix(Metaprompt, "\n") {
		t.Error("metaprompt ends with a newline; the upstream literal does not")
	}
	// The one substitution slot the request builder fills.
	if got := strings.Count(Metaprompt, "{{TASK}}"); got != 1 {
		t.Errorf("{{TASK}} appears %d times, want exactly 1", got)
	}
}
