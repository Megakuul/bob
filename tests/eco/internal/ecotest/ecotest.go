// Package ecotest is the shared helper set of the ecosystem acceptance suites tests/eco/{npm,
// ocaml,gen}: running bob in a fixture copy with its own home, reading --json events, and
// pointing bob at the offline registry mirror (tools/regmirror, HARNESS.md §4.3).
package ecotest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// Bob runs bob in dir with its own bob home; calls sharing a home share one store. Output that
// shows raw Nix text fails t (AGENTS.md §5.5) unless -v is given.
func Bob(t *testing.T, dir, home string, args ...string) harness.Result {
	t.Helper()
	return BobEnv(t, dir, []string{"BOB_HOME=" + home}, args...)
}

// BobEnv is Bob with an explicit environment.
func BobEnv(t *testing.T, dir string, env []string, args ...string) harness.Result {
	t.Helper()
	r := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Timeout: 5 * time.Minute}, args...)
	for _, a := range args {
		if a == "-v" || a == "-vv" {
			return r
		}
	}
	for _, l := range leakcheck.Check([]byte(r.Combined())) {
		t.Errorf("bob %s shows raw Nix text: %s", strings.Join(r.Args, " "), l)
	}
	return r
}

// OK is the first check after a bob run that must succeed: false (and t failed) when the
// feature is not built yet (red) or the run failed.
func OK(t *testing.T, r harness.Result) bool {
	t.Helper()
	if harness.Red(t, r) {
		return false
	}
	return ExitIs(t, r, 0)
}

// ExitIs fails t unless r exited with want.
func ExitIs(t *testing.T, r harness.Result, want int) bool {
	t.Helper()
	if r.Exit != want {
		t.Errorf("bob %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.Args, " "), r.Exit, want, r.Stdout, r.Stderr)
		return false
	}
	return true
}

// Event is one --json line (SPEC §02.4).
type Event map[string]any

// Str returns a string field.
func (e Event) Str(k string) string { s, _ := e[k].(string); return s }

// Events parses the --json lines of r.
func Events(t *testing.T, r harness.Result) []Event {
	t.Helper()
	var out []Event
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("bob %s: --json line is not JSON: %q", strings.Join(r.Args, " "), line)
			continue
		}
		out = append(out, e)
	}
	return out
}

// Named filters events by their "event" name.
func Named(evs []Event, name string) []Event {
	var out []Event
	for _, e := range evs {
		if e.Str("event") == name {
			out = append(out, e)
		}
	}
	return out
}

// BuiltCount is counts.built of the run.finish event (the derivations bob built), -1 on error.
func BuiltCount(t *testing.T, r harness.Result) int {
	t.Helper()
	fin := Named(Events(t, r), "run.finish")
	if len(fin) != 1 {
		t.Errorf("bob %s: want one run.finish event, got %d", strings.Join(r.Args, " "), len(fin))
		return -1
	}
	counts, _ := fin[0]["counts"].(map[string]any)
	n, _ := counts["built"].(float64)
	return int(n)
}

// BuiltLabels lists the labels of package.finish events with status "built", sorted.
func BuiltLabels(t *testing.T, r harness.Result) []string {
	t.Helper()
	var out []string
	for _, e := range Named(Events(t, r), "package.finish") {
		if e.Str("status") == "built" {
			out = append(out, e.Str("label"))
		}
	}
	sort.Strings(out)
	return out
}

// FinishPaths returns label to output paths of the package.finish events of r.
func FinishPaths(t *testing.T, r harness.Result) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, e := range Named(Events(t, r), "package.finish") {
		ps, _ := e["paths"].([]any)
		for _, p := range ps {
			s, _ := p.(string)
			out[e.Str("label")] = append(out[e.Str("label")], s)
		}
	}
	return out
}

// ErrorCodes lists the codes of the error events of r.
func ErrorCodes(t *testing.T, r harness.Result) []string {
	t.Helper()
	var out []string
	for _, e := range Named(Events(t, r), "error") {
		m, _ := e["error"].(map[string]any)
		c, _ := m["code"].(string)
		out = append(out, c)
	}
	return out
}

// Registry starts the offline registry mirror (or adopts BOB_TEST_REGISTRY_URL) and returns the
// environment that points bob at it. The mirror serves tests/fixtures/registry.
func Registry(t *testing.T, home string) []string {
	t.Helper()
	url := os.Getenv("BOB_TEST_REGISTRY_URL")
	if url == "" {
		blobs := os.Getenv("BOB_REGISTRY_BLOBS")
		if blobs == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				t.Fatalf("harness: %v", err)
			}
			blobs = filepath.Join(cache, "bob-test", "registry-blobs")
		}
		url = startMirror(t, filepath.Join(harness.Root(t), "tests", "fixtures", "registry"), blobs)
	}
	env := []string{
		"BOB_HOME=" + home,
		"BOB_TEST_HTTP_LOOPBACK=1", // the mirror is plain http on loopback (A109)
		"BOB_NPM_REGISTRY=" + url + "/npm/",
		"BOB_OPAM_CACHE_URL=" + url + "/opam/cache",
	}
	if c := OpamCommit(t); c != "" {
		env = append(env, "BOB_OPAM_REPOSITORY="+url+"/opam/repository/"+c+".tar.gz")
	}
	return env
}

// OpamCommit is the opam-repository commit of tests/fixtures/registry/index.json ("" until the
// first refresh).
func OpamCommit(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "tests", "fixtures", "registry", "index.json"))
	if err != nil {
		return ""
	}
	var idx struct {
		Opam struct {
			Commit string `json:"commit"`
		} `json:"opam"`
	}
	if json.Unmarshal(b, &idx) != nil {
		return ""
	}
	return idx.Opam.Commit
}

// Tool returns the store path of a nixpkgs tool of the pin (tools/pins.json, HARNESS.md §1.4).
func Tool(t *testing.T, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, name)
	if err != nil {
		t.Fatalf("%s from the nixpkgs pin: %v", name, err)
	}
	return p
}

// ReadFile reads path or fails t.
func ReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// WriteFile writes a user edit; generated files are read-only (R-FW-060), so it chmods first.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	_ = os.Chmod(path, 0o644)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("harness: %v", err)
	}
}

// ReplaceIn replaces the first occurrence of old in file; t fails when old is absent.
func ReplaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	s := ReadFile(t, path)
	if !strings.Contains(s, old) {
		t.Fatalf("harness: %s does not contain %q", path, old)
	}
	WriteFile(t, path, strings.Replace(s, old, new, 1))
}

// TreeDigest hashes names, modes, link targets and contents below path.
func TreeDigest(t *testing.T, path string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "\x00" + info.Mode().String() + "\x00"))
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Write([]byte(l))
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("harness: digest %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

var storeRef = regexp.MustCompile(`[0-9a-z]{32}-[A-Za-z0-9+._?=-]+`)

// Closure computes the runtime closure of a store path the way Nix does: it scans the files and
// symlink targets of every path for the 32-character hash names of other store paths that exist
// in the store of bob home, recursively. It returns the sorted store entry names and the sum of
// the sizes of their regular files. path may be given as /nix/store/<name> or as a path in home.
func Closure(t *testing.T, home, path string) (names []string, size int64) {
	t.Helper()
	store := filepath.Join(home, "nix", "store")
	seen := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		root := filepath.Join(store, name)
		if _, err := os.Lstat(root); err != nil {
			return // a reference to a path that is not in the store (hash collision in text)
		}
		seen[name] = true
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			var text []byte
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				l, err := os.Readlink(p)
				if err != nil {
					return err
				}
				text = []byte(l)
			case info.Mode().IsRegular():
				size += info.Size()
				if text, err = os.ReadFile(p); err != nil {
					return err
				}
			}
			for _, m := range storeRef.FindAll(text, -1) {
				visit(string(m))
			}
			return nil
		})
		if err != nil {
			t.Errorf("harness: closure scan of %s: %v", name, err)
		}
	}
	visit(filepath.Base(path))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, size
}

// OutputPaths builds args with --print-paths and returns label to output paths; ok is false
// (and t failed) when the build is red or fails.
func OutputPaths(t *testing.T, dir string, env []string, args ...string) (map[string][]string, bool) {
	t.Helper()
	r := BobEnv(t, dir, env, append([]string{"build", "--print-paths", "--json"}, args...)...)
	if !OK(t, r) {
		return nil, false
	}
	return FinishPaths(t, r), true
}

// RunEnvI runs binary with an empty environment (env -i) and returns the result.
func RunEnvI(t *testing.T, binary string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{}
	cmd.Dir = t.TempDir()
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := harness.Result{Args: append([]string{binary}, args...)}
	switch err := cmd.Run().(type) {
	case nil:
	case *exec.ExitError:
		r.Exit = err.ExitCode()
	default:
		r.Exit = -1
		r.Stderr = "harness: " + err.Error()
	}
	r.Stdout, r.Stderr = so.String(), r.Stderr+se.String()
	return r
}

// Unprivileged returns the uid/gid of the harness test user (bobtest) when the tests run as
// root, or the current ids otherwise (docs/environment.md).
func Unprivileged(t *testing.T) (uid, gid uint32) {
	t.Helper()
	if os.Geteuid() != 0 {
		return uint32(os.Getuid()), uint32(os.Getgid())
	}
	u, err := user.Lookup("bobtest")
	if err != nil {
		t.Fatalf("harness: the test user bobtest is missing (docs/environment.md): %v", err)
	}
	ui, _ := strconv.Atoi(u.Uid)
	gi, _ := strconv.Atoi(u.Gid)
	return uint32(ui), uint32(gi)
}

// ChownTree hands dir to uid/gid and makes its parents traversable (a no-op when not root).
func ChownTree(t *testing.T, dir string, uid, gid uint32) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(uid), int(gid))
	})
	if err != nil {
		t.Fatalf("harness: chown %s: %v", dir, err)
	}
	for p := filepath.Dir(dir); p != "/"; p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o001 == 0 {
			_ = os.Chmod(p, info.Mode().Perm()|0o011)
		}
	}
}

var (
	mirrorOnce sync.Once
	mirrorBin  string
	mirrorErr  error
)

// startMirror builds tools/regmirror (a separate Go module of the workspace, so the root module
// cannot import it) once per test binary, starts it on a loopback port and returns its URL.
func startMirror(t *testing.T, manifests, blobs string) string {
	t.Helper()
	mirrorOnce.Do(func() {
		dir, err := os.MkdirTemp("", "regmirror-bin")
		if err != nil {
			mirrorErr = err
			return
		}
		mirrorBin = filepath.Join(dir, "regmirror")
		cmd := exec.Command("go", "build", "-o", mirrorBin, "github.com/megakuul/bob/tools/regmirror/cmd/regmirror")
		cmd.Dir = harness.Root(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			mirrorErr = fmt.Errorf("go build regmirror: %w: %s", err, out)
		}
	})
	if mirrorErr != nil {
		t.Fatalf("harness: %v", mirrorErr)
	}
	cmd := exec.Command(mirrorBin, "-manifests", manifests, "-blobs", blobs)
	p, err := serverenv.StartProcess(cmd, filepath.Join(t.TempDir(), "regmirror.log"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	re := regexp.MustCompile(`listening on (http://\S+)`)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(p.Log)
		if m := re.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	t.Fatalf("harness: the registry mirror did not start")
	return ""
}
