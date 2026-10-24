package cli

import (
	"strings"
	"testing"
)

// TestProjectSlug: the default --remote project is a valid server project id (letter first, no
// trailing '-', ≤ 40 characters) derived from the repository directory name.
func TestProjectSlug(t *testing.T) {
	for in, want := range map[string]string{
		"remote":                  "remote",
		"My Repo.git":             "my-repo-git",
		"2024_app":                "app",
		"--x--":                   "x",
		"lines":                   "lines",
		"123":                     "",
		strings.Repeat("ab-", 30): strings.Repeat("ab-", 13) + "a",
	} {
		got := slug(in)
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !projectSlug().MatchString(got) {
			t.Errorf("slug(%q) = %q is not a project id", in, got)
		}
	}
}
