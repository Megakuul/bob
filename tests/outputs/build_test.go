package outputs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
)

// storePathOf turns a host path <home>/nix/store/<base> into the virtual /nix/store/<base>.
func storePathOf(host string) string { return "/nix/store/" + filepath.Base(host) }

// R-OUT-001: every package is a closure output; `bob build --json` reports the output path of
// the label, and the path holds the program.
func TestROUT001ClosureOutput(t *testing.T) {
	parity.Covers(t, "R-OUT-001")
	w := newWorld(t, "polyglot")
	out, ok := w.buildOne("//cmd/api:api")
	if !ok {
		return
	}
	if !strings.Contains(out, "/nix/store/") {
		t.Errorf("output path %q is not a store path", out)
	}
	bin := filepath.Join(out, "bin", "api")
	fi, err := os.Stat(bin)
	if err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("%s is not an executable file: %v", bin, err)
	}
	if m := elfMachine(t, bin); m.String() != map[string]string{"arm64": "EM_AARCH64", "amd64": "EM_X86_64"}[strings.TrimPrefix(string(hostPlatform(t)), "linux-")] {
		t.Errorf("host build of api is %v", m)
	}
}

// R-OUT-001: the worker uploads closures to the cache; after `bob build --remote` the path is
// substitutable from the server (`nix path-info --store <server>`).
func TestROUT001RemoteClosureSubstitutable(t *testing.T) {
	parity.Covers(t, "R-OUT-001")
	url, token, ok := serverURLs(t)
	if !ok {
		return
	}
	w := newWorld(t, "polyglot")
	login := harness.Run(t, "bob", harness.Spec{Dir: w.dir, Env: w.env(), Stdin: token + "\n"}, "login", "--token-stdin", url)
	if harness.Red(t, login) {
		return
	}
	if login.Exit != 0 {
		t.Fatalf("bob login: exit %d\n%s", login.Exit, login.Combined())
	}
	local, ok := w.buildOne("//cmd/api:api")
	if !ok {
		return
	}
	r := w.bob("build", "--remote", "//cmd/api:api")
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	nix := clients.Nix(t)
	nix.Env = append(nix.Env, "NIX_CONFIG=netrc-file = "+netrcFile(t, nix, url, token))
	pi := nix.Run("path-info", "--store", url+"/nix", storePathOf(local))
	if pi.Exit != 0 || !strings.Contains(pi.Stdout, storePathOf(local)) {
		t.Errorf("nix path-info --store %s/nix %s: exit %d\n%s%s", url, storePathOf(local), pi.Exit, pi.Stdout, pi.Stderr)
	}
}

// R-OUT-006: bundles are ordinary derivations built by bob-builder subcommands, they build in a
// namespace without any network (the derivations are non-fixed-output, so nothing needs the
// network; the inputs are warmed online first and the offline rebuild must succeed), and they
// cache like any package (second build: nothing built).
func TestROUT006BundlesAreBuildTargets(t *testing.T) {
	parity.Covers(t, "R-OUT-006")
	w := newWorld(t, "polyglot")
	if _, ok := w.buildOne("//cmd/api:api"); !ok {
		return
	}
	targets := []string{"//deploy:api-image", "//deploy:api-appimage", "//deploy:api-tar"}
	// Warm: the first build runs online (it substitutes nss-cacert, tzdata, glibc, ...); bundle
	// derivations themselves are built in the sandbox without network.
	if _, ok := w.build(targets...); !ok {
		return
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Fatalf("harness: unshare not found: %v", err)
	}
	off := harness.RunPath(t, unshare, harness.Spec{
		Dir:     w.dir,
		Env:     w.env(),
		Timeout: 8 * time.Minute,
	}, append([]string{"--map-current-user", "--net", "--", harness.Bin(t, "bob"), "build"}, targets...)...)
	if harness.Red(t, off) || !exitIs(t, off, 0) {
		return
	}
	// A rebuild without network after the warm build is a no-op and needs nothing from outside.
	n, ok := w.builtCount(targets...)
	if !ok {
		return
	}
	if n != 0 {
		t.Errorf("second build of the bundles built %d derivations, want 0 (no-op)", n)
	}

	all, ok := w.bundleDrvs()
	if !ok {
		return
	}
	for _, sub := range bundleSubcommands {
		ds := bundleDrvsOf(all, sub)
		if len(ds) == 0 {
			t.Errorf("no derivation built by `bob-builder %s`", sub)
		}
		for _, d := range ds {
			for _, o := range d.D.Outputs {
				if o.Hash != "" {
					t.Errorf("%s: output %s is fixed-output (hash %s); bundle builders use no network", d.File, o.Name, o.Hash)
				}
			}
			for k, v := range d.D.Env {
				for _, bad := range []string{"/docker", "/dockerd", "/podman", "/skopeo", "/crane"} {
					if strings.Contains(strings.ToLower(v), bad) {
						t.Errorf("%s: %s mentions %s; builders never run a container tool", d.File, k, bad)
					}
				}
			}
		}
	}
}
