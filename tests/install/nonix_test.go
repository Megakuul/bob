package install

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/cli/fakeserver"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// noNix runs `bash -c script` as an unprivileged user (uid 1000) in a mount namespace where /nix
// is an empty tmpfs: the host has no /nix, no Go and no Nix for bob (HARNESS §7.1 uses distro
// rootfs images through bwrap for the full matrix; this is the same condition on this host).
func (e env) noNix(t testing.TB, script string) harness.Result {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatalf("harness error: unshare not found: %v", err)
	}
	inner := "mount -t tmpfs none /nix && exec unshare --user --map-user=1000 --map-group=1000 -- bash -c " + shellQuote(script)
	return harness.RunPath(t, unshare, harness.Spec{Dir: t.TempDir(), Env: e.vars, Timeout: 4 * time.Minute},
		"--user", "--map-root-user", "--mount", "sh", "-c", inner)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// TestRINST002NoSystemNix: with only the bob binary, no /nix, no Go and no Nix, and a non-root
// user, `bob install acme/cli` against a public registry works and `acme --version` works from a
// new login shell.
func TestRINST002NoSystemNix(t *testing.T) {
	parity.Covers(t, "R-INST-002")
	srv := fakeserver.New(t, fakeserver.Options{Name: "default"})
	publish(t, srv, "acme", "cli", false, []string{"acme"}, "1.0.0")
	e := newEnv(t)
	bobBin := harness.Bin(t, "bob")

	probe := e.bob(t, "", "registry", "add", "default", srv.URL())
	if harness.Red(t, probe) {
		return
	}
	// The registry is configured outside, then everything else runs without /nix.
	r := e.noNix(t, fmt.Sprintf(`test "$(id -u)" = 1000 && ! test -e /nix/store && ! command -v go && ! command -v nix && %s install acme/cli && bash -lc 'acme --version'`, shellQuote(bobBin)))
	if r.Exit != 0 {
		t.Fatalf("install without /nix: exit %d\n%s", r.Exit, r.Combined())
	}
	if !strings.HasSuffix(strings.TrimSpace(r.Stdout), "1.0.0") {
		t.Errorf("`acme --version` from a new login shell did not print 1.0.0:\n%s", r.Stdout)
	}
	noLeak(t, r)
	// the library of the host was not needed: no request went to anything but the registry.
	if srv.Count("/api/bob.release.v1.ReleaseService/ResolveRelease") == 0 {
		t.Errorf("the registry saw no ResolveRelease request")
	}
}

// TestRINST002NothingToInstall: a release without closure artifacts is not installable: exit 2,
// BOB-E-0009 with the SPEC text.
func TestRINST002NothingToInstall(t *testing.T) {
	parity.Covers(t, "R-INST-002")
	srv := fakeserver.New(t, fakeserver.Options{Name: "default"})
	srv.AddRelease(t, fakeserver.Release{Project: "acme", Name: "imgonly", Version: "1.0.0", Images: []string{"image"}})
	e := newEnv(t)
	if r := e.bob(t, "", "registry", "add", "default", srv.URL()); harness.Red(t, r) {
		return
	}
	r := e.bob(t, "", "install", "acme/imgonly@1")
	if r.Exit != 2 {
		t.Errorf("exit %d, want 2\n%s", r.Exit, r.Combined())
	}
	out := r.Combined()
	want := `acme/imgonly@1 has nothing to install (it contains: oci image "image"); download its artifacts from ` + srv.URL() + `/p/acme/releases/imgonly/1`
	if !strings.Contains(out, "BOB-E-0009") || !strings.Contains(out, want) {
		t.Errorf("want BOB-E-0009 with `%s`:\n%s", want, out)
	}
	if _, err := os.Lstat(e.installDir() + "/current"); err == nil {
		t.Errorf("a generation was written for a release with nothing to install")
	}
	noLeak(t, r)
}

// TestRINST002ShimOverhead: the installed shim's start overhead is ≤ 15 ms p95 [8c/15G] [proposal;
// S1 shim 6.8–7.8 ms]. Budget row: the SPEC acceptance text itself. The overhead is the p95 of the
// shim run minus the median of running the same program directly.
func TestRINST002ShimOverhead(t *testing.T) {
	parity.Covers(t, "R-INST-002")
	e, srv := setup(t)
	if r := e.bob(t, "", "registry", "add", "default", srv.URL()); harness.Red(t, r) {
		return
	}
	if r := e.bob(t, "", "install", "acme/cli@1.0.0"); r.Exit != 0 {
		t.Fatalf("install: exit %d\n%s", r.Exit, r.Combined())
	}
	direct := t.TempDir() + "/prog"
	if err := os.WriteFile(direct, program(t, "1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	measure := func(path string) []time.Duration {
		var d []time.Duration
		for range 100 {
			t0 := time.Now()
			cmd := exec.Command(path, "--version")
			cmd.Env = e.vars
			_ = cmd.Run()
			d = append(d, time.Since(t0))
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d
	}
	shim, base := measure(e.shim("acme")), measure(direct)
	if over := shim[94] - base[49]; over > 15*time.Millisecond {
		t.Errorf("shim start overhead p95 = %v (shim p95 %v, direct median %v), budget 15 ms", over, shim[94], base[49])
	}
}
