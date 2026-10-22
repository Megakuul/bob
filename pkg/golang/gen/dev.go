package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// PrepareDev is the Go share of a dev shell (SPEC §02.7.2, engine.DevPreparer): the toolchain
// of the repository's modules and gopls on PATH, gopls with GOFLAGS=-tags=bob (S1-7), GOROOT
// and GOTOOLCHAIN=local inside, and CGO_ENABLED=0: cgo packages take their C libraries from
// bob builds (Module.Cgo), which the shell's go cannot link, so go and gopls in the shell work
// on the pure-Go view and cgo builds go through bob build/test. go.mod and go.sum are the
// Native files the engine writes anyway.
func (Ecosystem) PrepareDev(ctx context.Context, req engine.DevRequest) (prep engine.DevPrep, err error) {
	defer func() { // a bug here must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("go dev shell: internal error: %v", v)
		}
	}()
	var mods []*struct {
		label string
		spec  gospec.Module
		tc    engine.Resolved
	}
	for _, n := range req.Nodes {
		if n.Kind != gospec.KindModule || !strings.HasPrefix(n.Label, "//") {
			continue
		}
		var s gospec.Module
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return prep, fmt.Errorf("module spec of %s: %w", n.Label, err)
		}
		if s.Toolchain < 0 || s.Toolchain >= len(n.Deps) {
			return prep, fmt.Errorf("module %s has no toolchain", n.Label)
		}
		rv, ok := req.Inputs[n.Deps[s.Toolchain]]
		if !ok || rv.Outputs["out"] == "" {
			return prep, fmt.Errorf("toolchain of %s is not resolved", n.Label)
		}
		mods = append(mods, &struct {
			label string
			spec  gospec.Module
			tc    engine.Resolved
		}{n.Label, s, rv})
	}
	if len(mods) == 0 {
		return prep, nil
	}
	// The root module's toolchain is the shell's go (the shortest label sorts first: "//:x").
	sort.Slice(mods, func(i, j int) bool {
		di, dj := labelDir(mods[i].label), labelDir(mods[j].label)
		if len(di) != len(dj) {
			return len(di) < len(dj)
		}
		return di < dj
	})
	tc := mods[0].tc
	out := tc.Outputs["out"]
	version := ""
	if m := goStoreName.FindStringSubmatch(out); m != nil {
		version = m[1]
	}
	prep.Packages = append(prep.Packages, engine.DevPackage{Name: "go", Version: version, Path: out, Drv: tc.Drv})
	if req.Nixpkgs != nil {
		entries, err := req.Nixpkgs.Lookup(ctx, []string{"gopls"})
		if err != nil {
			return prep, err
		}
		if e, ok := entries["gopls"]; ok && e.DrvPath != "" {
			p := e.Outputs["out"]
			if b, ok := e.Outputs["bin"]; ok {
				p = b
			}
			prep.Packages = append(prep.Packages, engine.DevPackage{Name: "gopls", Version: e.Version, Path: p, Drv: e.DrvPath})
		}
	}
	prep.Env = map[string]string{"CGO_ENABLED": "0"}
	prep.InsideEnv = map[string]string{"GOROOT": out + "/share/go", "GOTOOLCHAIN": "local"}
	// §03.2.5: the go command of the shell reads modules only from bob's GOPROXY directory,
	// filled from bob.lock (a fresh clone downloads what is missing once).
	if dir := ProxyDir(); dir != "" {
		if err := fillProxyDir(ctx, req.Lock, dir); err != nil {
			return prep, err
		}
		prep.InsideEnv["GOPROXY"] = "file://" + dir
		prep.InsideEnv["GOFLAGS"] = "-mod=readonly"
		prep.InsideEnv["GOSUMDB"] = "off"
	}
	prep.ToolEnv = map[string]map[string]string{"gopls": {"GOFLAGS": "-tags=bob"}}
	return prep, nil
}

var _ engine.DevPreparer = Ecosystem{}

// fillProxyDir makes dir hold every module of the go section in GOPROXY layout: the .mod of
// each, the .zip of those that provide packages (resolution keeps them there; a fresh clone
// fetches the missing ones from the proxy bob reads from).
func fillProxyDir(ctx context.Context, section []byte, dir string) error {
	if len(section) == 0 {
		return nil
	}
	v, err := lock.DecodeSection(lock.SectionGo, section)
	if err != nil {
		return diag.New(1019, "reason", "go section: "+err.Error(), "line", "0")
	}
	sec := v.(*lock.GoSection)
	p, err := resolve.NewProxy(readProxy(sec.Proxy))
	if err != nil {
		return err
	}
	p.WithCache(dir)
	tmp, err := os.MkdirTemp("", "bob-goproxy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, m := range sec.Modules {
		rm := resolve.Module{Path: m.Path, Version: m.Version}
		if _, err := p.Mod(ctx, rm); err != nil {
			return fmt.Errorf("go module %s: %w", rm, err)
		}
		if m.Packages {
			f, temp, err := p.ZipFile(ctx, rm, tmp)
			if err != nil {
				return fmt.Errorf("go module %s: %w", rm, err)
			}
			if temp {
				_ = os.Remove(f)
			}
		}
	}
	return nil
}
