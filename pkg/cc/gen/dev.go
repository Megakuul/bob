package gen

import (
	"context"
	"fmt"

	"github.com/megakuul/bob/pkg/engine"
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
	return prep, nil
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
