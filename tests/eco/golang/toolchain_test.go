package golang

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

var (
	goToolchainRe = regexp.MustCompile(`/nix/store/[a-z0-9]{32}-go-(1\.\d+)\.\d+`)
	stdNameRe     = regexp.MustCompile(`-go-std-(1\.\d+)\.\d+`)
)

// R-GO-004a (a)+(b): with example.com/lib at Go 1.25 and example.com/api and tools at Go
// 1.26, every package is compiled with its module's toolchain (the job's toolchain store path
// names that Go), and std is built once per toolchain variant.
func TestRGO004aToolchainPerModule(t *testing.T) {
	parity.Covers(t, "R-GO-004a")
	home := t.TempDir()
	dir := harness.Fixture(t, "go")
	replaceIn(t, filepath.Join(dir, "lib", "bob.go"), `Go: "1.26"`, `Go: "1.25"`)
	if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
		return
	}
	if r := bobIn(t, dir, home, "build", "//..."); !ok(t, r) {
		return
	}
	all, parsed := drvStore(t, home)
	if !parsed {
		return
	}
	// The compile job of a package names its import path after -p; classify by it.
	want := map[string]string{
		"example.com/lib/strutil":      "1.25",
		"example.com/lib/fastadd":      "1.25",
		"example.com/api/internal/msg": "1.26",
		"example.com/api/cmd/api":      "1.26",
		"example.com/tools/cmd/stamp":  "1.26",
	}
	seen := map[string]bool{}
	for _, d := range bobDrvs(all) {
		spec := jobSpec(d)
		for pkg, goVer := range want {
			if !regexp.MustCompile(`-p["',\s:]+` + regexp.QuoteMeta(pkg) + `["',\s\]}]`).MatchString(spec) {
				continue
			}
			seen[pkg] = true
			m := goToolchainRe.FindAllStringSubmatch(spec, -1)
			if len(m) == 0 {
				t.Errorf("%s: the job of %s names no Go toolchain store path", pkg, d.Name)
			}
			for _, x := range m {
				if x[1] != goVer {
					t.Errorf("%s is compiled with Go %s, its module declares Go %s (job %s)", pkg, x[1], goVer, d.Name)
				}
			}
		}
	}
	for pkg := range want {
		if !seen[pkg] {
			t.Errorf("no compile job found for %s", pkg)
		}
	}
	// std: one derivation per (Go version, cgo) variant, and one variant set per toolchain.
	names := map[string]int{}
	versions := map[string]bool{}
	for n := range all {
		if m := stdNameRe.FindStringSubmatch(n); m != nil {
			// Count by name without the hash prefix: two std derivations with one name (same
			// Go version, platform, cgo variant) but different hashes are a duplicate build.
			_, base, _ := strings.Cut(n, "-")
			names[base]++
			versions[m[1]] = true
		}
	}
	if !versions["1.25"] || !versions["1.26"] {
		t.Errorf("std must be built for Go 1.25 and 1.26, found versions %v", versions)
	}
	for n, c := range names {
		if c != 1 {
			t.Errorf("std derivation %s exists %d times", n, c)
		}
	}
}

// goProxyModule writes one module version into a file:// GOPROXY directory (layout of
// §03.2.5: <escaped path>/@v/{list,<v>.info,<v>.mod,<v>.zip}).
func goProxyModule(t *testing.T, root, path, version, goMod, src string) {
	t.Helper()
	v := filepath.Join(root, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(v, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, body := range map[string]string{"go.mod": goMod, "bar.go": src} {
		w, err := zw.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	for name, body := range map[string][]byte{
		"list":            []byte(version + "\n"),
		version + ".info": []byte(`{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`),
		version + ".mod":  []byte(goMod),
		version + ".zip":  zb.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(v, name), body, 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
}

// fixtureRequires lists the (module path, version) pairs of every golang.Require call in the
// bob.go files below dir.
func fixtureRequires(t *testing.T, dir string) [][2]string {
	t.Helper()
	re := regexp.MustCompile(`golang\.Require\("([^"]+)",\s*"([^"]+)"`)
	var out [][2]string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "bob.go" {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out = append(out, [2]string{m[1], m[2]})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return out
}

// R-GO-004a (c): a module requirement whose go directive exceeds the module's Go fails at
// bob lock with the toolchain error "✗ github.com/foo/bar@v1.2.0 needs go 1.27.0, but
// example.com/api uses go 1.26 (set Go in api/bob.go)" (BOB-E-1008, definition class, exit 3).
func TestRGO004aRequirementNeedsNewerGo(t *testing.T) {
	parity.Covers(t, "R-GO-004a")
	proxy := t.TempDir()
	goProxyModule(t, proxy, "github.com/foo/bar", "v1.2.0", "module github.com/foo/bar\n\ngo 1.27.0\n", "package bar\n")
	dir := harness.Fixture(t, "go")
	// The fixture's other requirements must resolve too, so that a correct bob lock reaches
	// the toolchain check instead of failing first with BOB-E-1015 on a module the isolated
	// proxy does not serve. They are served as stubs with an old go directive: every
	// Require of every bob.go of the fixture gets one (the stub's content is irrelevant).
	for _, m := range fixtureRequires(t, dir) {
		if m[0] == "github.com/foo/bar" {
			continue
		}
		goProxyModule(t, proxy, m[0], m[1], "module "+m[0]+"\n\ngo 1.20\n", "package stub\n")
	}
	replaceIn(t, filepath.Join(dir, "api", "bob.go"),
		`golang.Require("github.com/google/uuid", "v1.6.0")}`,
		`golang.Require("github.com/google/uuid", "v1.6.0"), golang.Require("github.com/foo/bar", "v1.2.0")}`)
	env := []string{"BOB_HOME=" + t.TempDir(), "BOB_GOPROXY=file://" + proxy}
	r := bobEnv(t, dir, env, "lock", "--json")
	if harness.Red(t, r) || !exitIs(t, r, 3) {
		return
	}
	e, found := errorWithCode(t, r, "BOB-E-1008")
	if !found {
		return
	}
	const want = "github.com/foo/bar@v1.2.0 needs go 1.27.0, but example.com/api uses go 1.26 (set Go in api/bob.go)"
	if !strings.Contains(e.Message+" "+e.Hint, want) {
		t.Errorf("toolchain error\n  message: %q\n  hint:    %q\nwant it to read %q", e.Message, e.Hint, want)
	}
}
