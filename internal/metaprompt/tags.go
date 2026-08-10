package metaprompt

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cbroglie/mustache"
)

// This file owns the round trip between the two placeholder syntaxes in play.
// Prompt files are mustache ({{NAME}}), but the metaprompt only knows the
// cookbook's {$NAME} form and writes its templates that way. So: read the
// mustache tags out of the source, hand the metaprompt a {$NAME} list to pin
// its variables to, then convert its answer back and check nothing drifted.
// That check is the whole compatibility promise — an improved revision has to
// stay drop-in for whatever already renders the original.

// tagRe matches one mustache tag: a triple stash first, so {{{x}}} isn't read
// as {{ {x }} by the two-brace alternative.
var tagRe = regexp.MustCompile(`(?s)\{\{\{(.*?)\}\}\}|\{\{(.*?)\}\}`)

// dollarRe matches the metaprompt's {$NAME} placeholder form.
var dollarRe = regexp.MustCompile(`\{\$\s*([^{}]*?)\s*\}`)

// Sigil distinguishes the three ways mustache interpolates a variable. It has
// to survive the round trip: a template that deliberately emits raw text with
// {{{x}}} would start HTML-escaping if it came back as {{x}}.
type Sigil string

const (
	SigilPlain  Sigil = ""    // {{name}} — HTML-escaped
	SigilAmp    Sigil = "&"   // {{&name}} — unescaped
	SigilTriple Sigil = "{{{" // {{{name}}} — unescaped
)

// Tags is the mustache surface of a template: which variables it interpolates
// (and how), and which control tags — sections, partials, comments, delimiter
// changes — it contains.
type Tags struct {
	// Vars are variable names in order of first appearance, deduplicated.
	Vars []string
	// Sigils records how each name in Vars is interpolated. When one name is
	// used with two different sigils, the first one wins.
	Sigils map[string]Sigil
	// Controls are raw control tags in source order, kept as a multiset: a
	// section's open and close tags both have to come back.
	Controls []string
}

// ParseTags reads the mustache tags out of src. It scans with a regex rather
// than reaching into the mustache parser, both because that parser exposes no
// tag list and because a prompt with a malformed tag should still get as far as
// a useful error message.
func ParseTags(src string) Tags {
	t := Tags{Sigils: map[string]Sigil{}}
	for _, m := range tagRe.FindAllStringSubmatch(src, -1) {
		raw, inner, sigil := m[0], m[2], SigilPlain
		if m[1] != "" || strings.HasPrefix(raw, "{{{") {
			inner, sigil = m[1], SigilTriple
		}
		inner = strings.TrimSpace(inner)
		if inner == "" {
			continue
		}
		if sigil == SigilPlain {
			switch inner[0] {
			case '&':
				inner, sigil = strings.TrimSpace(inner[1:]), SigilAmp
			case '#', '^', '/', '>', '!', '=':
				t.Controls = append(t.Controls, raw)
				continue
			}
		}
		if inner == "" {
			continue
		}
		if _, seen := t.Sigils[inner]; !seen {
			t.Vars = append(t.Vars, inner)
			t.Sigils[inner] = sigil
		}
	}
	return t
}

// Tag renders name in the sigil it was found with, so ToMustache can put a
// variable back exactly as the source spelled it.
func (t Tags) Tag(name string) string {
	switch t.Sigils[name] {
	case SigilTriple:
		return "{{{" + name + "}}}"
	case SigilAmp:
		return "{{&" + name + "}}"
	default:
		return "{{" + name + "}}"
	}
}

// DollarList renders the variables in the {$NAME} form the metaprompt uses,
// sorted so the request is stable across runs.
func (t Tags) DollarList() string {
	names := append([]string(nil), t.Vars...)
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "{$" + n + "}"
	}
	return strings.Join(out, ", ")
}

// ControlList renders the control tags for the steering text, deduplicated and
// sorted for a stable request (the multiset matters when verifying the reply,
// not when asking for it).
func (t Tags) ControlList() string {
	seen := map[string]bool{}
	var uniq []string
	for _, c := range t.Controls {
		if !seen[c] {
			seen[c] = true
			uniq = append(uniq, c)
		}
	}
	sort.Strings(uniq)
	return strings.Join(uniq, ", ")
}

// ToMustache converts the metaprompt's {$NAME} placeholders back to mustache,
// restoring the sigil each name had in the source. Names the source didn't have
// become plain {{NAME}} — Verify is what rejects them; converting first means
// the error can show a readable template.
func (t Tags) ToMustache(s string) string {
	return dollarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := dollarRe.FindStringSubmatch(m)[1]
		return t.Tag(name)
	})
}

// Verify reports whether out is a usable improvement of a template whose tags
// are want: it must parse as mustache, interpolate exactly the same variables
// the same way, and carry the same control tags. Every problem found is
// reported at once, since a drifting rewrite usually drifts in several places.
func (t Tags) Verify(out string) error {
	var errs []error

	if _, err := mustache.ParseStringRaw(out, true); err != nil {
		errs = append(errs, fmt.Errorf("result is not a valid mustache template: %w", err))
	}

	got := ParseTags(out)
	wantTags := make([]string, len(t.Vars))
	for i, n := range t.Vars {
		wantTags[i] = t.Tag(n)
	}
	gotTags := make([]string, len(got.Vars))
	for i, n := range got.Vars {
		gotTags[i] = got.Tag(n)
	}
	if missing, extra := diff(wantTags, gotTags); len(missing) > 0 || len(extra) > 0 {
		errs = append(errs, fmt.Errorf("variables changed: %s", describe(missing, extra)))
	}
	if missing, extra := diff(t.Controls, got.Controls); len(missing) > 0 || len(extra) > 0 {
		errs = append(errs, fmt.Errorf("mustache tags changed: %s", describe(missing, extra)))
	}
	if leftover := dollarRe.FindAllString(out, -1); len(leftover) > 0 {
		errs = append(errs, fmt.Errorf("unconverted metaprompt placeholders remain: %s",
			strings.Join(dedupe(leftover), ", ")))
	}

	return errors.Join(errs...)
}

// diff compares two multisets of tags, returning what want has too much of and
// what got has too much of.
func diff(want, got []string) (missing, extra []string) {
	counts := map[string]int{}
	for _, w := range want {
		counts[w]++
	}
	for _, g := range got {
		counts[g]--
	}
	for _, tag := range sortedKeys(counts) {
		for i := 0; i < counts[tag]; i++ {
			missing = append(missing, tag)
		}
		for i := 0; i < -counts[tag]; i++ {
			extra = append(extra, tag)
		}
	}
	return missing, extra
}

func describe(missing, extra []string) string {
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "dropped "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "added "+strings.Join(extra, ", "))
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
