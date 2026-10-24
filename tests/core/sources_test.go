package core

// SPEC §1.4.1–§1.4.3 sources, hermetic steps, tests and services: R-FW-021, R-FW-023,
// R-FW-024, R-FW-025.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// sourcePaths returns node id → source store path of the repository at dir.
func sourcePaths(t *testing.T, dir string) (map[string]string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := build.SourcePaths(ctx, build.Request{Root: dir, Patterns: []string{"//..."}})
	if isNotImplemented(err) {
		harness.RedFeature(t, "build.SourcePaths")
		return nil, false
	}
	if err != nil {
		t.Errorf("build.SourcePaths: %v", err)
		return nil, false
	}
	if len(m) == 0 {
		t.Errorf("build.SourcePaths returned no source paths")
		return nil, false
	}
	for id, p := range m {
		if !regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-.+-src$`).MatchString(p) {
			t.Errorf("node %s: source path %q is not a store path named <name>-src", id, p)
		}
	}
	return m, true
}

func changedIDs(before, after map[string]string) []string {
	var out []string
	for id, p := range after {
		if before[id] != p {
			out = append(out, id)
		}
	}
	return out
}

// R-FW-021 (a): a gitignored file (root .gitignore, nested .gitignore, .git/info/exclude) or an
// edited bob.go changes no source store path.
func TestRFW021IgnoredFilesDoNotChangeSources(t *testing.T) {
	parity.Covers(t, "R-FW-021")
	dir := harness.Fixture(t, "acme")
	before, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	foo := filepath.Join(dir, "libs", "foo")
	writeFile(t, filepath.Join(dir, ".gitignore"), readOrEmpty(filepath.Join(dir, ".gitignore"))+"ignored_root.cpp\n")
	writeFile(t, filepath.Join(foo, ".gitignore"), "ignored_nested.cpp\n")
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), readOrEmpty(filepath.Join(dir, ".git", "info", "exclude"))+"ignored_exclude.cpp\n")
	for _, name := range []string{"ignored_root.cpp", "ignored_nested.cpp", "ignored_exclude.cpp"} {
		writeFile(t, filepath.Join(foo, "src", name), "int "+strings.TrimSuffix(name, ".cpp")+"() { return 1; }\n")
	}
	// Editing a bob.go (a comment) never changes a source path; bob.go is always excluded.
	replaceIn(t, filepath.Join(foo, "bob.go"), "// Lib is libfoo.so", "// Lib (edited) is libfoo.so")
	after, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if ch := changedIDs(before, after); len(ch) != 0 {
		t.Errorf("gitignored files / a bob.go edit changed the source paths of %v", ch)
	}
	// A file that is not ignored does change it (the checks above are not vacuous).
	writeFile(t, filepath.Join(foo, "src", "visible.cpp"), "int visible() { return 2; }\n")
	again, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if ch := changedIDs(after, again); len(ch) == 0 {
		t.Errorf("adding libs/foo/src/visible.cpp changed no source path")
	}
}

func readOrEmpty(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// R-FW-021 (b): changing one .cpp changes exactly the source path of the package(s) whose
// Sources contain it: here //libs/foo:foo's node(s) and no other.
func TestRFW021ChangeOneFileChangesOnePackage(t *testing.T) {
	parity.Covers(t, "R-FW-021")
	dir := harness.Fixture(t, "acme")
	before, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	g, ok := evalIR(t, dir)
	if !ok {
		return
	}
	var cpp string
	entries, err := os.ReadDir(filepath.Join(dir, "libs", "foo", "src"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".cpp") {
			cpp = filepath.Join(dir, "libs", "foo", "src", e.Name())
			break
		}
	}
	if cpp == "" {
		t.Fatal("harness: acme has no libs/foo/src/*.cpp")
	}
	writeFile(t, cpp, readFile(t, cpp)+"\n// changed\n")
	after, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	want := map[string]bool{}
	for _, n := range g.Nodes {
		for _, s := range n.Srcs {
			if s.Dir == "libs/foo" {
				want[n.ID] = true
			}
		}
	}
	if len(want) == 0 {
		t.Fatalf("harness: no IR node has srcs in libs/foo")
	}
	got := map[string]bool{}
	for _, id := range changedIDs(before, after) {
		got[id] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("node %s contains the changed file but its source path did not change", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("node %s does not contain the changed file but its source path changed", id)
		}
	}
}

// R-FW-021 (c): file modes other than the executable bit, and mtimes, do not change the path;
// the executable bit does.
func TestRFW021ModesAndMtimes(t *testing.T) {
	parity.Covers(t, "R-FW-021")
	dir := harness.Fixture(t, "acme")
	before, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	src := filepath.Join(dir, "libs", "foo", "src")
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) == 0 {
		t.Fatalf("harness: libs/foo/src: %v", err)
	}
	file := filepath.Join(src, entries[0].Name())
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	after, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if ch := changedIDs(before, after); len(ch) != 0 {
		t.Errorf("a mode change (0644 → 0600) and an mtime change moved the source paths of %v", ch)
	}
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	exec, ok := sourcePaths(t, dir)
	if !ok {
		return
	}
	if ch := changedIDs(after, exec); len(ch) == 0 {
		t.Errorf("setting the executable bit changed no source path")
	}
}

// R-FW-021 errors: pattern leaving the repo, pattern matching nothing, symlink out of the repo.
func TestRFW021SourceErrors(t *testing.T) {
	parity.Covers(t, "R-FW-021")
	cases := []struct {
		name, repo, setup string
		re                string
	}{
		{"escape", "srcerr/escape", "", `p/bob\.go:\d+(:\d+)?: source pattern "\.\./\.\./secret\.txt" leaves the repository`},
		{"nomatch", "srcerr/nomatch", "", `p/bob\.go:\d+(:\d+)?: bob\.Glob\("missing/\*\.txt"\) matches no files`},
		{"symlink", "srcerr/symlink", "link", `p/data/out: symlink points outside the repository`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := repo(t, c.repo)
			if c.setup == "link" {
				if err := os.Symlink("/etc/passwd", filepath.Join(dir, "p", "data", "out")); err != nil {
					t.Fatal(err)
				}
			}
			r := bobRun(t, dir, "build", "//...")
			if harness.Red(t, r) || !exitIs(t, r, 3) {
				return
			}
			if !regexp.MustCompile(c.re).MatchString(r.Stderr) {
				t.Errorf("stderr lacks %s:\n%s", c.re, r.Stderr)
			}
		})
	}
}
