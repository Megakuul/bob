package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
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
	prep.ToolEnv = map[string]map[string]string{"gopls": {"GOFLAGS": "-tags=bob"}}
	return prep, nil
}

var _ engine.DevPreparer = Ecosystem{}
