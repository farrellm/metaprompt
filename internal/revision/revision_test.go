package revision

import (
	"os"
	"path/filepath"
	"testing"
)

func seed(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestNextPath(t *testing.T) {
	tests := []struct {
		name   string
		seeded []string
		input  string
		want   string
	}{
		{
			name:   "first revision",
			seeded: []string{"foo.mustache"},
			input:  "foo.mustache",
			want:   "foo.1.mustache",
		},
		{
			name:   "continues from the highest",
			seeded: []string{"foo.mustache", "foo.1.mustache", "foo.2.mustache"},
			input:  "foo.mustache",
			want:   "foo.3.mustache",
		},
		{
			// Improving a revision continues the series rather than starting
			// foo.2.1.mustache.
			name:   "input is itself a revision",
			seeded: []string{"foo.mustache", "foo.1.mustache", "foo.2.mustache"},
			input:  "foo.2.mustache",
			want:   "foo.3.mustache",
		},
		{
			// Gaps happen when a bad revision is deleted; the next one goes
			// after the highest, not into the hole.
			name:   "gap in the series",
			seeded: []string{"foo.mustache", "foo.5.mustache"},
			input:  "foo.mustache",
			want:   "foo.6.mustache",
		},
		{
			name:   "other prompts are ignored",
			seeded: []string{"foo.mustache", "bar.7.mustache", "foo-bar.3.mustache", "foo.1.txt"},
			input:  "foo.mustache",
			want:   "foo.1.mustache",
		},
		{
			name:   "non-numeric suffix is not a revision",
			seeded: []string{"foo.mustache", "foo.draft.mustache"},
			input:  "foo.mustache",
			want:   "foo.1.mustache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := seed(t, tt.seeded...)
			got, err := NextPath(filepath.Join(dir, tt.input))
			if err != nil {
				t.Fatalf("NextPath() error = %v", err)
			}
			if want := filepath.Join(dir, tt.want); got != want {
				t.Errorf("NextPath() = %s, want %s", got, want)
			}
		})
	}
}

// Each call reports the next free slot, so writing between calls walks the
// series forward instead of repeating one number.
func TestNextPathAdvancesAsFilesAppear(t *testing.T) {
	dir := seed(t, "foo.mustache")
	src := filepath.Join(dir, "foo.mustache")

	for _, want := range []string{"foo.1.mustache", "foo.2.mustache", "foo.3.mustache"} {
		got, err := NextPath(src)
		if err != nil {
			t.Fatalf("NextPath() error = %v", err)
		}
		if got != filepath.Join(dir, want) {
			t.Fatalf("NextPath() = %s, want %s", got, want)
		}
		if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNextPathMissingDir(t *testing.T) {
	if _, err := NextPath(filepath.Join(t.TempDir(), "nope", "foo.mustache")); err == nil {
		t.Error("NextPath() = nil error for a missing directory")
	}
}
