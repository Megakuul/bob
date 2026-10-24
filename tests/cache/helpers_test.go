package cache

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

var rootSeq atomic.Int32

// local is a throw-away chroot Nix store of the client, so a substituted path lands below
// <root>/nix/store while the logical store dir stays /nix/store (the paths of the server).
type local struct {
	c    *clients.Client
	root string
}

func newLocal(c *clients.Client) local {
	return local{c: c, root: c.Dir(fmt.Sprintf("root-%d", rootSeq.Add(1)))}
}

func (l local) store() string { return "local?root=" + l.root }

// run runs nix with the logical store dir /nix/store (clients.Nix pins a private dir for
// store-less commands).
func (l local) run(args ...string) harness.Result {
	return l.c.RunEnv([]string{"NIX_STORE_DIR=/nix/store"}, args...)
}

// upstream is cache.nixos.org with its public key: the client configuration bob writes lists it
// next to bob's cache (SPEC §6.6.5), and bob never serves a path that cache.nixos.org has
// (R-REG-005), so a closure that holds nixpkgs paths (glibc, gcc-lib, …) substitutes only with both.
const (
	upstreamURL = "https://cache.nixos.org"
	upstreamKey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
)

// copyFrom substitutes paths and their closures into the local store with bob's client
// configuration (SPEC §6.6.5): the cache at url first, then cache.nixos.org, trusting keys and
// the cache.nixos.org key. netrc may be empty (anonymous). A path that is in neither cache, or
// whose signature does not verify, fails the substitution. Nix's narinfo disk cache is off in
// both directions: the client's home is shared by every local store of a test, and a narinfo
// fetched with a token must not answer a later anonymous or other-project lookup.
func (l local) copyFrom(url, netrc string, keys []string, paths ...string) harness.Result {
	args := []string{"build", "--no-link", "--store", l.store(), "--max-jobs", "0",
		"--option", "substituters", url + " " + upstreamURL,
		"--option", "trusted-public-keys", strings.Join(append(append([]string{}, keys...), upstreamKey), " "),
		"--option", "narinfo-cache-negative-ttl", "0", "--option", "narinfo-cache-positive-ttl", "0"}
	if netrc != "" {
		args = append(args, "--option", "netrc-file", netrc)
	}
	return l.run(append(args, paths...)...)
}

// verify runs `nix store verify` for paths with exactly the given trusted keys.
func (l local) verify(keys []string, paths ...string) harness.Result {
	args := []string{"store", "verify", "--store", l.store(), "--trusted-public-keys", strings.Join(keys, " ")}
	return l.run(append(args, paths...)...)
}

// closure lists the bob-built part of the closure of path in the local store: every path but
// those substituted from cache.nixos.org (signed by its key), which must verify against that key
// alone (R-REG-005: bob does not serve them).
func (l local) closure(t testing.TB, path string) []string {
	t.Helper()
	r := l.run("path-info", "--recursive", "--json", "--store", l.store(), path)
	if r.Exit != 0 {
		t.Fatalf("nix path-info --recursive %s: exit %d: %s", path, r.Exit, r.Combined())
	}
	var infos map[string]struct {
		Signatures []string `json:"signatures"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &infos); err != nil {
		t.Fatalf("nix path-info --json %s: %v: %s", path, err, r.Stdout)
	}
	var bob, upstream []string
	for p, info := range infos {
		if slices.ContainsFunc(info.Signatures, func(s string) bool { return strings.HasPrefix(s, "cache.nixos.org-1:") }) {
			upstream = append(upstream, p)
		} else {
			bob = append(bob, p)
		}
	}
	slices.Sort(bob)
	if len(upstream) > 0 {
		if v := l.verify([]string{upstreamKey}, upstream...); v.Exit != 0 {
			t.Errorf("nix store verify of the cache.nixos.org paths of %s: exit %d: %s", path, v.Exit, v.Combined())
		}
	}
	return bob
}

// roots returns the closure roots of the first platform of rel.
func roots(t testing.TB, rel regenv.Release) []string {
	t.Helper()
	if len(rel.Closures) == 0 || len(rel.Closures[0].Roots) == 0 {
		t.Fatalf("release %s has no closure roots", rel.ID)
	}
	return rel.Closures[0].Roots
}

// fixtureDir is a committed checkout of fixture acme (it defines the release "api").
func fixtureDir(t testing.TB) string { return harness.Fixture(t, "acme") }

// nondetDir is a committed checkout of fixture "nondet": one package whose output contains a
// per-build nonce, so two builds of the same inputs give different bytes under the same path
// (needs fixture tests/fixtures/repos/nondet; owner: fixtures).
func nondetDir(t testing.TB) string { return harness.Fixture(t, "nondet") }
