package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// recorder captures failures so the test can assert that Assert fails.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper()                   {}
func (r *recorder) Name() string              { return "recorder" }
func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }
func (r *recorder) Logf(string, ...any)       {}
func (r *recorder) Failed() bool              { return len(r.errs) > 0 }

// tempGolden writes an approved golden below the repo root in a throwaway directory.
func tempGolden(t *testing.T, content string) string {
	t.Helper()
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, "out"), "golden-selftest-")
	if err != nil {
		if err := os.MkdirAll(filepath.Join(root, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
		if dir, err = os.MkdirTemp(filepath.Join(root, "out"), "golden-selftest-"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestApprovedGolden(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	out := fmt.Sprintf("built %s/pkg at 2026-10-21T09:15:00Z\ncache %s/.cache\nstore /nix/store/0abc-hello\n", root, home)
	Assert(t, "tests/goldens/harness/normalise.txt", []byte(out), Human())
}

func TestMismatchFails(t *testing.T) {
	t.Setenv(EnvUpdate, "0")
	if Updating() {
		t.Skip("update mode")
	}
	rel := tempGolden(t, "a\nb\n")
	tests := []struct {
		name, path, got string
		fail            bool
	}{
		{"equal", rel, "a\nb\n", false},
		{"changed line", rel, "a\nc\n", true},
		{"extra line", rel, "a\nb\nc\n", true},
		{"missing golden", rel + ".nope", "a\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recorder{TB: t}
			Assert(r, tt.path, []byte(tt.got))
			if r.Failed() != tt.fail {
				t.Fatalf("failed=%v, want %v: %v", r.Failed(), tt.fail, r.errs)
			}
		})
	}
}

func TestUpdateWritesCandidate(t *testing.T) {
	rel := tempGolden(t, "old\n")
	cand := t.TempDir()
	t.Setenv(EnvUpdate, "1")
	t.Setenv(EnvCandidates, cand)
	r := &recorder{TB: t}
	Assert(r, rel, []byte("new\n"), Reqs("R-FW-047"))
	if r.Failed() {
		t.Fatalf("update mode failed: %v", r.errs)
	}
	b, err := os.ReadFile(filepath.Join(cand, rel))
	if err != nil || string(b) != "new\n" {
		t.Fatalf("candidate = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(cand, MetaDir, rel+".json")); err != nil {
		t.Fatalf("meta: %v", err)
	}
	root, _ := Root()
	if b, _ := os.ReadFile(filepath.Join(root, rel)); string(b) != "old\n" {
		t.Fatalf("update mode touched the golden: %q", b)
	}
	// A now-matching output removes the stale candidate.
	Assert(r, rel, []byte("old\n"))
	if _, err := os.Stat(filepath.Join(cand, rel)); !os.IsNotExist(err) {
		t.Fatalf("stale candidate kept: %v", err)
	}
}

func TestNormalise(t *testing.T) {
	tests := []struct {
		name, in, want string
		human          bool
	}{
		{"root before home", "/home/u/bob/x /home/u/y", "$REPO/x $HOME/y", false},
		{"timestamps human", "at 2026-10-21 09:15:00.123+02:00", "at <t>", true},
		{"timestamps machine kept", "at 2026-10-21T09:15:00Z", "at 2026-10-21T09:15:00Z", false},
		{"store hash kept", "/nix/store/0abc-hello", "/nix/store/0abc-hello", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Normalise([]byte(tt.in), "/home/u/bob", "/home/u", tt.human))
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeHook(t *testing.T) {
	t.Setenv(EnvUpdate, "0")
	rel := tempGolden(t, "id=<id>\n")
	r := &recorder{TB: t}
	Assert(r, rel, []byte("id=42\n"), Normalize(func(b []byte) []byte { return []byte("id=<id>\n") }))
	if r.Failed() {
		t.Fatalf("hook not applied: %v", r.errs)
	}
}
