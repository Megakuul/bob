package outputs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// manifest is release.json (bob.release/v1, R-OUT-023).
type manifest struct {
	Schema    string     `json:"schema"`
	Project   string     `json:"project"`
	Release   string     `json:"release"`
	Label     string     `json:"label"`
	Version   string     `json:"version"`
	Public    bool       `json:"public"`
	Artifacts []artifact `json:"artifacts"`
}

type artifact struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Platform    string            `json:"platform"`
	StorePath   string            `json:"storePath"`
	Entrypoints map[string]string `json:"entrypoints"`
	File        string            `json:"file"`
	SHA256      string            `json:"sha256"`
	Size        int64             `json:"size"`
	Repo        string            `json:"repo"`
	Tags        []string          `json:"tags"`
	Index       string            `json:"index"`
}

// readManifest reads <release output>/release.json.
func readManifest(t testing.TB, out string) manifest {
	t.Helper()
	return decode[manifest](t, readFile(t, filepath.Join(out, "release.json")))
}

// hostPath maps a /nix/store/<base> path of a manifest to the file in the world's store.
func (w *world) hostPath(store string) string {
	return filepath.Join(w.home, "nix", "store", filepath.Base(store))
}

// releaseVersioned builds //deploy:api with the version and returns the manifest and its path.
func (w *world) release(version string) (manifest, string, bool) {
	w.t.Helper()
	args := []string{"//deploy:api"}
	if version != "" {
		args = append([]string{"--version", version}, args...)
	}
	out, ok := w.buildOne(args[len(args)-1], args[:len(args)-1]...)
	if !ok {
		return manifest{}, "", false
	}
	return readManifest(w.t, out), out, true
}

// R-OUT-022: the version enters only the manifest, OCI tags, the image label and file names.
// Releases 1.0.0 and 1.0.1 of unchanged sources share closure store paths, AppImage bytes and OCI
// layer blobs; without a version bob says so and uses 0.0.0-dev.
func TestROUT022VersionEntersOnlyTheManifest(t *testing.T) {
	parity.Covers(t, "R-OUT-022")
	w := newWorld(t, "polyglot")
	m1, _, ok := w.release("1.0.0")
	if !ok {
		return
	}
	m2, _, ok := w.release("1.0.1")
	if !ok {
		return
	}
	if len(m1.Artifacts) != len(m2.Artifacts) {
		t.Fatalf("1.0.0 has %d artifacts, 1.0.1 has %d", len(m1.Artifacts), len(m2.Artifacts))
	}
	for i := range m1.Artifacts {
		a, b := m1.Artifacts[i], m2.Artifacts[i]
		if a.Name != b.Name || a.Kind != b.Kind || a.Platform != b.Platform {
			t.Fatalf("artifact %d differs in identity: %+v vs %+v", i, a, b)
		}
		switch a.Kind {
		case "closure":
			if a.StorePath != b.StorePath {
				t.Errorf("closure %s %s: store paths %s and %s differ between versions", a.Name, a.Platform, a.StorePath, b.StorePath)
			}
		case "appimage", "tarball":
			if a.SHA256 != b.SHA256 || a.Size != b.Size {
				t.Errorf("%s %s %s: bytes differ between versions (%s vs %s)", a.Kind, a.Name, a.Platform, a.SHA256, b.SHA256)
			}
			if a.File == b.File || !strings.Contains(a.File, "1.0.0") || !strings.Contains(b.File, "1.0.1") {
				t.Errorf("%s files %q and %q must carry their version", a.Kind, a.File, b.File)
			}
		case "oci":
			if a.Index == b.Index {
				t.Errorf("oci index digest is the same for both versions; tags and label differ, so the index must too")
			}
			if fmt.Sprint(a.Tags) != "[1.0.0]" || fmt.Sprint(b.Tags) != "[1.0.1]" {
				t.Errorf("oci tags %v and %v, want [1.0.0] and [1.0.1]", a.Tags, b.Tags)
			}
			l1, l2 := openLayout(t, w.hostPath(a.StorePath)), openLayout(t, w.hostPath(b.StorePath))
			for _, arch := range []string{"amd64", "arm64"} {
				ma, da, ca := l1.manifestFor(arch)
				mb, db, cb := l2.manifestFor(arch)
				if fmt.Sprint(descriptorDigests(ma.Layers)) != fmt.Sprint(descriptorDigests(mb.Layers)) {
					t.Errorf("%s: layer blobs differ between versions", arch)
				}
				if da.Digest == db.Digest || ma.Config.Digest == mb.Config.Digest {
					t.Errorf("%s: image manifest or config is identical across versions; the version label must differ", arch)
				}
				if ca.Config.Labels["org.opencontainers.image.version"] != "1.0.0" || cb.Config.Labels["org.opencontainers.image.version"] != "1.0.1" {
					t.Errorf("%s: label org.opencontainers.image.version = %q / %q", arch, ca.Config.Labels["org.opencontainers.image.version"], cb.Config.Labels["org.opencontainers.image.version"])
				}
			}
		}
	}

	r := w.bob("build", "//deploy:api")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	if !strings.Contains(r.Stderr+r.Stdout, "note: no version given; using 0.0.0-dev") {
		t.Errorf("bob build //deploy:api without --version did not print the note; output:\n%s%s", r.Stdout, r.Stderr)
	}
	m0, _, ok := w.release("")
	if ok && m0.Version != "0.0.0-dev" {
		t.Errorf("version without --version is %q, want 0.0.0-dev", m0.Version)
	}
}

func descriptorDigests(ds []descriptor) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Digest)
	}
	return out
}

var versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

// R-OUT-023: release.json of the fixture matches the golden and the JSON schema; keys sorted,
// arrays sorted by name then platform; per-kind fields; file hashes match the files; invalid
// versions are evaluation errors.
func TestROUT023ReleaseManifest(t *testing.T) {
	parity.Covers(t, "R-OUT-023")
	w := newWorld(t, "polyglot")
	m, out, ok := w.release("1.0.1")
	if !ok {
		return
	}
	raw := readFile(t, filepath.Join(out, "release.json"))
	golden.Assert(t, "tests/goldens/outputs/release.json", raw, golden.Reqs("R-OUT-023"))

	schema := decode[map[string]any](t, readFile(t, filepath.Join(harness.Root(t), "pkg", "release", "schema", "release-v1.json")))
	var doc any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("release.json is not JSON: %v", err)
	}
	for _, e := range validateSchema(schema, doc, "$") {
		t.Errorf("release.json violates the schema: %s", e)
	}
	if bad := unsortedKeys(t, raw); bad != "" {
		t.Errorf("release.json keys are not sorted: %s", bad)
	}
	if m.Schema != "bob.release/v1" || m.Release != "api" || m.Label != "//deploy:api" || m.Version != "1.0.1" || m.Public {
		t.Errorf("header fields: %+v", m)
	}
	if !sort.SliceIsSorted(m.Artifacts, func(i, j int) bool {
		a, b := m.Artifacts[i], m.Artifacts[j]
		return a.Name < b.Name || a.Name == b.Name && a.Platform < b.Platform
	}) {
		t.Errorf("artifacts are not sorted by name, then platform")
	}
	seen := map[string]int{}
	for _, a := range m.Artifacts {
		seen[a.Kind]++
		switch a.Kind {
		case "closure":
			if len(a.Entrypoints) == 0 {
				t.Errorf("closure %s %s has no entrypoints", a.Name, a.Platform)
			}
			for n, rel := range a.Entrypoints {
				if st, err := os.Stat(filepath.Join(w.hostPath(a.StorePath), rel)); err != nil || st.Mode()&0o111 == 0 {
					t.Errorf("closure %s entrypoint %s → %s is not an executable of the output: %v", a.Name, n, rel, err)
				}
			}
			if a.Platform != "linux-amd64" && a.Platform != "linux-arm64" {
				t.Errorf("closure platform %q", a.Platform)
			}
		case "appimage", "tarball":
			f := filepath.Join(w.hostPath(a.StorePath), a.File)
			if a.File == "" || a.Platform == "multi" {
				t.Errorf("%s %s: file %q platform %q", a.Kind, a.Name, a.File, a.Platform)
				continue
			}
			if _, err := os.Stat(f); err != nil {
				// the package output has the unversioned file name; the manifest names the versioned one
				f = onlyFile(t, w.hostPath(a.StorePath), map[string]string{"appimage": "*.AppImage", "tarball": "*.tar.gz"}[a.Kind])
			}
			if got := sha256File(t, f); got != a.SHA256 {
				t.Errorf("%s %s: sha256 %s, manifest %s", a.Kind, a.Name, got, a.SHA256)
			}
			if got := fileSize(t, f); got != a.Size {
				t.Errorf("%s %s: size %d, manifest %d", a.Kind, a.Name, got, a.Size)
			}
		case "oci":
			if a.Platform != "multi" || a.Repo == "" || fmt.Sprint(a.Tags) != "[1.0.1]" || a.Index == "" {
				t.Errorf("oci artifact %+v", a)
			}
			if _, d := openLayout(t, w.hostPath(a.StorePath)).imageIndex(); d.Digest != a.Index {
				t.Errorf("oci index %s, layout has %s", a.Index, d.Digest)
			}
		default:
			t.Errorf("unknown artifact kind %q", a.Kind)
		}
	}
	for _, k := range []string{"closure", "appimage", "tarball", "oci"} {
		if seen[k] == 0 {
			t.Errorf("manifest has no %s artifact", k)
		}
	}

	// Versions: valid ones build, invalid ones are evaluation errors (exit 3).
	for _, v := range []string{"2.0.0-rc.1+build_5", "0", strings.Repeat("a", 64)} {
		if !versionRE.MatchString(v) {
			t.Fatalf("harness: %q should be valid", v)
		}
		if r := w.bob("build", "--version", v, "//deploy:api"); r.Exit != 0 {
			t.Errorf("version %q was refused: exit %d\n%s", v, r.Exit, r.Stderr)
		}
	}
	for _, v := range []string{"", ".1", "-1", "1 0", "1/0", strings.Repeat("a", 65), "v1\n"} {
		if versionRE.MatchString(v) {
			t.Fatalf("harness: %q should be invalid", v)
		}
		r := w.bob("build", "--version="+v, "//deploy:api")
		if r.Exit != 3 || !strings.Contains(r.Stderr, fmt.Sprintf("✗ invalid release version %q", v)) {
			t.Errorf("version %q: exit %d, want 3 and `✗ invalid release version %q`\n%s", v, r.Exit, v, r.Stderr)
		}
	}
}

// unsortedKeys walks the tokens of raw in document order and returns a description of the first
// object whose keys are not in ascending order ("" when every object is sorted). SPEC §4.8 asks
// for sorted keys, not for a particular indentation.
func unsortedKeys(t testing.TB, raw []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		obj     bool
		last    string
		hasLast bool
		wantKey bool
	}
	var stack []frame
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return ""
			}
			t.Fatalf("release.json is not JSON: %v", err)
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, frame{obj: true, wantKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].wantKey = true
				}
			}
			continue
		}
		n := len(stack)
		if n == 0 || !stack[n-1].obj {
			continue
		}
		f := &stack[n-1]
		if f.wantKey {
			k, _ := tok.(string)
			if f.hasLast && k <= f.last {
				return fmt.Sprintf("%q after %q", k, f.last)
			}
			f.last, f.hasLast, f.wantKey = k, true, false
		} else {
			f.wantKey = true
		}
	}
}

// validateSchema checks v (decoded with UseNumber) against the JSON Schema subset of
// pkg/release/schema/release-v1.json: type, const, enum, pattern, minLength, minimum, required,
// properties, additionalProperties (false or schema), items.
func validateSchema(schema map[string]any, v any, path string) []string {
	var errs []string
	if c, ok := schema["const"]; ok && v != c {
		errs = append(errs, fmt.Sprintf("%s: %v is not %v", path, v, c))
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v not in %v", path, v, enum))
		}
	}
	switch schema["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return append(errs, path+": not an object")
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, ok := m[r.(string)]; !ok {
					errs = append(errs, path+": missing "+r.(string))
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, sub := range m {
			if ps, ok := props[k].(map[string]any); ok {
				errs = append(errs, validateSchema(ps, sub, path+"."+k)...)
				continue
			}
			switch ap := schema["additionalProperties"].(type) {
			case bool:
				if !ap {
					errs = append(errs, path+": unexpected property "+k)
				}
			case map[string]any:
				errs = append(errs, validateSchema(ap, sub, path+"."+k)...)
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return append(errs, path+": not an array")
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range a {
				errs = append(errs, validateSchema(items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return append(errs, path+": not a string")
		}
		if p, ok := schema["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(s) {
			errs = append(errs, fmt.Sprintf("%s: %q does not match %s", path, s, p))
		}
		if n, ok := schema["minLength"].(float64); ok && float64(len(s)) < n {
			errs = append(errs, path+": too short")
		}
	case "integer":
		n, ok := v.(json.Number)
		if !ok || strings.ContainsAny(n.String(), ".eE") {
			return append(errs, path+": not an integer")
		}
		if min, ok := schema["minimum"].(float64); ok {
			if f, _ := n.Float64(); f < min {
				errs = append(errs, path+": below minimum")
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			errs = append(errs, path+": not a boolean")
		}
	}
	return errs
}
