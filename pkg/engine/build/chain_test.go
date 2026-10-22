package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/drv"
)

// chainOf walks the input derivations of the direct nixpkgs inputs to the failing one.
func TestChainOf(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "nix", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	known := modMap{}
	mk := func(name string, inputs ...string) string {
		d := &drv.Derivation{Name: name, System: "aarch64-linux", Builder: "/bin/sh",
			Outputs: []drv.Output{{Name: "out"}}, Env: map[string]string{"name": name}}
		for _, in := range inputs {
			d.InputDrvs = append(d.InputDrvs, drv.InputDrv{Path: in, Outputs: []string{"out"}})
		}
		if err := d.Finalize(known); err != nil {
			t.Fatal(err)
		}
		p, err := d.Path()
		if err != nil {
			t.Fatal(err)
		}
		a, err := d.ATerm()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, p), a, 0o644); err != nil {
			t.Fatal(err)
		}
		known[p] = d
		return p
	}
	a := mk("a")
	b := mk("b", a)
	other := mk("other")
	c := mk("c", other, b)
	got := chainOf(home, []string{c}, a)
	if strings.Join(got, " ") != strings.Join([]string{c, b, a}, " ") {
		t.Errorf("chain %v, want c → b → a", got)
	}
	if got := chainOf(home, []string{other}, a); got != nil {
		t.Errorf("unreachable target: %v", got)
	}
}
