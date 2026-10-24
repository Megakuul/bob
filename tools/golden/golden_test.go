package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want string
	}{
		{"equal", []string{"x", "y"}, []string{"x", "y"}, " x  y"},
		{"change", []string{"x", "y"}, []string{"x", "z"}, " x -y +z"},
		{"insert", []string{"x"}, []string{"x", "y"}, " x +y"},
		{"new file", nil, []string{"y"}, "+y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parts []string
			for _, o := range diffLines(tt.a, tt.b) {
				parts = append(parts, string(o.Kind)+o.Text)
			}
			if got := strings.Join(parts, " "); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalSortsJSON(t *testing.T) {
	got := string(canonical("x.json", []byte(`{"b":1,"a":{"d":2.50,"c":true}}`)))
	want := "{\n  \"a\": {\n    \"c\": true,\n    \"d\": 2.50\n  },\n  \"b\": 1\n}\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pngBytes(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReviewAndApprove(t *testing.T) {
	root := t.TempDir()
	cands := filepath.Join(root, "out", "golden-candidates")
	writeFile(t, filepath.Join(root, "tests/goldens/ir/a.json"), []byte(`{"a":1}`))
	writeFile(t, filepath.Join(cands, "tests/goldens/ir/a.json"), []byte(`{"a":2}`))
	writeFile(t, filepath.Join(cands, ".meta/tests/goldens/ir/a.json.json"), []byte(`{"golden":"tests/goldens/ir/a.json","test":"TestIR/a","reqs":["R-FW-047"]}`))
	writeFile(t, filepath.Join(cands, "tests/goldens/cli/new.txt"), []byte("<b>hi</b>\n"))
	writeFile(t, filepath.Join(root, "tests/goldens/same.txt"), []byte("s\n"))
	writeFile(t, filepath.Join(cands, "tests/goldens/same.txt"), []byte("s\n"))
	writeFile(t, filepath.Join(root, "tests/goldens/ui/x.png"), pngBytes(t, color.White))
	writeFile(t, filepath.Join(cands, "tests/goldens/ui/x.png"), pngBytes(t, color.Black))

	out := filepath.Join(root, "out", "golden-review")
	if err := run(t.Context(), []string{"-root", root, "review"}); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"3 changed golden(s)", "tests/goldens/ir/a.json", "TestIR/a", "R-FW-047",
		`-  &#34;a&#34;: 1`, "&lt;b&gt;hi&lt;/b&gt;", "img/g2-diff.png", "1 differing pixels"} {
		if !strings.Contains(string(html), s) {
			t.Errorf("review page lacks %q", s)
		}
	}
	if strings.Contains(string(html), "same.txt") {
		t.Error("unchanged candidate listed")
	}

	if err := run(t.Context(), []string{"-root", root, "-no-protect", "approve", "tests/goldens/ir/a.json"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tests/goldens/ir/a.json")); string(b) != `{"a":2}` {
		t.Fatalf("approved golden = %q", b)
	}
	if err := run(t.Context(), []string{"-root", root, "-no-protect", "approve", "tests/goldens/nope"}); err == nil {
		t.Fatal("approving an unknown id succeeded")
	}
	if err := run(t.Context(), []string{"-root", root, "-no-protect", "approve", "all"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tests/goldens/cli/new.txt")); err != nil {
		t.Fatalf("approve all: %v", err)
	}
}
