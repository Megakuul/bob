package eval

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed" // the framework module zip (R-FW-043.4)
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// The framework module (R-FW-043.4): the evaluator requires github.com/megakuul/bob at bob's
// own version. bob embeds the framework's L0–L3 packages as a module zip and extracts them into
// the evaluator's private module cache with the matching go.sum lines, so the evaluator never
// needs a network, a proxy or the bob source tree.

const frameworkModule = "github.com/megakuul/bob"

// devVersion is the framework version of a bob that is not a released module version (a
// source build): stable, so IR goldens do not change with every commit.
const devVersion = "v0.0.0-dev"

// frameworkDirs are the L0–L3 packages of SPEC R-ARCH-003, the only framework code a bob.go may
// import (R7). Their gen/ (E2) and bop/ (builder) subpackages are engine code and are left out.
var frameworkDirs = []string{
	"pkg/bob", "pkg/nixpkgs",
	"pkg/cc", "pkg/golang", "pkg/rust", "pkg/npm", "pkg/ocaml", "pkg/proto", "pkg/connect",
	"pkg/graphql", "pkg/openapi", "pkg/services",
	"pkg/output/oci", "pkg/output/appimage", "pkg/output/tarball", "pkg/release",
}

// frameworkZip is written by go generate (TestFrameworkZip -update): the framework module of
// the source tree this bob is built from. A test fails when it is stale.
//
//go:generate go test -run TestFrameworkZip -update-framework
//go:embed framework.zip
var frameworkZip []byte

var semverRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`) })

// BobVersion is this bob's version as a Go module version: the module version it was installed
// at, or v0.0.0-dev for a source build. The evaluator's IR must carry the same (R-FW-044).
func BobVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path == frameworkModule && semverRe().MatchString(bi.Main.Version) {
		return bi.Main.Version
	}
	return devVersion
}

// framework is the framework module the evaluator compiles against.
type framework struct {
	version string
	goDir   string            // go directive of its go.mod
	files   map[string][]byte // module-relative path → content, incl. go.mod
	h1      string            // go.sum hash of the module
	modH1   string            // go.sum hash of its go.mod
}

// loadFramework returns the framework module: the live source tree when this bob was built from
// one that is still on disk (source builds, tests), the embedded zip otherwise.
func loadFramework() (*framework, error) {
	files, err := frameworkSource()
	if err != nil {
		if files, err = unzipFramework(frameworkZip); err != nil {
			return nil, fmt.Errorf("internal error: the embedded bob framework is unreadable; this is a bob bug: %w", err)
		}
	}
	return newFramework(BobVersion(), files)
}

func newFramework(version string, files map[string][]byte) (*framework, error) {
	gomod, ok := files["go.mod"]
	if !ok {
		return nil, errors.New("internal error: the bob framework has no go.mod; this is a bob bug")
	}
	fw := &framework{version: version, goDir: goVersion(gomod), files: files}
	prefix := frameworkModule + "@" + version + "/"
	names := slices.Sorted(maps.Keys(files))
	var sum strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sum, "%x  %s\n", sha256.Sum256(files[n]), prefix+n)
	}
	fw.h1 = hash1(sum.String())
	fw.modH1 = hash1(fmt.Sprintf("%x  go.mod\n", sha256.Sum256(gomod)))
	return fw, nil
}

// hash1 is the "h1:" hash of go.sum over a dirhash summary (golang.org/x/mod/sumdb/dirhash).
func hash1(summary string) string {
	s := sha256.Sum256([]byte(summary))
	return "h1:" + base64.StdEncoding.EncodeToString(s[:])
}

// FrameworkSum returns the go.sum lines of the framework module at BobVersion(), as the
// evaluator compiles against it: what a root go.sum needs for the framework requirement of the
// generated root go.mod (§03.2.5).
func FrameworkSum() (string, error) {
	fw, err := loadFramework()
	if err != nil {
		return "", err
	}
	return fw.sumLines(), nil
}

// sumLines are the go.sum lines of the framework module.
func (fw *framework) sumLines() string {
	return fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", frameworkModule, fw.version, fw.h1, frameworkModule, fw.version, fw.modH1)
}

// frameworkSource reads the framework module from the source tree of this binary (found
// through the file name the compiler recorded; empty in -trimpath builds).
func frameworkSource() (map[string][]byte, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		return nil, errors.New("no source tree")
	}
	for d := filepath.Dir(file); d != filepath.Dir(d); d = filepath.Dir(d) {
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			if modulePath(b) != frameworkModule {
				break
			}
			return frameworkFiles(os.DirFS(d))
		}
	}
	return nil, errors.New("no source tree")
}

// frameworkFiles collects the framework module from the bob module tree in src: the non-test
// Go files of the L0–L3 packages and a go.mod with the module's go directive and no
// requirements (L0–L3 import only the standard library, R-ARCH-003).
func frameworkFiles(src fs.FS) (map[string][]byte, error) {
	gomod, err := fs.ReadFile(src, "go.mod")
	if err != nil {
		return nil, err
	}
	// Only the language version ("1.26"): the framework needs the language, not a patch
	// release, so the pinned nixpkgs go (whose patch level may lag bob's own) can compile it.
	files := map[string][]byte{"go.mod": []byte("module " + frameworkModule + "\n\ngo " + langVersion(goVersion(gomod)) + "\n")}
	for _, top := range frameworkDirs {
		err := fs.WalkDir(src, top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == top {
					return fs.SkipDir
				}
				return err
			}
			if d.IsDir() {
				// gen, bop and resolve are engine code (generators, builder ops, lock-time resolvers):
				// the evaluator never compiles them and bob.go cannot import them (R7).
				if p != top && (d.Name() == "gen" || d.Name() == "bop" || d.Name() == "resolve" || d.Name() == "testdata" ||
					strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := fs.ReadFile(src, p)
			if err != nil {
				return err
			}
			files[p] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// zipFramework writes files as a deterministic zip (sorted, fixed time).
func zipFramework(files map[string][]byte) ([]byte, error) {
	names := slices.Sorted(maps.Keys(files))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// maxFrameworkFile bounds one embedded file when reading the zip back.
const maxFrameworkFile = 4 << 20

func unzipFramework(b []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if !fs.ValidPath(f.Name) || f.FileInfo().IsDir() {
			return nil, fmt.Errorf("bad entry %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		c, err := io.ReadAll(io.LimitReader(rc, maxFrameworkFile+1))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		if len(c) > maxFrameworkFile {
			return nil, fmt.Errorf("entry %q too large", f.Name)
		}
		files[f.Name] = c
	}
	return files, nil
}

// install makes modcache (the evaluator's GOMODCACHE) hold the framework module in
// the layout the go command reads offline: the extracted module plus cache/download's .mod and
// .ziphash files. Nothing is rewritten when the stamp shows the same module.
func (fw *framework) install(modcache string) error {
	stamp := filepath.Join(modcache, ".bob-framework")
	want := fw.version + " " + fw.h1 + "\n"
	if b, err := os.ReadFile(stamp); err == nil && string(b) == want {
		return nil
	}
	if err := makeWritable(modcache); err != nil {
		return err
	}
	if err := os.RemoveAll(modcache); err != nil {
		return err
	}
	dir := filepath.Join(modcache, frameworkModule+"@"+fw.version)
	dl := filepath.Join(modcache, "cache", "download", frameworkModule, "@v")
	for name, c := range fw.files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, c, 0o444); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dl, fw.version+".mod"), fw.files["go.mod"], 0o444); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dl, fw.version+".ziphash"), []byte(fw.h1), 0o444); err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte(want), 0o644)
}

// makeWritable gives the owner write access to every directory below dir, so a stale module
// cache (read-only files in read-only directories, like the go command's) can be removed.
func makeWritable(dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0o755)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// packages lists the framework's package import paths (R7 and the seed cache build).
func (fw *framework) packages() []string {
	seen := map[string]bool{}
	for n := range fw.files {
		if strings.HasSuffix(n, ".go") {
			seen[frameworkModule+"/"+path.Dir(n)] = true
		}
	}
	out := slices.Sorted(maps.Keys(seen))
	return out
}

// langVersion is the language version of a go directive: "1.26.9" → "1.26".
func langVersion(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// InstallFrameworkProxy makes the GOPROXY directory dir (file:// layout) serve the framework
// module at BobVersion(): <dir>/github.com/megakuul/bob/@v/{list,<v>.info,<v>.mod,<v>.zip}.
// The go command and gopls of a dev shell (§03.2.5 point 3) read modules only from there, and
// every bob.go and the generated root go.mod require the framework (R-FW-001, R-FW-043.4).
// Files that already hold the same bytes are left alone.
func InstallFrameworkProxy(dir string) error {
	fw, err := loadFramework()
	if err != nil {
		return err
	}
	prefix := frameworkModule + "@" + fw.version + "/"
	files := make(map[string][]byte, len(fw.files))
	for n, c := range fw.files {
		files[prefix+n] = c
	}
	zb, err := zipFramework(files)
	if err != nil {
		return err
	}
	vdir := filepath.Join(dir, filepath.FromSlash(frameworkModule), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	list := filepath.Join(vdir, "list")
	old, _ := os.ReadFile(list)
	versions := strings.Fields(string(old))
	if !slices.Contains(versions, fw.version) {
		versions = append(versions, fw.version)
	}
	slices.Sort(versions)
	for name, c := range map[string][]byte{
		fw.version + ".info": []byte(`{"Version":"` + fw.version + `","Time":"1980-01-01T00:00:00Z"}` + "\n"),
		fw.version + ".mod":  fw.files["go.mod"],
		fw.version + ".zip":  zb,
		"list":               []byte(strings.Join(versions, "\n") + "\n"),
	} {
		p := filepath.Join(vdir, name)
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, c) {
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, c, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	return nil
}
