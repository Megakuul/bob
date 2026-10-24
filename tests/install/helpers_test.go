package install

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
)

// env is one consumer's environment: a fresh HOME, and PATH with bob's bin dir first.
type env struct {
	home string
	vars []string
}

func newEnv(t testing.TB) env {
	t.Helper()
	h := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	return env{home: h, vars: []string{
		"HOME=" + h,
		"XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(h, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(h, ".cache"),
		"PATH=" + filepath.Join(h, ".local", "bin") + ":/usr/bin:/bin",
		"TERM=dumb",
		"BOB=" + harness.Bin(t, "bob"),
	}}
}

func (e env) binDir() string       { return filepath.Join(e.home, ".local", "bin") }
func (e env) shim(n string) string { return filepath.Join(e.binDir(), n) }
func (e env) installDir() string {
	return filepath.Join(e.home, ".local", "share", "bob", "install")
}
func (e env) with(extra ...string) env {
	return env{home: e.home, vars: append(append([]string{}, e.vars...), extra...)}
}

// bob runs bob in dir.
func (e env) bob(t testing.TB, dir string, args ...string) harness.Result {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	return harness.Run(t, "bob", harness.Spec{Dir: dir, Env: e.vars, Timeout: 4 * time.Minute}, args...)
}

// run runs a program (a shim) with the consumer's environment.
func (e env) run(t testing.TB, dir, path string, args ...string) harness.Result {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	return harness.RunPath(t, path, harness.Spec{Dir: dir, Env: e.vars}, args...)
}

// version runs the installed program `name` and returns what `--version` printed.
func (e env) version(t testing.TB, name string) (string, harness.Result) {
	t.Helper()
	r := e.run(t, "", e.shim(name), "--version")
	return strings.TrimSpace(r.Stdout), r
}

func noLeak(t testing.TB, r harness.Result) {
	t.Helper()
	for _, l := range leakcheck.Check([]byte(r.Combined())) {
		t.Errorf("raw Nix text in output of bob %s: %s", strings.Join(r.Args, " "), l)
	}
}

// program compiles testdata/prog as a static binary with the version burned in.
func program(t testing.TB, version string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "prog-"+version)
	cmd := exec.Command("go", "build", "-o", out, "-ldflags", "-X main.version="+version, ".")
	cmd.Dir = filepath.Join(harness.Root(t), "tests", "install", "testdata", "prog")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: go build prog: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// publish adds release versions of project/name with bin names to srv.
func publish(t testing.TB, srv *fakeserver.Server, project, name string, private bool, bins []string, versions ...string) {
	t.Helper()
	for _, v := range versions {
		srv.AddRelease(t, fakeserver.Release{Project: project, Name: name, Version: v, Private: private, Program: program(t, v), Bins: bins})
	}
}

// addRegistry runs `bob registry add <name> <url>`.
func (e env) addRegistry(t testing.TB, name string, srv *fakeserver.Server) harness.Result {
	t.Helper()
	return e.bob(t, "", "registry", "add", name, srv.URL())
}

// generation is a generations/<n>.json file (SPEC §02.8 Files).
type generation struct {
	V       int    `json:"v"`
	N       int    `json:"n"`
	Created string `json:"created"`
	Entries []struct {
		Name       string            `json:"name"`
		Registry   string            `json:"registry"`
		Ref        string            `json:"ref"`
		Version    string            `json:"version"`
		Platform   string            `json:"platform"`
		StorePaths []string          `json:"storePaths"`
		Bins       map[string]string `json:"bins"`
	} `json:"entries"`
}

func (e env) readGeneration(t testing.TB, n int) generation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.installDir(), "generations", itoa(n)+".json"))
	if err != nil {
		t.Errorf("generation %d: %v", n, err)
		return generation{}
	}
	var g generation
	if err := json.Unmarshal(b, &g); err != nil {
		t.Errorf("generation %d is not JSON: %v", n, err)
	}
	return g
}

// current returns the number of the generation `current` points to (0: no current).
func (e env) current(t testing.TB) int {
	t.Helper()
	l, err := os.Readlink(filepath.Join(e.installDir(), "current"))
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSuffix(filepath.Base(l), ".json") {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func sortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func readFile(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// events decodes the NDJSON stdout of a --json run.
func events(t testing.TB, stdout string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range lines(stdout) {
		var e map[string]any
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Errorf("stdout line is not JSON: %q", l)
			continue
		}
		out = append(out, e)
	}
	return out
}

// start starts bob in the background (for kill tests); the caller waits for it.
func (e env) start(t testing.TB, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(harness.Bin(t, "bob"), args...)
	cmd.Dir = t.TempDir()
	cmd.Env = e.vars
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// tree concatenates the content of every file under dir (excluding .git), for "changes no file".
func tree(t testing.TB, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b.WriteString(rel + "\x00" + string(data) + "\x00")
		return nil
	})
	if err != nil {
		t.Fatalf("harness: hash tree: %v", err)
	}
	return b.String()
}
