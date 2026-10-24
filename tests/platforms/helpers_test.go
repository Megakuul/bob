package platforms

import (
	"context"
	"debug/elf"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

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

func otherPlatform(t testing.TB) bob.Platform {
	if hostPlatform(t) == bob.LinuxARM64 {
		return bob.LinuxAMD64
	}
	return bob.LinuxARM64
}

func nixSystem(p bob.Platform) string {
	if p == bob.LinuxAMD64 {
		return "x86_64-linux"
	}
	return "aarch64-linux"
}

func machine(p bob.Platform) elf.Machine {
	if p == bob.LinuxAMD64 {
		return elf.EM_X86_64
	}
	return elf.EM_AARCH64
}

func qemuName(p bob.Platform) string {
	if p == bob.LinuxAMD64 {
		return "x86_64"
	}
	return "aarch64"
}

// world is a fixture copy with its own bob home.
type world struct {
	t    *testing.T
	dir  string
	home string
}

func newWorld(t *testing.T, fixture string) *world {
	t.Helper()
	w := &world{t: t, dir: harness.Fixture(t, fixture), home: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(w.home, "user"), 0o755); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) overlay(files map[string]string) {
	w.t.Helper()
	for rel, c := range files {
		p := filepath.Join(w.dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
	for _, a := range [][]string{{"add", "-A"}, {"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "overlay"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = w.dir
		if b, err := cmd.CombinedOutput(); err != nil {
			w.t.Fatalf("harness: git %v: %v: %s", a, err, b)
		}
	}
}

func (w *world) env() []string {
	h := filepath.Join(w.home, "user")
	return []string{
		"BOB_HOME=" + w.home, "HOME=" + h, "XDG_CONFIG_HOME=" + filepath.Join(h, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(h, ".local", "share"), "XDG_STATE_HOME=" + filepath.Join(h, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(h, ".cache"), "TERM=dumb",
	}
}

// bob runs bob; raw Nix text in the output of a command without -v fails the test.
func (w *world) bob(args ...string) harness.Result { return w.bobEnv(nil, args...) }

func (w *world) bobEnv(env []string, args ...string) harness.Result {
	w.t.Helper()
	r := harness.Run(w.t, "bob", harness.Spec{Dir: w.dir, Env: append(w.env(), env...), Timeout: 10 * time.Minute}, args...)
	verbose := false
	for _, a := range args {
		verbose = verbose || a == "-v"
	}
	if !verbose {
		shown := r.Combined()
		for _, a := range args {
			if a == "--print-paths" {
				// --print-paths prints host paths <home>/nix/store/<hash>-<name> on purpose (SPEC
				// §02.6, §02.7.5); they are not raw Nix text.
				shown = r.Stderr + "\n" + printPathsRE.ReplaceAllString(r.Stdout, `"paths":[]`)
			}
		}
		for _, l := range leakcheck.Check([]byte(shown)) {
			w.t.Errorf("bob %s shows raw Nix text: %s", strings.Join(args, " "), l)
		}
	}
	return r
}

// printPathsRE matches the paths[] array of a package.finish event.
var printPathsRE = regexp.MustCompile(`"paths"\s*:\s*\[[^\]]*\]`)

func exitIs(t testing.TB, r harness.Result, want int) bool {
	t.Helper()
	if r.Exit != want {
		t.Errorf("bob %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(r.Args, " "), r.Exit, want, r.Stdout, r.Stderr)
		return false
	}
	return true
}

// printPaths builds with --print-paths and returns label → first output path.
func (w *world) printPaths(args ...string) (map[string]string, harness.Result, bool) {
	w.t.Helper()
	r := w.bob(append([]string{"build", "--print-paths", "--json"}, args...)...)
	if harness.Red(w.t, r) || !exitIs(w.t, r, 0) {
		return nil, r, false
	}
	out := map[string]string{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["event"] != "package.finish" {
			continue
		}
		if ps, _ := e["paths"].([]any); len(ps) > 0 {
			label, _ := e["label"].(string)
			out[label], _ = ps[0].(string)
		}
	}
	return out, r, true
}

// binsOf lists the files below <out>/bin.
func binsOf(out string) []string {
	m, _ := filepath.Glob(filepath.Join(out, "bin", "*"))
	return m
}

func elfMachineOf(path string) (elf.Machine, error) {
	f, err := elf.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Machine, nil
}

func pinnedTool(t testing.TB, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	p, err := serverenv.NixTool(ctx, name)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return p
}

// qemuRun runs a guest binary under qemu-user; home is the bob home whose store holds the
// guest's /nix/store.
func qemuRun(t testing.TB, p bob.Platform, home, binary string, args ...string) harness.Result {
	t.Helper()
	qemu := filepath.Join(pinnedTool(t, "qemu-user"), "bin", "qemu-"+qemuName(p))
	return harness.RunPath(t, qemu, harness.Spec{Env: []string{"PATH=/usr/bin:/bin"}, Timeout: 5 * time.Minute},
		append([]string{"-L", home, binary}, args...)...)
}
