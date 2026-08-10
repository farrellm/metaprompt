// Package revision numbers the successive revisions of a prompt file.
//
// A prompt starts life as name.mustache and each improvement lands beside it as
// name.1.mustache, name.2.mustache, and so on. The original is never touched,
// so a revision you don't like is deleted rather than undone, and the whole
// history of a prompt is visible in one directory listing.
package revision

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// numbered matches the ".N" a revision carries before its extension.
var numbered = regexp.MustCompile(`\.(\d+)$`)

// NextPath returns the path of the next revision of path.
//
// Both name.mustache and name.3.mustache yield the same answer — one past the
// highest revision already on disk — so improving a revision continues the
// original's numbering instead of starting a second series (name.3.1.mustache).
// The returned path is guaranteed not to exist.
func NextPath(path string) (string, error) {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	// A revision's stem is the original's: name.3 -> name.
	base = numbered.ReplaceAllString(base, "")
	if base == "" {
		return "", fmt.Errorf("%s has no name to number", path)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n, ok := revisionOf(e.Name(), base, ext)
		if ok && n > highest {
			highest = n
		}
	}

	next := filepath.Join(dir, fmt.Sprintf("%s.%d%s", base, highest+1, ext))
	// ReadDir already ruled this out; the check costs nothing and turns a race
	// or a symlink surprise into an error instead of an overwrite.
	if _, err := os.Lstat(next); err == nil {
		return "", fmt.Errorf("%s already exists", next)
	}
	return next, nil
}

// revisionOf reports the revision number name carries, if it is a revision of
// base with extension ext.
func revisionOf(name, base, ext string) (int, bool) {
	stem, ok := strings.CutSuffix(name, ext)
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutPrefix(stem, base+".")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
