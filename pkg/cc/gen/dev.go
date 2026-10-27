package gen

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nixidx"
)

// devTools are the nixpkgs attributes of the C/C++ share of a dev shell: the compiler (the
// cc-wrapper bob builds with, gcc/g++/cc/c++) and clangd and clang-format for editors that
// read compile_commands.json.
var devTools = []string{"gcc", "clang-tools"}

// PrepareDev is the C/C++ share of a dev shell (SPEC §02.7.0: "the toolchains of every
// ecosystem used in the repo (Go, Rust, C/C++, Node, OCaml at the pins of §03) … are added
// automatically", engine.DevPreparer): GCC of the nixpkgs pin and the clang tools on PATH.
// compile_commands.json is the Native file the engine writes anyway. Before A459 the C/C++
// ecosystem contributed nothing, so a C/C++ project's dev shell had no compiler.
func (Ecosystem) PrepareDev(ctx context.Context, req engine.DevRequest) (prep engine.DevPrep, err error) {
	defer func() { // a bug here must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("cc dev shell: internal error: %v", v)
		}
	}()
	if req.Nixpkgs == nil {
		return prep, nil
	}
	entries, err := req.Nixpkgs.Lookup(ctx, devTools)
	if err != nil {
		return prep, err
	}
	prep.Packages = devPackages(entries)
	prep.Realise = includeDrvs(req)
	return prep, nil
}

// includeDrvs are the nixpkgs derivations the C/C++ packages depend on directly (libraries such
// as nixpkgs.Openssl.Lib(p)): compile_commands.json names their dev outputs' include
// directories, so clangd needs them in bob's store after bob gen/bob env (R-CC-003a), not only
// after a build that happened to realise them.
func includeDrvs(req engine.DevRequest) []string {
	if req.Graph == nil {
		return nil
	}
	byID := map[string]*ir.Node{}
	for _, n := range req.Graph.Nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range req.Nodes {
		for _, d := range n.Deps {
			dn := byID[d]
			if dn == nil || !strings.HasPrefix(dn.Kind, "nixpkgs.") {
				continue
			}
			if r, ok := req.Inputs[d]; ok && r.Drv != "" && !seen[r.Drv] {
				seen[r.Drv] = true
				out = append(out, r.Drv)
			}
		}
	}
	sort.Strings(out)
	return out
}

// devPackages are the dev shell packages of the looked-up devTools, in devTools order; a tool
// the pin does not have is left out.
func devPackages(entries map[string]nixidx.Entry) []engine.DevPackage {
	var out []engine.DevPackage
	for _, a := range devTools {
		e, ok := entries[a]
		if !ok || e.Missing || e.DrvPath == "" {
			continue
		}
		if p := e.Outputs[engine.PickOutput(e.Outputs, "bin", "out")]; p != "" {
			out = append(out, engine.DevPackage{Name: a, Version: e.Version, Path: p, Drv: e.DrvPath})
		}
	}
	return out
}

var _ engine.DevPreparer = Ecosystem{}
