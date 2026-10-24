package outputs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/tests/harness"
)

// bundleDrv is one derivation of the bundle builders (bob-builder oci-layer, oci-image, appimage,
// tarball, release).
type bundleDrv struct {
	File string // store file name
	Cmd  string // builder subcommand
	D    *drv.Derivation
}

// bundleSubcommands are the bob-builder subcommands of §04 outputs (R-OUT-006).
var bundleSubcommands = []string{"oci-layer", "oci-image", "appimage", "tarball"}

// bundleDrvs parses every .drv of the world's store and keeps the ones built by a bundle
// subcommand. ok is false when drv.Parse is not implemented (the test is then red).
func (w *world) bundleDrvs() ([]bundleDrv, bool) {
	w.t.Helper()
	store := filepath.Join(w.home, "nix", "store")
	ents, err := os.ReadDir(store)
	if err != nil {
		w.t.Fatalf("bob home has no store: %v", err)
	}
	var out []bundleDrv
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".drv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			w.t.Fatalf("read %s: %v", e.Name(), err)
		}
		d, err := drv.Parse(b)
		if err != nil {
			if strings.Contains(err.Error(), harness.NotImplemented) || isNotImpl(err) {
				harness.RedFeature(w.t, "drv.Parse")
				return nil, false
			}
			w.t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if !strings.HasSuffix(d.Builder, "/bin/bob-builder") {
			continue
		}
		for _, a := range d.Args {
			for _, s := range bundleSubcommands {
				if a == s {
					out = append(out, bundleDrv{File: e.Name(), Cmd: s, D: d})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, true
}

func isNotImpl(err error) bool {
	d, ok := err.(*diag.Error)
	return ok && d.Code == diag.CodeNotImplemented
}

// bundleDrvsOf returns the derivations with the given subcommand.
func bundleDrvsOf(all []bundleDrv, cmd string) []bundleDrv {
	var out []bundleDrv
	for _, d := range all {
		if d.Cmd == cmd {
			out = append(out, d)
		}
	}
	return out
}
