package mat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkSrc(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for f, c := range files {
		p := filepath.Join(d, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFreshWriteThenNoop(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "package a\n", "sub/b.go": "package sub\n"})
	w := []Want{{Dir: "svc/gen/a", Label: "//p:a", Source: src}}
	r, err := Apply(repo, w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Written != 2 {
		t.Fatalf("written=%d", r.Written)
	}
	if got := read(t, filepath.Join(repo, "svc/gen/a/.gitignore")); got != IgnoreBody {
		t.Fatalf("gitignore %q", got)
	}
	fi, _ := os.Stat(filepath.Join(repo, "svc/gen/a/a.go"))
	if fi.Mode().Perm() != 0o444 {
		t.Fatalf("mode %v", fi.Mode())
	}
	r, err = Apply(repo, w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Written != 0 || r.Hashed != 0 || r.Roots[0].State != "fresh" {
		t.Fatalf("no-op wrote=%d hashed=%d state=%s", r.Written, r.Hashed, r.Roots[0].State)
	}
}

func TestEditedFileIsConflictAndForceBacksUp(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "package a\n"})
	w := []Want{{Dir: "gen/a", Label: "//p:a", Source: src}}
	if _, err := Apply(repo, w, Options{}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(repo, "gen/a/a.go")
	os.Chmod(p, 0o644)
	os.WriteFile(p, []byte("package a // my edit\n"), 0o644)
	src2 := mkSrc(t, map[string]string{"a.go": "package a // v2\n"})
	w[0].Source = src2
	_, err := Apply(repo, w, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Conflicts[0].Kind != "edited" {
		t.Fatalf("want edited conflict, got %v", err)
	}
	if read(t, p) != "package a // my edit\n" {
		t.Fatal("edit was overwritten without --force")
	}
	t.Log(err)
	r, err := Apply(repo, w, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, p) != "package a // v2\n" || len(r.Backups) != 1 {
		t.Fatalf("force: %v", r)
	}
	if read(t, filepath.Join(repo, r.Backups[0])) != "package a // my edit\n" {
		t.Fatal("backup lost the edit")
	}
}

func TestForeignFileIsConflict(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "package a\n"})
	os.MkdirAll(filepath.Join(repo, "gen/a"), 0o755)
	os.WriteFile(filepath.Join(repo, "gen/a/handwritten.go"), []byte("package a\n"), 0o644)
	_, err := Apply(repo, []Want{{Dir: "gen/a", Label: "//p:a", Source: src}}, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Conflicts[0].Kind != "foreign" {
		t.Fatalf("want foreign conflict, got %v", err)
	}
	t.Log(err)
}

func TestStaleFilesRemovedAndChangedRewritten(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "1", "old.go": "x", "d/e.go": "e"})
	w := []Want{{Dir: "gen/a", Label: "//p:a", Source: src}}
	Apply(repo, w, Options{})
	w[0].Source = mkSrc(t, map[string]string{"a.go": "2", "new.go": "n"})
	r, err := Apply(repo, w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Written != 2 || r.Removed != 2 {
		t.Fatalf("written=%d removed=%d", r.Written, r.Removed)
	}
	if _, err := os.Stat(filepath.Join(repo, "gen/a/d")); !os.IsNotExist(err) {
		t.Fatal("empty dir left behind")
	}
	if read(t, filepath.Join(repo, "gen/a/a.go")) != "2" {
		t.Fatal("not rewritten")
	}
}

func TestDeletedGeneratedFileIsRestored(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "1"})
	w := []Want{{Dir: "gen/a", Label: "//p:a", Source: src}}
	Apply(repo, w, Options{})
	os.Remove(filepath.Join(repo, "gen/a/a.go"))
	r, err := Apply(repo, w, Options{})
	if err != nil || r.Written != 1 {
		t.Fatalf("restore: %v %v", r, err)
	}
}

func TestOrphanPruned(t *testing.T) {
	repo := t.TempDir()
	a := mkSrc(t, map[string]string{"a.go": "1"})
	b := mkSrc(t, map[string]string{"b.go": "1"})
	Apply(repo, []Want{{"gen/a", "//p:a", a}, {"gen/b", "//p:b", b}}, Options{})
	r, err := Apply(repo, []Want{{"gen/a", "//p:a", a}}, Options{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "gen/b")); !os.IsNotExist(err) {
		t.Fatalf("orphan dir still there: %+v", r.Roots)
	}
	own, _ := Owned(repo)
	if len(own) != 1 || own[0] != "gen/a" {
		t.Fatalf("manifest roots %v", own)
	}
}

func TestOrphanWithEditKept(t *testing.T) {
	repo := t.TempDir()
	b := mkSrc(t, map[string]string{"b.go": "1"})
	Apply(repo, []Want{{"gen/b", "//p:b", b}}, Options{})
	p := filepath.Join(repo, "gen/b/b.go")
	os.Chmod(p, 0o644)
	os.WriteFile(p, []byte("edited"), 0o644)
	_, err := Apply(repo, nil, Options{Prune: true})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want conflict, got %v", err)
	}
	if read(t, p) != "edited" {
		t.Fatal("edited orphan deleted")
	}
}

// Crash after the write-ahead manifest and some files: the next run (with different desired
// content) must not report bob's own half-written files as foreign.
func TestCrashRecovery(t *testing.T) {
	repo := t.TempDir()
	v1 := mkSrc(t, map[string]string{"a.go": "1", "b.go": "1"})
	w := []Want{{"gen/a", "//p:a", v1}}
	Apply(repo, w, Options{})
	v2 := mkSrc(t, map[string]string{"a.go": "2", "b.go": "2", "c.go": "2"})
	// simulate: pending written, a.go and c.go replaced, crash before final manifest
	m, _ := load(repo)
	want, _ := desired(v2, nil, &Result{})
	m.Roots["gen/a"].Pending, m.Roots["gen/a"].PendingSource = want, v2
	save(repo, m)
	writeAtomic(filepath.Join(repo, "gen/a/a.go"), []byte("2"), 0o444)
	writeAtomic(filepath.Join(repo, "gen/a/c.go"), []byte("2"), 0o444)
	os.WriteFile(filepath.Join(repo, "gen/a/.b.go"+tmpMarker+"123"), []byte("partial"), 0o600)
	v3 := mkSrc(t, map[string]string{"a.go": "3"})
	r, err := Apply(repo, []Want{{"gen/a", "//p:a", v3}}, Options{})
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(repo, "gen/a"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != ".gitignore,a.go" {
		t.Fatalf("after recovery: %v (%+v)", names, r)
	}
	b, _ := os.ReadFile(filepath.Join(repo, ManifestRel))
	var mm Manifest
	json.Unmarshal(b, &mm)
	if mm.Roots["gen/a"].Pending != nil {
		t.Fatal("pending not cleared")
	}
}

func TestBadPaths(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a": "1"})
	for _, d := range []string{"../x", "/abs", "a/../../b", ".git/x", "", ".", "a//b", ".bob/gen"} {
		if _, err := Apply(repo, []Want{{d, "//p:a", src}}, Options{}); err == nil {
			t.Errorf("%q accepted", d)
		}
	}
	os.MkdirAll(filepath.Join(repo, ".bob/gen"), 0o755)
	os.WriteFile(filepath.Join(repo, ManifestRel), []byte(`{"version":1,"roots":{"../../etc":{"files":{}}}}`), 0o644)
	if _, err := Apply(repo, nil, Options{Prune: true}); err == nil {
		t.Error("hostile manifest accepted")
	}
}

func TestNestedAndDuplicateRoots(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a": "1"})
	if _, err := Apply(repo, []Want{{"gen", "//p:a", src}, {"gen/b", "//p:b", src}}, Options{}); err == nil {
		t.Error("nested accepted")
	} else {
		t.Log(err)
	}
	if _, err := Apply(repo, []Want{{"gen", "//p:a", src}, {"gen", "//p:b", src}}, Options{}); err == nil {
		t.Error("duplicate accepted")
	} else {
		t.Log(err)
	}
}

func TestManifestLostButContentMatches(t *testing.T) {
	repo := t.TempDir()
	src := mkSrc(t, map[string]string{"a.go": "1"})
	w := []Want{{"gen/a", "//p:a", src}}
	Apply(repo, w, Options{})
	os.Remove(filepath.Join(repo, ManifestRel))
	if _, err := Apply(repo, w, Options{}); err != nil {
		t.Fatalf("re-adopt: %v", err)
	}
}
