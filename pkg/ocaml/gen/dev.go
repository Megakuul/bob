package gen

import (
	"context"
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
)

// devTools are the nixpkgs editor tools of the OCaml share of a dev shell (S5 §3.5): the
// language server, merlin and the .merlin reader, all built for the pin's OCaml.
var devTools = []string{"ocamlPackages.ocaml-lsp", "ocamlPackages.merlin", "ocamlPackages.dot-merlin-reader"}

// PrepareDev is the OCaml share of a dev shell (SPEC §03.5.4, engine.DevPreparer): the
// toolchain (ocaml, ocamlfind, dune) and the editor tools on PATH, and OCAMLPATH inside bob's
// namespace pointing at the opam packages and local libraries of the repository in the store,
// which the generated .merlin files name as PKG. The packages and libraries are listed as dev
// packages, so the shell realises (builds) them.
//
// Planning them needs a full plan context (Builder, Sources, Resolver); when the engine gives
// none, the shell has the tools and the toolchain's own libraries only.
func (Ecosystem) PrepareDev(ctx context.Context, req engine.DevRequest) (prep engine.DevPrep, err error) {
	defer func() { // a bug here must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("ocaml dev shell: internal error: %v", v)
		}
	}()
	ts, err := targets(req.Graph)
	if err != nil {
		return prep, err
	}
	var own []*target
	for _, t := range ts {
		if strings.HasPrefix(t.n.Label, "//") {
			own = append(own, t)
		}
	}
	if len(own) == 0 {
		return prep, nil
	}
	t0 := own[0]
	roots := map[string]string{}
	for _, a := range []string{mlspec.AttrOCaml, mlspec.AttrFindlib, mlspec.AttrDune} {
		rv, ok := req.Inputs[t0.n.Deps[t0.spec.Tools[a]]]
		if !ok {
			return prep, fmt.Errorf("the OCaml toolchain (%s) of %s is not resolved", a, t0.n.Label)
		}
		o := engine.PickOutput(rv.Outputs, "bin", "out")
		roots[a] = rv.Outputs[o]
		prep.Packages = append(prep.Packages, engine.DevPackage{Name: a, Path: rv.Outputs[o], Drv: rv.Drv})
	}
	if req.Nixpkgs != nil {
		entries, err := req.Nixpkgs.Lookup(ctx, devTools)
		if err != nil {
			return prep, err
		}
		for _, a := range devTools {
			e, ok := entries[a]
			if !ok || e.DrvPath == "" {
				continue
			}
			p := e.Outputs[engine.PickOutput(e.Outputs, "bin", "out")]
			prep.Packages = append(prep.Packages, engine.DevPackage{Name: a, Version: e.Version, Path: p, Drv: e.DrvPath})
		}
	}
	siteLib := roots[mlspec.AttrFindlib] + "/lib/ocaml/" + OCamlVersion + "/site-lib"
	var ocamlpath, stublibs []string
	if req.Builder != "" && req.Resolver != nil && len(req.Sources) > 0 {
		plReq := req.PlanRequest
		plReq.Nodes = nil
		for _, t := range own {
			plReq.Nodes = append(plReq.Nodes, t.n)
		}
		pl, err := newPlanner(ctx, plReq)
		if err != nil {
			return prep, err
		}
		res, err := pl.plan()
		if err != nil {
			return prep, err
		}
		for _, name := range sortedKeys(pl.opam) {
			o := pl.opam[name]
			prep.Packages = append(prep.Packages, engine.DevPackage{Name: "opam:" + name, Version: o.version, Path: o.out, Drv: o.drv})
			ocamlpath = append(ocamlpath, o.out+"/lib")
			stublibs = append(stublibs, o.out+"/lib/stublibs")
		}
		for _, t := range own {
			if t.spec.Type != mlspec.TypeLibrary {
				continue
			}
			rv := res.Nodes[t.n.ID]
			prep.Packages = append(prep.Packages, engine.DevPackage{Name: t.n.Label, Path: rv.Outputs["out"], Drv: rv.Drv})
			ocamlpath = append(ocamlpath, rv.Outputs["out"]+"/lib")
		}
	}
	prep.InsideEnv = map[string]string{
		"OCAMLPATH":            strings.Join(append(ocamlpath, siteLib), ":"),
		"CAML_LD_LIBRARY_PATH": strings.Join(append(stublibs, roots[mlspec.AttrOCaml]+"/lib/ocaml/stublibs"), ":"),
	}
	return prep, nil
}
