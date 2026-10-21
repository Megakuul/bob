// Package harness is the shared base of the acceptance suites under tests/ (HARNESS.md §2):
// it builds bob's binaries once per test binary, runs them in a scratch copy of a fixture
// repository, and recognises the "not implemented" answer of a stubbed feature.
//
// A red acceptance test (before its feature exists) must fail for exactly one reason: the
// feature answers BOB-E-9008. Tests call Red or RequireImplemented right after running the
// feature, so a red test says why it is red, and a test that fails for any other reason is a
// real failure. Tests are never skipped to make CI green (AGENTS.md §5.2).
package harness

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
)

// NotImplemented is the code every not-yet-built feature returns (diag.CodeNotImplemented).
const NotImplemented = "BOB-E-9008"

var (
	binsOnce sync.Once
	bins     map[string]string
	binsErr  error
)

// Bin returns the path of a freshly built binary: "bob", "bob-server" or "bob-builder".
// All binaries are built once per test binary; a build failure is a harness error.
func Bin(t testing.TB, name string) string {
	t.Helper()
	binsOnce.Do(func() {
		root, err := serverenv.Root()
		if err != nil {
			binsErr = err
			return
		}
		dir, err := os.MkdirTemp("", "bob-harness-bin-")
		if err != nil {
			binsErr = err
			return
		}
		bins = map[string]string{}
		for _, n := range []string{"bob", "bob-server", "bob-builder"} {
			out := filepath.Join(dir, n)
			cmd := exec.Command("go", "build", "-o", out, "./cmd/"+n)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				binsErr = fmt.Errorf("go build ./cmd/%s: %w: %s: %w", n, err, b, serverenv.ErrHarness)
				return
			}
			bins[n] = out
		}
	})
	if binsErr != nil {
		t.Fatal(binsErr)
	}
	b, ok := bins[name]
	if !ok {
		t.Fatalf("harness: unknown binary %q", name)
	}
	return b
}

// Root is the repository root.
func Root(t testing.TB) string {
	t.Helper()
	r, err := serverenv.Root()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Result is one finished process.
type Result struct {
	Args           []string
	Stdout, Stderr string
	Exit           int // -1: did not start or was killed
}

// Combined is stdout followed by stderr.
func (r Result) Combined() string { return r.Stdout + r.Stderr }

// Spec describes one run.
type Spec struct {
	Dir     string        // working directory (default: a fresh temp dir)
	Env     []string      // added to a minimal environment (PATH, HOME, TZ=UTC, LC_ALL=C)
	Stdin   string        // standard input
	Timeout time.Duration // default 2 minutes
}

// Run runs binary name (see Bin) with args.
func Run(t testing.TB, name string, s Spec, args ...string) Result {
	t.Helper()
	return RunPath(t, Bin(t, name), s, args...)
}

// RunPath runs an arbitrary executable.
func RunPath(t testing.TB, path string, s Spec, args ...string) Result {
	t.Helper()
	if s.Timeout == 0 {
		s.Timeout = 2 * time.Minute
	}
	if s.Dir == "" {
		s.Dir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = s.Dir
	home := filepath.Join(t.TempDir(), "home")
	_ = os.MkdirAll(home, 0o755)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home, "TZ=UTC", "LC_ALL=C"}, s.Env...)
	if s.Stdin != "" {
		cmd.Stdin = strings.NewReader(s.Stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := Result{Args: args}
	err := cmd.Run()
	r.Stdout, r.Stderr = so.String(), se.String()
	switch e := err.(type) {
	case nil:
	case *exec.ExitError:
		r.Exit = e.ExitCode()
	default:
		r.Exit = -1
		r.Stderr += "\nharness: " + err.Error()
	}
	return r
}

// IsNotImplemented reports whether r is the stub answer.
func (r Result) IsNotImplemented() bool {
	return r.Exit != 0 && strings.Contains(r.Combined(), NotImplemented)
}

// Red marks t failed because the feature behind r is not built yet. Use it as the first check
// after running a stubbed feature: `harness.Red(t, r)` returns true (and has failed t) when r
// is the BOB-E-9008 answer, so the caller returns. The failure text names the feature.
func Red(t testing.TB, r Result) bool {
	t.Helper()
	if !r.IsNotImplemented() {
		return false
	}
	t.Errorf("red: not implemented yet: bob %s -> %s", strings.Join(r.Args, " "), firstLine(r.Combined()))
	return true
}

// RedFeature fails t as red for a feature exercised without a binary (a library entry point
// that still returns diag.NotImplemented).
func RedFeature(t testing.TB, feature string) {
	t.Helper()
	t.Errorf("red: not implemented yet: %s", feature)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Fixture copies tests/fixtures/repos/<name> into a fresh temp dir, runs `git init` and commits
// the tree, and returns the directory. The outer repository has no nested .git (HARNESS §4.1).
func Fixture(t testing.TB, name string) string {
	t.Helper()
	src := filepath.Join(Root(t), "tests", "fixtures", "repos", name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, out)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("harness: copy fixture %s: %v", name, err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness: git %v: %v: %s", args, err, b)
		}
	}
	return dst
}
