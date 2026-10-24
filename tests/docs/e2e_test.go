package docs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestQuickstartsE2E runs the example projects of the documentation end to end with a real bob
// (opt-in: it needs network access and takes minutes per page). For each page whose path
// matches BOB_DOCS_E2E_PAGES (default ^03-quickstart/), it creates an empty git repository,
// then walks the page's blocks in order:
//
//   - ```<lang> file=<path>: write the file;
//   - ```bash run (optionally exit=<n>, default 0): run the block with bash -e in the project,
//     with the directory of $BOB_DOCS_E2E first on PATH; output goes to a file, so a command
//     the block starts in the background does not hold the test;
//   - ```text expect: every line must match a line of the preceding run block's output, in
//     order; "…" matches any text, a line that is only "…" is skipped, runs of white space
//     compare equal.
//
// Processes a page started in the background are killed when the page ends. The environment is
// the test's (set BOB_HOME to a fresh directory for outputs that say "built" rather than
// "cached"). Projects are kept under BOB_DOCS_E2E_DIR when it is set.
//
//	BOB_DOCS_E2E=$PWD/bob BOB_HOME=/var/tmp/docs-home go test ./tests/docs -run TestQuickstartsE2E -v -timeout 3h
func TestQuickstartsE2E(t *testing.T) {
	bobBin := os.Getenv("BOB_DOCS_E2E")
	if bobBin == "" {
		t.Skip("set BOB_DOCS_E2E to the path of a bob binary to run the documentation end to end")
	}
	bobBin, err := filepath.Abs(bobBin)
	if err != nil {
		t.Fatal(err)
	}
	filter := regexp.MustCompile(`^03-quickstart/`)
	if f := os.Getenv("BOB_DOCS_E2E_PAGES"); f != "" {
		filter = regexp.MustCompile(f)
	}
	work := os.Getenv("BOB_DOCS_E2E_DIR")
	if work == "" {
		work = t.TempDir()
	}
	path := filepath.Dir(bobBin) + string(os.PathListSeparator) + os.Getenv("PATH")
	ran := 0
	for _, p := range loadPages(t, repoRoot(t)) {
		if !filter.MatchString(p.rel) {
			continue
		}
		ran++
		t.Run(p.rel, func(t *testing.T) {
			dir := filepath.Join(work, strings.TrimSuffix(strings.ReplaceAll(p.rel, "/", "_"), ".md"))
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			git := exec.Command("git", "init", "-q")
			git.Dir = dir
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			var groups []int
			defer func() {
				for _, g := range groups {
					_ = syscall.Kill(-g, syscall.SIGTERM)
				}
			}()
			last := ""
			for i, b := range p.blocks {
				if name, ok := b.attrs["file"]; ok {
					f := filepath.Join(dir, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(f, []byte(b.content), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if _, ok := b.attrs["run"]; ok {
					want := 0
					if v, ok := b.attrs["exit"]; ok {
						if want, err = strconv.Atoi(v); err != nil {
							t.Fatalf("%s:%d: exit=%q is not a number", p.rel, b.line, v)
						}
					}
					out, code, pgid := runBlock(t, dir, path, i, b.content)
					groups = append(groups, pgid)
					t.Logf("%s:%d $ %s(exit %d)\n%s", p.rel, b.line, b.content, code, out)
					if code != want {
						t.Fatalf("%s:%d: the block exited %d, want %d", p.rel, b.line, code, want)
					}
					last = out
					continue
				}
				if _, ok := b.attrs["expect"]; ok {
					if err := matchLines(last, b.content); err != nil {
						t.Errorf("%s:%d: %v", p.rel, b.line, err)
					}
				}
			}
		})
	}
	if ran == 0 {
		t.Fatalf("no page matches %s", filter)
	}
}

// runBlock runs one run block with bash -e in dir and returns its combined output, exit status
// and process group.
func runBlock(t *testing.T, dir, path string, n int, script string) (string, int, int) {
	t.Helper()
	outFile := filepath.Join(dir, ".bob-docs-e2e."+strconv.Itoa(n)+".out")
	f, err := os.Create(outFile)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outFile)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-e", "-o", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+path)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Run()
	_ = f.Close()
	out, rerr := os.ReadFile(outFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run block: %v", err)
		}
		code = ee.ExitCode()
	}
	pgid := 0
	if cmd.Process != nil {
		pgid = cmd.Process.Pid
	}
	return string(out), code, pgid
}

// matchLines checks that every line of want matches a line of got, in order (see
// TestQuickstartsE2E for the rules).
func matchLines(got, want string) error {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	lines := strings.Split(got, "\n")
	i := 0
	for _, w := range strings.Split(want, "\n") {
		w = norm(w)
		if w == "" || w == "…" {
			continue
		}
		var re strings.Builder
		re.WriteString("^")
		for k, part := range strings.Split(w, "…") {
			if k > 0 {
				re.WriteString(".*")
			}
			re.WriteString(regexp.QuoteMeta(part))
		}
		re.WriteString("$")
		rx := regexp.MustCompile(re.String())
		found := false
		for ; i < len(lines); i++ {
			if rx.MatchString(norm(lines[i])) {
				found, i = true, i+1
				break
			}
		}
		if !found {
			return fmt.Errorf("expected output line %q not found (in order) in:\n%s", w, got)
		}
	}
	return nil
}
