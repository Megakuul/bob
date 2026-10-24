package fetch

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// tarEntry is one member of a test archive: a directory (name ends in "/"), a symlink (link
// set) or a regular file.
type tarEntry struct{ name, link, body string }

func writeTar(t *testing.T, ents []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: 0o644}
		switch {
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.name[len(e.name)-1] == '/':
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "a.tar")
	if err := os.WriteFile(f, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestUnpackSymlinkChainEscape is the regression test of review finding F1 (R-NIX-014): a
// symlink whose target is lexically inside the tree but climbs out through another symlink of
// the same archive is refused, in either entry order, and nothing is extracted.
func TestUnpackSymlinkChainEscape(t *testing.T) {
	for name, ents := range map[string][]tarEntry{
		"chain": {{name: "d/"}, {name: "d/up", link: ".."}, {name: "x", link: "d/up/../outside"}},
		"chain listed first": {{name: "x", link: "d/up/../outside"}, {name: "d/"},
			{name: "d/up", link: ".."}},
		"repeated": {{name: "d/"}, {name: "d/up", link: ".."},
			{name: "x", link: "d/up/d/up/d/up/../../../etc/passwd"}},
		"two links": {{name: "a", link: "b/.."}, {name: "b", link: "c"}, {name: "c/"}, {name: "c/e/"},
			{name: "y", link: "a/../z"}},
		"loop": {{name: "l1", link: "l2"}, {name: "l2", link: "l1"}},
		"via file": {{name: "d/"}, {name: "d/up", link: ".."}, {name: "d/f", body: "x"},
			{name: "y", link: "d/up/.."}},
	} {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			root := filepath.Join(outside, "root")
			err := unpack(context.Background(), writeTar(t, ents), root, 0, "demo", "https://x/a.tar")
			code(t, err, 3004)
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Errorf("refused archive left %s behind (%v)", root, err)
			}
		})
	}
}

// TestUnpackSymlinkChainInside accepts chains that stay inside the root at every step.
func TestUnpackSymlinkChainInside(t *testing.T) {
	ents := []tarEntry{{name: "d/"}, {name: "d/e/"}, {name: "d/e/f", body: "hi"},
		{name: "lib", link: "d/e"}, {name: "y", link: "lib/../e/f"}, {name: "d/back", link: "../lib/f"},
		{name: "dangling", link: "nope/../d"}}
	root := filepath.Join(t.TempDir(), "root")
	if err := unpack(context.Background(), writeTar(t, ents), root, 0, "demo", "https://x/a.tar"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"y", "d/back"} {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || string(b) != "hi" {
			t.Errorf("read %s: %q, %v", p, b, err)
		}
	}
}

func TestLinkInside(t *testing.T) {
	links := map[string]string{"d/up": "..", "d/self": ".", "abs": "/etc", "top": "d/up/.."}
	for rel, want := range map[string]bool{"d/up": true, "d/self": true, "abs": false, "top": false} {
		if got := linkInside(links, rel); got != want {
			t.Errorf("linkInside(%s) = %v, want %v", rel, got, want)
		}
	}
}
