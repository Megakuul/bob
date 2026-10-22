package nix

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// synthRepo writes a git repository with the given files (path → content).
func synthRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "synth")
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
	return dir
}

// probeFiles is the S0b probe: a bob.Run step (nixpkgs bash and coreutils, substituted from the
// cache: probe F1) that records, from inside the Nix build sandbox, the uid, /home, the network
// interfaces and whether a connection can be made (probe F3).
func probeFiles() map[string]string {
	return map[string]string{
		"bob.go": "//go:build bob\n\npackage synth\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"probe/bob.go": `//go:build bob

package probe

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Probe records facts about its own sandbox.
var Probe = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "probe", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},
		Cmd: []string{"bash", "-c", "id -u > $BOB_OUT/uid; ls -A /home > $BOB_OUT/home; tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' > $BOB_OUT/ifaces; if timeout 3 bash -c 'exec 3<>/dev/tcp/1.1.1.1/53'; then echo up; else echo blocked; fi > $BOB_OUT/net"},
		Outputs: []string{"uid", "home", "ifaces", "net"},
	})
})
`,
	}
}

// TestRNIX021NamespaceProbes is S0b's probes F1 and F3 in bind mode (this host has /nix; pivot mode
// runs in tests/rootless): substituting packages from the cache works, and inside the build
// sandbox the user is not root, the network is blocked, /home is hidden and only loopback exists.
// Four parallel bob builds on one state finish without lock errors.
func TestRNIX021NamespaceProbes(t *testing.T) {
	parity.Covers(t, "R-NIX-021")
	if _, err := os.Stat("/nix"); err != nil {
		t.Fatalf("harness: bind mode needs /nix on the host: %v", err)
	}
	e := newBobEnv(t)
	dir := synthRepo(t, probeFiles())
	e.fixtures["probe"] = dir
	p, r := e.buildPath(t, "probe", "//probe:probe")
	if harness.Red(t, r) {
		return
	}
	if os.Getuid() == 0 {
		t.Log("note: running as root; the rootless matrix (tests/rootless) covers the non-root case")
	}
	read := func(f string) string { return strings.TrimSpace(string(mustRead(t, filepath.Join(e.Home, p, f)))) }
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
	// Budget: namespace entry ≤ 10 ms (SPEC §1.17) is measured by the budgets stage.
}
