package build

import (
	"context"
	"os/exec"
	"path"

	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
)

// evalGoRoot is the Go toolchain of the evaluator (R-FW-043.5, ADR 0003): Options.GoRoot;
// else the go on PATH (the IR does not depend on the Go version, and the eval key records it);
// else nixpkgs' go at bob.lock's pin, or at the default channel's current revision before
// bob.lock has a pin (r.transientPin, which the lock stage then records).
func (r *runner) evalGoRoot(ctx context.Context, st *store.Store, f fetch.Fetcher) (string, error) {
	if r.o.GoRoot != "" {
		return r.o.GoRoot, nil
	}
	if _, err := exec.LookPath("go"); err == nil {
		return "", nil
	}
	lf, err := readLock(r.o.Root)
	if err != nil {
		return "", err
	}
	pin := lf.Nixpkgs
	if pin.Rev == "" {
		if pin, err = lock.PinNixpkgs(ctx, f, lock.DefaultChannel, lock.NixpkgsPin{}, false, nil); err != nil {
			return "", err
		}
		r.transientPin = pin
	}
	return nixpkgsGo(ctx, st, f, pin)
}

// nixpkgsGo realises nixpkgs' go at pin and returns its GOROOT as the host sees it.
func nixpkgsGo(ctx context.Context, st *store.Store, f fetch.Fetcher, pin lock.NixpkgsPin) (string, error) {
	src, err := f.Ensure(ctx, lock.NixpkgsRequest(pin))
	if err != nil {
		return "", err
	}
	idx, err := nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: pin.Rev, System: store.HostSystem()})
	if err != nil {
		return "", err
	}
	entries, err := idx.Lookup(ctx, []string{"go"})
	if err != nil {
		return "", err
	}
	e := entries["go"]
	out, ok := e.Outputs["out"]
	if e.DrvPath == "" || !ok {
		return "", internalf("nixpkgs:go", "nixpkgs at %s has no go", pin.Rev)
	}
	if missing, err := st.Invalid(ctx, []string{out}); err != nil {
		return "", err
	} else if len(missing) > 0 {
		c := st.Command(ctx, "nix-store", "--realise", e.DrvPath)
		if err := c.Run(); err != nil {
			return "", internalf("nixpkgs:go", "realising go: %v", err)
		}
	}
	return path.Join(hostPath(st, out), "share", "go"), nil
}
