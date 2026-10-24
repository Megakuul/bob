package bop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder"
)

func TestVersioned(t *testing.T) {
	for _, c := range [][4]string{
		{"api-tar-linux-arm64.tar.gz", "api-tar", "1.0.1", "api-tar-1.0.1-linux-arm64.tar.gz"},
		{"api-appimage-x86_64.AppImage", "api-appimage", "1.0.1", "api-appimage-1.0.1-x86_64.AppImage"},
		{"bundle.AppImage", "api", "2", "bundle-2.AppImage"},
	} {
		if got := Versioned(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("Versioned(%q) = %q, want %q", c[0], got, c[3])
		}
	}
}

func TestReleaseManifest(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	cl := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-api"
	tb := "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-tarball-api-tar-linux-arm64"
	img := "/nix/store/cccccccccccccccccccccccccccccccc-oci-image-api-1.0.1"
	for p, c := range map[string]string{
		cl + "/bin/api":                    "x",
		cl + "/bin/apictl":                 "y",
		tb + "/api-tar-linux-arm64.tar.gz": "tarball",
		img + "/index.json":                `{"manifests":[{"digest":"sha256:` + strings.Repeat("ab", 32) + `"}]}`,
	} {
		f := filepath.Join(store, strings.TrimPrefix(p, "/nix/store/"))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(c), 0o555); err != nil {
			t.Fatal(err)
		}
	}
	args := Args{Release: "api", Label: "//deploy:api", Version: "1.0.1", Artifacts: []Artifact{
		{Name: "api-tar", Kind: "tarball", Platform: "linux-arm64", StorePath: tb, File: "*.tar.gz"},
		{Name: "api", Kind: "closure", Platform: "linux-arm64", StorePath: cl},
		{Name: "api", Kind: "closure", Platform: "linux-amd64", StorePath: cl, Entrypoints: map[string]string{"api": "bin/api"}},
		{Name: "api-image", Kind: "oci", Platform: "multi", StorePath: img, Repo: "api", Tags: []string{"1.0.1"}},
	}}
	top := t.TempDir()
	out := filepath.Join(store, "dddddddddddddddddddddddddddddddd-release-api-1.0.1")
	job := map[string]any{"v": 1, "kind": "release", "label": "//deploy:api", "outputs": []string{"out"}, "args": args}
	attrs, _ := json.Marshal(map[string]any{"bobSpec": job, "outputs": map[string]string{"out": out}})
	f := filepath.Join(top, ".attrs.json")
	if err := os.WriteFile(f, attrs, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIX_ATTRS_JSON_FILE", f)
	t.Setenv("NIX_BUILD_TOP", top)
	t.Setenv("NIX_STORE", store)
	var stderr bytes.Buffer
	if code := builder.Main([]string{"release"}, Kinds(), &stderr, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(out, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Schema    string
		Artifacts []map[string]any
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range m.Artifacts {
		order = append(order, a["name"].(string)+"/"+a["platform"].(string))
	}
	if strings.Join(order, " ") != "api/linux-amd64 api/linux-arm64 api-image/multi api-tar/linux-arm64" {
		t.Errorf("order %v", order)
	}
	if eps := m.Artifacts[1]["entrypoints"].(map[string]any); len(eps) != 2 || eps["apictl"] != "bin/apictl" {
		t.Errorf("default entrypoints %v", eps)
	}
	if m.Artifacts[3]["file"] != "api-tar-1.0.1-linux-arm64.tar.gz" || m.Artifacts[3]["size"].(float64) != 7 ||
		m.Artifacts[2]["index"] != "sha256:"+strings.Repeat("ab", 32) || m.Schema != "bob.release/v1" {
		t.Errorf("manifest %s", b)
	}
}
