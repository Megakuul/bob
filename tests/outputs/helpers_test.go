package outputs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// apiLine is the golden line of the polyglot //cmd/api:api (sha256("bob"), Rust numhex 42, protobuf
// size of {uuid, "hello 3432", digest}).
const apiLine = "digest=81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9 numhex=3432 proto_bytes=116"

func hostPlatform(t testing.TB) bob.Platform {
	t.Helper()
	switch runtime.GOARCH {
	case "arm64":
		return bob.LinuxARM64
	case "amd64":
		return bob.LinuxAMD64
	}
	t.Fatalf("harness: unsupported host architecture %s", runtime.GOARCH)
	return ""
}

// ociArch is the OCI architecture name of a platform.
func ociArch(p bob.Platform) string {
	if p == bob.LinuxAMD64 {
		return "amd64"
	}
	return "arm64"
}

// world is one repository copy and one bob home, shared by the builds of a test so that
// derivations accumulate (a second build is a no-op).
type world struct {
	t    *testing.T
	dir  string
	home string
}

// newWorld copies fixture into a fresh git repository with a fresh bob home.
func newWorld(t *testing.T, fixture string) *world {
	t.Helper()
	w := &world{t: t, dir: harness.Fixture(t, fixture), home: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(w.home, "user"), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	return w
}

// overlay writes files (path relative to the repository → content) into the repository and
// commits them.
func (w *world) overlay(files map[string]string) {
	w.t.Helper()
	for rel, content := range files {
		p := filepath.Join(w.dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatalf("harness: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			w.t.Fatalf("harness: %v", err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "overlay"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = w.dir
		if b, err := cmd.CombinedOutput(); err != nil {
			w.t.Fatalf("harness: git %v: %v: %s", args, err, b)
		}
	}
}

// sibling returns a world on the same repository directory with another bob home: a "fresh
// store" for reproducibility checks.
func (w *world) freshStore() *world {
	f := &world{t: w.t, dir: w.dir, home: w.t.TempDir()}
	if err := os.MkdirAll(filepath.Join(f.home, "user"), 0o755); err != nil {
		w.t.Fatalf("harness: %v", err)
	}
	return f
}

// bob runs bob in the repository with this world's home; raw Nix text in the output of a
// command without -v fails the test (AGENTS.md §5.5).
func (w *world) bob(args ...string) harness.Result {
	w.t.Helper()
	return w.bobEnv(nil, args...)
}

func (w *world) bobEnv(env []string, args ...string) harness.Result {
	w.t.Helper()
	r := harness.Run(w.t, "bob", harness.Spec{Dir: w.dir, Env: append(w.env(), env...), Timeout: 8 * time.Minute}, args...)
	if !contains(args, "-v") {
		shown := r.Combined()
		if contains(args, "--print-paths") {
			// --print-paths prints host paths <home>/nix/store/<hash>-<name> on purpose (SPEC §02.6,
			// §02.7.5); they are not raw Nix text.
			shown = r.Stderr + "\n" + printPathsRE.ReplaceAllString(r.Stdout, `"paths":[]`)
		}
		for _, l := range leakcheck.Check([]byte(shown)) {
			w.t.Errorf("bob %s shows raw Nix text: %s", strings.Join(args, " "), l)
		}
	}
	return r
}

// env is the environment of every bob run of this world: its own bob home and a user home that
// persists between runs (credentials, configuration).
func (w *world) env() []string {
	h := filepath.Join(w.home, "user")
	return []string{
		"BOB_HOME=" + w.home, "HOME=" + h, "XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"), "XDG_STATE_HOME=" + filepath.Join(h, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(h, ".cache"), "TERM=dumb",
	}
}

// printPathsRE matches the paths[] array of a package.finish event.
var printPathsRE = regexp.MustCompile(`"paths"\s*:\s*\[[^\]]*\]`)

func contains(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}

// event is one --json line (SPEC §02.4).
type event map[string]any

func (e event) str(k string) string { s, _ := e[k].(string); return s }

func parseEvents(t testing.TB, r harness.Result) []event {
	t.Helper()
	var out []event
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("bob %s: --json line is not JSON: %q", strings.Join(r.Args, " "), line)
			continue
		}
		out = append(out, e)
	}
	return out
}

// exitIs fails t unless r exited with want.
func exitIs(t testing.TB, r harness.Result, want int) bool {
	t.Helper()
	if r.Exit != want {
		t.Errorf("bob %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.Args, " "), r.Exit, want, r.Stdout, r.Stderr)
		return false
	}
	return true
}

// build runs `bob build --print-paths --json args…` and returns label → first output path (host
// path). ok is false when t is red or failed.
func (w *world) build(args ...string) (map[string]string, bool) {
	w.t.Helper()
	r := w.bob(append([]string{"build", "--print-paths", "--json"}, args...)...)
	if harness.Red(w.t, r) || !exitIs(w.t, r, 0) {
		return nil, false
	}
	out := map[string]string{}
	for _, e := range parseEvents(w.t, r) {
		if e.str("event") != "package.finish" {
			continue
		}
		if ps, _ := e["paths"].([]any); len(ps) > 0 {
			out[e.str("label")], _ = ps[0].(string)
		}
	}
	return out, true
}

// buildOne builds one label and returns its output path.
func (w *world) buildOne(label string, extra ...string) (string, bool) {
	w.t.Helper()
	paths, ok := w.build(append(extra, label)...)
	if !ok {
		return "", false
	}
	p := paths[strings.SplitN(label, "@", 2)[0]]
	if p == "" {
		p = paths[label]
	}
	if p == "" {
		w.t.Errorf("bob build %s printed no output path; got %v", label, paths)
		return "", false
	}
	return p, true
}

// builtCount runs a build and returns counts.built of run.finish.
func (w *world) builtCount(args ...string) (int, bool) {
	w.t.Helper()
	r := w.bob(append([]string{"build", "--json"}, args...)...)
	if harness.Red(w.t, r) || !exitIs(w.t, r, 0) {
		return 0, false
	}
	for _, e := range parseEvents(w.t, r) {
		if e.str("event") == "run.finish" {
			c, _ := e["counts"].(map[string]any)
			n, _ := c["built"].(float64)
			return int(n), true
		}
	}
	w.t.Errorf("no run.finish event in bob %s", strings.Join(args, " "))
	return 0, false
}

// onlyFile returns the single file below dir matching glob.
func onlyFile(t testing.TB, dir, glob string) string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil || len(m) != 1 {
		t.Fatalf("want exactly one %s in %s, got %v (%v)", glob, dir, m, err)
	}
	return m[0]
}

func readFile(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func sha256File(t testing.TB, p string) string {
	t.Helper()
	sum := sha256.Sum256(readFile(t, p))
	return hex.EncodeToString(sum[:])
}

func fileSize(t testing.TB, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// pinned returns the store path of a nixpkgs tool of the pin; a missing pin is a harness error.
func pinned(t testing.TB, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, name)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return p
}

// pinnedBin is bin/<exe> of the pinned tool name.
func pinnedBin(t testing.TB, name, exe string) string {
	t.Helper()
	return filepath.Join(pinned(t, name), "bin", exe)
}

// run runs an arbitrary program with a minimal environment.
func run(t testing.TB, timeout time.Duration, env []string, name string, args ...string) harness.Result {
	t.Helper()
	return harness.RunPath(t, name, harness.Spec{Env: env, Timeout: timeout}, args...)
}

// ---- tar and gzip ----

type tarEntry struct {
	Hdr  tar.Header
	Data []byte
}

// readTar reads an (optionally gzip-compressed) tar stream.
func readTar(t testing.TB, b []byte) []tarEntry {
	t.Helper()
	var r io.Reader = bytes.NewReader(b)
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(r)
		if err != nil {
			t.Fatalf("gzip: %v", err)
		}
		r = zr
	}
	tr := tar.NewReader(r)
	var out []tarEntry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar entry %s: %v", h.Name, err)
		}
		out = append(out, tarEntry{Hdr: *h, Data: data})
	}
}

func tarNames(es []tarEntry) []string {
	var n []string
	for _, e := range es {
		n = append(n, e.Hdr.Name)
	}
	return n
}

func tarFind(es []tarEntry, name string) (tarEntry, bool) {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "./"), "/")
	for _, e := range es {
		if strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(e.Hdr.Name, "./"), "/"), "/") == strings.TrimSuffix(name, "/") {
			return e, true
		}
	}
	return tarEntry{}, false
}

// checkTarRules checks the R-OUT-010 tar rules on every entry: lexical order with parents
// first, mtime 1 s after the epoch, uid/gid 0, no names, no xattrs, no hard links.
func checkTarRules(t testing.TB, what string, es []tarEntry) {
	t.Helper()
	names := tarNames(es)
	if !sort.StringsAreSorted(names) {
		t.Errorf("%s: tar entries are not in lexical order", what)
	}
	seen := map[string]bool{}
	for _, e := range es {
		h := e.Hdr
		clean := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if p := filepath.Dir(clean); p != "." && !seen[p] {
			t.Errorf("%s: %s comes before its parent directory %s", what, h.Name, p)
		}
		seen[clean] = true
		if h.ModTime.Unix() != 1 {
			t.Errorf("%s: %s has mtime %d, want 1", what, h.Name, h.ModTime.Unix())
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: %s has owner %d/%d %q/%q, want 0/0 and no names", what, h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if len(h.PAXRecords) != 0 {
			t.Errorf("%s: %s has xattrs or PAX records %v", what, h.Name, h.PAXRecords)
		}
		if h.Typeflag == tar.TypeLink {
			t.Errorf("%s: %s is a hard link; hard links must be written as regular files", what, h.Name)
		}
	}
}

// gzipHeaderOK checks "no name, ModTime zero" of a gzip stream (R-OUT-010).
func gzipHeaderOK(t testing.TB, what string, b []byte) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Errorf("%s: not gzip: %v", what, err)
		return
	}
	if zr.Name != "" || !zr.ModTime.IsZero() && zr.ModTime.Unix() != 0 {
		t.Errorf("%s: gzip header has name %q mtime %v, want none", what, zr.Name, zr.ModTime)
	}
	if zr.Comment != "" || len(zr.Extra) != 0 {
		t.Errorf("%s: gzip header has comment or extra data", what)
	}
}

// ---- ELF ----

func elfMachine(t testing.TB, p string) elf.Machine {
	t.Helper()
	f, err := elf.Open(p)
	if err != nil {
		t.Fatalf("%s is not an ELF file: %v", p, err)
	}
	defer f.Close()
	return f.Machine
}

func fmtSet(s []string) string { return fmt.Sprintf("%q", s) }
