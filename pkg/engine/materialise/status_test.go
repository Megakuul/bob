package materialise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func status(t *testing.T, repo string, defined map[string]string) map[string]string {
	t.Helper()
	rs, err := Status(context.Background(), repo, defined)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := map[string]string{}
	for _, r := range rs {
		out[r.Label] = r.State
	}
	return out
}

// bob status: each of the five states, decided without writing anything.
func TestStatusStates(t *testing.T) {
	repo := t.TempDir()
	roots := []Root{
		{Dir: "gen/a", Label: "//p:a", Source: "/nix/store/aaaa-a", Inputs: "ia", Tree: tree(t, map[string]string{"a.go": "package a\n"})},
		{Dir: "gen/b", Label: "//p:b", Source: "/nix/store/bbbb-b", Inputs: "ib", Tree: tree(t, map[string]string{"b.go": "package b\n"})},
		{Dir: "gen/c", Label: "//p:c", Source: "/nix/store/cccc-c", Inputs: "ic", Tree: tree(t, map[string]string{"c.go": "package c\n"})},
		{Dir: "nm", Label: "//p:nm", Source: "/nix/store/dddd-nm", Inputs: "in", Keep: []string{".vite"}, Tree: tree(t, map[string]string{"x/i.js": "1\n"})},
	}
	apply(t, Options{Repo: repo}, roots...)
	defined := map[string]string{"//p:a": "ia", "//p:b": "ib", "//p:c": "ic", "//p:nm": "in"}
	got := status(t, repo, defined)
	for l, s := range got {
		if s != StateFresh {
			t.Errorf("after Apply: %s is %s, want fresh", l, s)
		}
	}

	write(t, filepath.Join(repo, "nm/.vite/cache"), "tool cache\n") // a Keep name is not bob's business
	write(t, filepath.Join(repo, "gen/a/a.go"), "package a // edited\n")
	if err := os.Remove(filepath.Join(repo, "gen/b/b.go")); err != nil {
		t.Fatal(err)
	}
	defined["//p:c"] = "ic2"
	delete(defined, "//p:nm")
	defined["//p:new"] = "x"
	before := treeHashOf(t, repo)
	got = status(t, repo, defined)
	want := map[string]string{"//p:a": StateEdited, "//p:b": StateMissing, "//p:c": StateStale, "//p:nm": StateOrphan, "//p:new": StateMissing}
	for l, s := range want {
		if got[l] != s {
			t.Errorf("%s is %q, want %q", l, got[l], s)
		}
	}
	if treeHashOf(t, repo) != before {
		t.Errorf("Status changed the working tree")
	}

	defined["//p:nm"] = "in"
	write(t, filepath.Join(repo, "nm/x/foreign.js"), "mine\n")
	if s := status(t, repo, defined)["//p:nm"]; s != StateEdited {
		t.Errorf("a foreign file: %s, want edited", s)
	}
}

// A root whose write was interrupted (pending entries) is stale until bob gen finishes it.
func TestStatusInterruptedWrite(t *testing.T) {
	repo := t.TempDir()
	r := Root{Dir: "gen/a", Label: "//p:a", Source: "/nix/store/aaaa-a", Inputs: "ia", Tree: tree(t, map[string]string{"a.go": "package a\n"})}
	apply(t, Options{Repo: repo}, r)
	m, err := readManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	mr := m.Roots["gen/a"]
	mr.Pending = map[string]ManifestFile{"b.go": mr.Files["a.go"]}
	m.Roots["gen/a"] = mr
	if err := writeManifest(repo, m); err != nil {
		t.Fatal(err)
	}
	if s := status(t, repo, map[string]string{"//p:a": "ia"})["//p:a"]; s != StateStale {
		t.Errorf("interrupted write: %s, want stale", s)
	}
}

// A node_modules symlink into the same store object through another bob home is rewritten,
// not a conflict, also when the manifest is gone (R-FW-066 (e)).
func TestSameStoreObjectOtherHome(t *testing.T) {
	repo := t.TempDir()
	out := t.TempDir()
	if err := os.Symlink("/nix/store/eeee-npm-x/node_modules/x", filepath.Join(out, "x")); err != nil {
		t.Fatal(err)
	}
	r := Root{Dir: "web/node_modules", Label: "//web:app", Source: "/nix/store/ffff-nm", Tree: out, LinkStore: "/home/one/.local/share/bob/nix/store"}
	apply(t, Options{Repo: repo}, r)
	if err := os.Remove(filepath.Join(repo, ".bob", "gen", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	r.LinkStore = "/home/two/.local/share/bob/nix/store"
	res := apply(t, Options{Repo: repo}, r)
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflicts: %v", conflictPaths(res))
	}
	got, err := os.Readlink(filepath.Join(repo, "web/node_modules/x"))
	if err != nil || !strings.HasPrefix(got, "/home/two/") {
		t.Errorf("link = %q, %v; want it rewritten below the new home", got, err)
	}
	if err := os.Symlink("/elsewhere/x", filepath.Join(repo, "web/node_modules/y")); err != nil {
		t.Fatal(err)
	}
	if res := apply(t, Options{Repo: repo}, r); len(res.Conflicts) != 1 {
		t.Errorf("a foreign symlink is not a conflict: %v", conflictPaths(res))
	}
}

func treeHashOf(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + fi.ModTime().String() + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
