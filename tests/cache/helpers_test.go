package cache

import (
	"fmt"
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

// copyFrom substitutes paths from the cache at url into the local store, checking signatures
// against keys. netrc may be empty (anonymous).
func (l local) copyFrom(url, netrc string, keys []string, paths ...string) harness.Result {
	args := []string{"copy", "--from", url, "--to", l.store(),
		"--option", "trusted-public-keys", strings.Join(keys, " "),
		"--option", "narinfo-cache-negative-ttl", "0"}
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

// closure lists the closure of path in the local store.
func (l local) closure(t testing.TB, path string) []string {
	t.Helper()
	r := l.run("path-info", "--recursive", "--store", l.store(), path)
	if r.Exit != 0 {
		t.Fatalf("nix path-info --recursive %s: exit %d: %s", path, r.Exit, r.Combined())
	}
	return strings.Fields(r.Stdout)
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
