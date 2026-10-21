package protected

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProtected(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"tests/goldens/ir/a.json", true},
		{"tests/unit/scratch/x", false},
		{"tests/protected.sha256", false},
		{"tools/ci/stage.go", true},
		{"tools/go.mod", true},
		{"tools/internal/licence/licence.go", false},
		{"pkg/diag/codes.lock", true},
		{"pkg/diag/diag.go", false},
	} {
		if got := Protected(DefaultGlobs, tc.path); got != tc.want {
			t.Errorf("Protected(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteThenReportChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, "tests/goldens/a.json", "a")
	write(t, root, "tests/goldens/b.json", "b")
	write(t, root, "pkg/layering_test.go", "// Package pkg …\n//bob:protected\npackage pkg\n")
	write(t, root, "pkg/other_test.go", "package pkg\n//bob:protected\n")
	write(t, root, "node_modules/x/tests/c", "c")
	if err := Write(ctx, root); err != nil {
		t.Fatal(err)
	}
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range m.Hashes {
		paths = append(paths, p)
	}
	want := []string{"pkg/layering_test.go", "tests/goldens/a.json", "tests/goldens/b.json"}
	if !samePaths(paths, want) {
		t.Fatalf("manifest paths %v, want %v", paths, want)
	}
	if !reflect.DeepEqual(m.Globs, DefaultGlobs) {
		t.Fatalf("globs %v", m.Globs)
	}
	again, err := Parse(bytes.NewReader(m.Format()))
	if err != nil || !reflect.DeepEqual(again, m) {
		t.Fatalf("format/parse round trip: %v", err)
	}

	r, err := Check(ctx, root, "main") // root is no git repo: main comparison is skipped
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Changes) != 0 || len(r.Notes) != 1 {
		t.Fatalf("clean tree: changes %v notes %v", r.Changes, r.Notes)
	}

	write(t, root, "tests/goldens/a.json", "A")
	write(t, root, "tests/goldens/new.json", "n")
	if err := os.Remove(filepath.Join(root, "tests/goldens/b.json")); err != nil {
		t.Fatal(err)
	}
	r, err = Check(ctx, root, "main")
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := []Change{
		{"changed", "tests/goldens/a.json", "manifest"},
		{"removed", "tests/goldens/b.json", "manifest"},
		{"added", "tests/goldens/new.json", "manifest"},
	}
	if !reflect.DeepEqual(r.Changes, wantChanges) {
		t.Fatalf("changes %v, want %v", r.Changes, wantChanges)
	}
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, p := range a {
		set[p] = true
	}
	for _, p := range b {
		if !set[p] {
			return false
		}
	}
	return true
}
