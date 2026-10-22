package builder

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// R-OUT-010: sorted entries, fixed owner and time, no PAX records even for long names.
func TestWriteTarRules(t *testing.T) {
	long := "nix/store/" + strings.Repeat("x", 120) + "/file"
	es := []TarEntry{TarFile(long, 0o444, []byte("a")), TarDir("nix", 0o555), TarSymlink("b", strings.Repeat("t", 150)),
		TarDir("nix/store", 0o555), TarDir("nix/store/"+strings.Repeat("x", 120), 0o555), TarFile("a.b", 0o644, nil)}
	var buf bytes.Buffer
	if err := WriteTar(&buf, es); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(h.PAXRecords) != 0 || h.ModTime.Unix() != 1 || h.Uid != 0 || h.Uname != "" {
			t.Errorf("%s: %+v", h.Name, h)
		}
		names = append(names, h.Name)
	}
	if got := strings.Join(names, " "); !strings.HasPrefix(got, "a.b b nix/ nix/store/ nix/store/xx") {
		t.Errorf("order %s", got)
	}
	if err := WriteTar(io.Discard, []TarEntry{TarDir("a", 0o755), TarDir("a", 0o755)}); err == nil {
		t.Errorf("duplicate entries accepted")
	}
	a, err := TarGzTo(io.Discard, es)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := TarGzTo(io.Discard, es)
	if a != b || !strings.HasPrefix(a.DiffID, "sha256:") {
		t.Errorf("not reproducible: %+v %+v", a, b)
	}
}
