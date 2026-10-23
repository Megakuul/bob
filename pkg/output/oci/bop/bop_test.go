package bop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder"
)

// runJob runs one job through builder.Main like Nix does (structured attributes in a file,
// NIX_BUILD_TOP, NIX_STORE) and returns the exit code and the output.
func runJob(t *testing.T, top, store, kind string, args any, out string) (int, string) {
	t.Helper()
	job := map[string]any{"v": 1, "kind": kind, "label": "//t:img", "outputs": []string{"out"}, "args": args,
		"fixup": map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []string{},
			"tools": map[string]string{"strip": "", "objcopy": "", "patchelf": ""}}}
	attrs, err := json.Marshal(map[string]any{"bobSpec": job, "outputs": map[string]string{"out": out}})
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(top, ".attrs.json")
	if err := os.WriteFile(f, attrs, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIX_ATTRS_JSON_FILE", f)
	t.Setenv("NIX_BUILD_TOP", top)
	t.Setenv("NIX_STORE", store)
	kinds := builder.Core()
	for k, v := range Kinds() {
		kinds[k] = v
	}
	var stdout, stderr bytes.Buffer
	code := builder.Main([]string{kind}, kinds, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

const (
	fooPath = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-foo-1.0"
	libPath = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-libx-2.0"
)

// fakeStore creates a store directory with two paths: foo (a program, a hard link, a long name)
// and libx (a library with a symlink and a file to prune).
func fakeStore(t *testing.T) string {
	t.Helper()
	store := filepath.Join(t.TempDir(), "store")
	foo := filepath.Join(store, filepath.Base(fooPath))
	lib := filepath.Join(store, filepath.Base(libPath))
	long := strings.Repeat("d", 60) + "/" + strings.Repeat("f", 60)
	for p, c := range map[string]string{
		foo + "/bin/foo":            "#!/bin/sh\necho foo\n",
		foo + "/share/" + long:      "long",
		lib + "/lib/libx.so.2":      "ELF?",
		lib + "/share/doc/README":   "docs",
		lib + "/share/doc/LICENSE":  "MIT",
		lib + "/share/locale/de/mo": "de",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(foo+"/bin/foo", 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(foo+"/bin/foo", foo+"/bin/foo-link"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libx.so.2", lib+"/lib/libx.so"); err != nil {
		t.Fatal(err)
	}
	return store
}

func readTar(t *testing.T, b []byte) []*tar.Header {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || zr.ModTime.Unix() > 0 {
		t.Errorf("gzip header has name %q mtime %v", zr.Name, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	var out []*tar.Header
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
}

// checkRules checks the R-OUT-010 tar rules.
func checkRules(t *testing.T, hs []*tar.Header) {
	t.Helper()
	var names []string
	seen := map[string]bool{}
	for _, h := range hs {
		names = append(names, h.Name)
		clean := strings.TrimSuffix(h.Name, "/")
		if p := filepath.Dir(clean); p != "." && !seen[p] {
			t.Errorf("%s comes before its parent", h.Name)
		}
		seen[clean] = true
		if h.ModTime.Unix() != 1 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || len(h.PAXRecords) != 0 {
			t.Errorf("%s: header %+v", h.Name, h)
		}
		if h.Typeflag == tar.TypeLink {
			t.Errorf("%s is a hard link", h.Name)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entries not sorted: %v", names)
	}
}

func TestLayerAndImage(t *testing.T) {
	store := fakeStore(t)
	top := t.TempDir()
	layer := func(paths, skip []string) string {
		out := filepath.Join(store, filepath.Base(paths[0])[:8]+"zzzzzzzzzzzzzzzzzzzzzzzz-oci-layer")
		code, log := runJob(t, top, store, "oci-layer", LayerArgs{Format: LayerFormat, Paths: paths, Skip: skip}, out)
		if code != 0 {
			t.Fatalf("oci-layer exit %d: %s", code, log)
		}
		return out
	}
	l1 := layer([]string{fooPath}, []string{})
	l2 := layer([]string{libPath}, []string{libPath + "/share/locale", libPath + "/share/doc/README"})

	var info LayerInfo
	b, err := os.ReadFile(filepath.Join(l2, "layer.json"))
	if err != nil || json.Unmarshal(b, &info) != nil || info.Size <= 0 || !strings.HasPrefix(info.DiffID, "sha256:") {
		t.Fatalf("layer.json %s: %v", b, err)
	}
	// no store hash in plain text: layers discard their references (A146)
	if strings.Contains(string(b), "bbbbbbbb") || len(info.Names) != 1 || info.Names[0] != "libx-2.0" {
		t.Errorf("layer.json holds store hashes or wrong names: %s", b)
	}
	gz, _ := os.ReadFile(filepath.Join(l2, "layer.tar.gz"))
	hs := readTar(t, gz)
	checkRules(t, hs)
	var names []string
	for _, h := range hs {
		names = append(names, h.Name)
	}
	got := strings.Join(names, " ")
	if strings.Contains(got, "locale") || strings.Contains(got, "README") || !strings.Contains(got, "LICENSE") {
		t.Errorf("skip not applied: %s", got)
	}
	if !strings.HasPrefix(got, "nix/ nix/store/ ") {
		t.Errorf("layer does not start with nix/ and nix/store/: %s", got)
	}
	gz1, _ := os.ReadFile(filepath.Join(l1, "layer.tar.gz"))
	for _, h := range readTar(t, gz1) {
		if strings.HasSuffix(h.Name, "bin/foo") && h.Mode&0o111 == 0 {
			t.Errorf("bin/foo lost its mode: %o", h.Mode)
		}
	}
	checkRules(t, readTar(t, gz1))

	image := func(out string) {
		cfg := Config{User: "65534:65534", WorkingDir: "/", Env: []string{"PATH=" + fooPath + "/bin:/usr/bin:/bin"},
			Entrypoint: []string{fooPath + "/bin/foo"}, Cmd: []string{}, ExposedPorts: []string{"8080/tcp"},
			Labels: map[string]string{"org.opencontainers.image.licenses": "MIT"}}
		args := ImageArgs{Manifests: []Manifest{
			{Architecture: "amd64", Layers: []string{l2, l1}, Custom: Custom{CACert: libPath + "/ca.crt", Licences: "[]\n"}, Config: cfg},
			{Architecture: "arm64", Layers: []string{l2, l1}, Custom: Custom{ZoneInfo: libPath + "/zoneinfo", Licences: "[]\n"}, Config: cfg},
		}, Tag: "1.0.0"}
		if code, log := runJob(t, top, store, "oci-image", args, out); code != 0 {
			t.Fatalf("oci-image exit %d: %s", code, log)
		}
	}
	img := filepath.Join(store, "cccccccccccccccccccccccccccccccc-oci-image-t")
	image(img)
	var topIdx struct {
		Manifests []struct {
			MediaType, Digest string
			Annotations       map[string]string
		}
	}
	b, _ = os.ReadFile(filepath.Join(img, "index.json"))
	if err := json.Unmarshal(b, &topIdx); err != nil || len(topIdx.Manifests) != 1 || topIdx.Manifests[0].MediaType != MediaIndex ||
		topIdx.Manifests[0].Annotations["org.opencontainers.image.ref.name"] != "1.0.0" {
		t.Fatalf("index.json %s (%v)", b, err)
	}
	blob := func(d string) []byte {
		b, err := os.ReadFile(filepath.Join(img, "blobs", "sha256", strings.TrimPrefix(d, "sha256:")))
		if err != nil {
			t.Fatalf("blob %s: %v", d, err)
		}
		return b
	}
	var idx struct {
		Manifests []struct {
			Digest   string
			Platform struct{ Architecture string }
		}
	}
	if err := json.Unmarshal(blob(topIdx.Manifests[0].Digest), &idx); err != nil || len(idx.Manifests) != 2 ||
		idx.Manifests[0].Platform.Architecture != "amd64" {
		t.Fatalf("image index: %+v %v", idx, err)
	}
	var man struct {
		Config struct{ Digest string }
		Layers []struct{ Digest string }
	}
	if err := json.Unmarshal(blob(idx.Manifests[0].Digest), &man); err != nil || len(man.Layers) != 3 {
		t.Fatalf("manifest: %+v %v", man, err)
	}
	// the layer blobs are relative symlinks to the layer outputs, the customisation layer a file
	if l, err := os.Readlink(filepath.Join(img, "blobs", "sha256", strings.TrimPrefix(man.Layers[0].Digest, "sha256:"))); err != nil ||
		l != "../../../"+filepath.Base(l2)+"/layer.tar.gz" {
		t.Errorf("layer blob link %q (%v)", l, err)
	}
	custom := readTar(t, blob(man.Layers[2].Digest))
	checkRules(t, custom)
	modes := map[string]int64{}
	links := map[string]string{}
	for _, h := range custom {
		modes[h.Name], links[h.Name] = h.Mode, h.Linkname
	}
	if modes["tmp/"] != 0o1777 || modes["nix/store/"] != 0o555 || links["etc/ssl/certs/ca-certificates.crt"] != libPath+"/ca.crt" {
		t.Errorf("customisation layer: modes %v links %v", modes, links)
	}
	raw := map[string]any{}
	if err := json.Unmarshal(blob(man.Config.Digest), &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["created"]; has {
		t.Errorf("config has created")
	}
	if c := string(blob(man.Config.Digest)); !strings.Contains(c, `"created_by":"bob: libx-2.0"`) ||
		!strings.Contains(c, `"created_by":"bob: foo-1.0"`) || strings.Contains(c, "bbbbbbbb") {
		t.Errorf("history must name the layers' store paths without hashes: %s", c)
	}
	if !strings.Contains(string(blob(man.Config.Digest)), `"created":"1970-01-01T00:00:01Z"`) {
		t.Errorf("history created missing: %s", blob(man.Config.Digest))
	}

	// reproducible: a second run into another output gives the same index
	img2 := filepath.Join(store, "dddddddddddddddddddddddddddddddd-oci-image-t")
	image(img2)
	b2, _ := os.ReadFile(filepath.Join(img2, "index.json"))
	if !bytes.Equal(b, b2) {
		t.Errorf("two runs differ:\n%s\n%s", b, b2)
	}
}

func TestImageError(t *testing.T) {
	store := fakeStore(t)
	code, log := runJob(t, t.TempDir(), store, "oci-image", ImageArgs{Manifests: []Manifest{}, Error: "programs failed"},
		filepath.Join(store, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-x"))
	if code != builder.ExitStep || !strings.Contains(log, "programs failed") {
		t.Errorf("exit %d: %s", code, log)
	}
}
