package nix

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// probeFiles is the S0b probe: a bob.Run step (nixpkgs bash and coreutils, substituted from the
// cache: probe F1) that records, from inside the Nix build sandbox, the uid, /home, the network
// interfaces and whether a connection can be made (probe F3).
func probeFiles() map[string]string {
	return map[string]string{
		"bob.go": "//go:build bob\n\npackage synth\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})\n",
		"probe/bob.go": `//go:build bob

package probe

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Probe records facts about its own sandbox.
var Probe = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "probe", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Bash.Tool(p), nixpkgs.Coreutils.Tool(p)},
		Cmd: []string{"bash", "-c", "id -u > $BOB_OUT/uid; ls -A /home > $BOB_OUT/home; tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' > $BOB_OUT/ifaces; if timeout 3 bash -c 'exec 3<>/dev/tcp/1.1.1.1/53'; then echo up; else echo blocked; fi > $BOB_OUT/net"},
		Outputs: []string{"uid", "home", "ifaces", "net"},
	})
})
`,
	}
}

// nonRoot runs bob as an unprivileged user in bind mode (the host has /nix): the user running
// the tests when it is not root, otherwise the user bobtest (HARNESS.md §7). Everything bob
// touches lives in a world-readable share directory owned by that user.
type nonRoot struct {
	share, bob string
	cred       *syscall.Credential
}

func newNonRoot(t testing.TB) *nonRoot {
	t.Helper()
	share, err := os.MkdirTemp("/var/tmp", "bob-nix021-")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(share) })
	if err := os.Chmod(share, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	n := &nonRoot{share: share, bob: filepath.Join(share, "bob")}
	mustWrite(t, n.bob, mustRead(t, harness.Bin(t, "bob")), 0o755)
	if os.Getuid() == 0 {
		u, err := user.Lookup("bobtest")
		if err != nil {
			t.Fatalf("harness: R-NIX-021 needs the unprivileged user bobtest (HARNESS.md §7): %v", err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		n.cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	return n
}

// own hands dir (recursively) to the unprivileged user.
func (n *nonRoot) own(t testing.TB, dir string) {
	t.Helper()
	if n.cred == nil {
		return
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(n.cred.Uid), int(n.cred.Gid))
	})
	if err != nil {
		t.Fatalf("harness: chown %s: %v", dir, err)
	}
}

// run runs bob in dir with its own state below the share directory.
func (n *nonRoot) run(t testing.TB, dir string, args ...string) harness.Result {
	t.Helper()
	state := filepath.Join(n.share, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	n.own(t, state)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, n.bob, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + state, "TZ=UTC", "LC_ALL=C",
		"BOB_HOME=" + filepath.Join(state, "bobhome"),
		"XDG_CONFIG_HOME=" + filepath.Join(state, "config"),
		"XDG_DATA_HOME=" + filepath.Join(state, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(state, "cache"),
	}
	if n.cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: n.cred}
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r := harness.Result{Args: args}
	err := cmd.Run()
	r.Stdout, r.Stderr = so.String(), se.String()
	if ee, ok := err.(*exec.ExitError); ok {
		r.Exit = ee.ExitCode()
	} else if err != nil {
		r.Exit = -1
		r.Stderr += "\nharness: " + err.Error()
	}
	return r
}

// TestRNIX021NamespaceProbes is S0b's probes F1 and F3 in bind mode as a non-root user (this
// host has /nix; pivot mode runs in tests/rootless): substituting packages from the cache works,
// and inside the build sandbox the user is not root, the network is blocked, /home is hidden
// and only loopback exists. The namespace entry takes <= 10 ms (budget B-009 in
// tests/budgets/budgets.json, measured by the stage event "namespace"). Four parallel bob
// builds on one state finish without lock errors.
// Ruling G16: the 10 ms budget is observed as the durationMs of the `namespace` stage event; bob
// runs as the user bobtest when the tests run as root (non-root bind mode).
func TestRNIX021NamespaceProbes(t *testing.T) {
	parity.Covers(t, "R-NIX-021")
	if _, err := os.Stat("/nix"); err != nil {
		t.Fatalf("harness: bind mode needs /nix on the host: %v", err)
	}
	n := newNonRoot(t)
	dir := filepath.Join(n.share, "probe-repo")
	files := probeFiles()
	files["go.mod"] = "module example.com/synth\n\ngo 1.26.0\n"
	files["bob.lock"] = "{\n  \"lockVersion\": 1\n}\n"
	for p, c := range files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(p)), []byte(c), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=h", "-c", "user.email=h@bob.invalid", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("harness: git %v: %v: %s", args, err, b)
		}
	}
	n.own(t, dir)
	r := n.run(t, dir, "build", "--json", "--print-paths", "//probe:probe")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob build //probe:probe as non-root (bind mode): exit %d:\n%s", r.Exit, r.Combined())
	}
	var p string
	for _, ev := range events(t, r.Stdout) {
		if l, _ := ev["label"].(string); ev["event"] == "package.finish" && strings.HasPrefix(l, "//probe:probe") {
			if ps, _ := ev["paths"].([]any); len(ps) > 0 {
				p, _ = ps[0].(string)
			}
		}
		// Budget B-009 (R-NIX-021: namespace entry <= 10 ms [8c/15G] and [2c/4G]).
		if ev["event"] == "namespace" {
			if ms, ok := ev["durationMs"].(float64); !ok || ms > 10 {
				t.Errorf("namespace entry took %v ms, want <= 10 ms (B-009): %v", ev["durationMs"], ev)
			}
		}
	}
	if p == "" {
		t.Fatalf("no paths[] in the package.finish event of //probe:probe:\n%s", r.Stdout)
	}
	read := func(f string) string {
		return strings.TrimSpace(string(mustRead(t, filepath.Join(n.share, "state", "bobhome", "nix", "store", filepath.Base(p), f))))
	}
	if got := read("uid"); got != "1000" {
		t.Errorf("uid inside the build = %q, want 1000 (not root)", got)
	}
	if got := read("home"); got != "" {
		t.Errorf("/home inside the build lists %q, want nothing", got)
	}
	if got := read("ifaces"); got != "lo" {
		t.Errorf("interfaces inside the build = %q, want only the loopback lo", got)
	}
	if got := read("net"); got != "blocked" {
		t.Errorf("network inside the build: %q, want blocked", got)
	}
	var wg sync.WaitGroup
	results := make([]harness.Result, 4)
	e2 := newBobEnv(t)
	e2.dir(t, "cpp-lib")
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e2.run(t, "cpp-lib", "build", "//...")
		}()
	}
	wg.Wait()
	for i, r := range results {
		out := strings.ToLower(r.Combined())
		if r.Exit != 0 || strings.Contains(out, "database is locked") || strings.Contains(out, "bob-e-7004") {
			t.Errorf("parallel build %d: exit %d:\n%s", i, r.Exit, r.Combined())
		}
	}
}
