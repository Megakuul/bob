package gen

import (
	"context"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

// TestLockUpdateAll (A457, R-CLI-010 "re-resolves the named (or all) entries"): `bob lock
// --update` without names moves the Go modules like naming each of them does, to the newest
// release. Before, it only dropped the pins: a module an earlier `--update <module>` had moved
// went back to its bob.go version.
func TestLockUpdateAll(t *testing.T) {
	proxy := t.TempDir()
	proxyModule(t, proxy, "golang.org/x/net", "v0.1.0", "module golang.org/x/net\n\ngo 1.20\n", map[string]string{"net.go": "package net\n"})
	t.Setenv(proxyEnv, "file://"+proxy)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	writeTree(t, root, map[string]string{"tools/main.go": "package main\n\nimport _ \"golang.org/x/net\"\n\nfunc main() {}\n"})
	g := moduleGraph(t, gospec.Requirement{Path: "golang.org/x/net", Version: "v0.1.0"})
	lockWith := func(prev []byte, update []string) []byte {
		t.Helper()
		raw, err := Ecosystem{}.Lock(context.Background(), engine.LockRequest{Graph: g, Root: root, Prev: prev, Update: update})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	buildList := func(raw []byte) string {
		t.Helper()
		v, err := lock.DecodeSection(lock.SectionGo, raw)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(v.(*lock.GoSection).Workspaces["example.com/tools"].Modules, " ")
	}
	first := lockWith(nil, nil)
	proxyModule(t, proxy, "golang.org/x/net", "v0.2.0", "module golang.org/x/net\n\ngo 1.20\n", map[string]string{"net.go": "package net\n"})
	if got := buildList(lockWith(first, nil)); got != "golang.org/x/net@v0.1.0" {
		t.Errorf("plain lock moved a pin: %s", got)
	}
	named := lockWith(first, []string{"golang.org/x/net"})
	if got := buildList(named); got != "golang.org/x/net@v0.2.0" {
		t.Errorf("--update golang.org/x/net: %s", got)
	}
	if got := buildList(lockWith(named, []string{})); got != "golang.org/x/net@v0.2.0" {
		t.Errorf("--update after --update golang.org/x/net: %s, want v0.2.0 (not back to bob.go's v0.1.0)", got)
	}
	if got := buildList(lockWith(first, []string{})); got != "golang.org/x/net@v0.2.0" {
		t.Errorf("--update: %s, want the newest release", got)
	}
}
