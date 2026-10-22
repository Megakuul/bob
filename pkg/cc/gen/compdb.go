package gen

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// compileCommand is one entry of compile_commands.json (§03.1.4).
type compileCommand struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments"`
	Output    string   `json:"output"`
}

// compileCommands returns compile_commands.json: one entry per source file of every C/C++
// library, program and test, for the host platform and the default variant. Own sources and
// headers point into the working tree, relative to the repository root; dependency headers
// point into the store (clangd runs through .bob/bin/clangd, where store paths resolve).
// Without a repository root (the request's Root) there is nothing to point at, and without
// the nixpkgs index the compiler is named without its store path.
func compileCommands(ctx context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	if req.Root == "" {
		return nil, nil
	}
	pl := newPlanner(ctx, req)
	nodes := map[string]*ir.Node{} // label → first node (by id)
	for _, n := range req.Graph.Nodes {
		switch n.Kind {
		case ccspec.KindLibrary, ccspec.KindBinary, ccspec.KindTest:
		default:
			continue
		}
		if !strings.HasPrefix(n.Label, "//") {
			continue
		}
		if old, ok := nodes[n.Label]; !ok || n.ID < old.ID {
			nodes[n.Label] = n
		}
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	paths, err := pl.nixpkgsPaths(ctx, nodes)
	if err != nil {
		return nil, err
	}
	var entries []compileCommand
	labels := make([]string, 0, len(nodes))
	for l := range nodes {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		n := nodes[l]
		var t ccspec.Target
		if err := json.Unmarshal(n.Spec, &t); err != nil {
			return nil, internal(n, "spec: %v", err)
		}
		c, err := configOf(n)
		if err != nil {
			return nil, internal(n, "%v", err)
		}
		files, err := pl.files(n)
		if err != nil {
			return nil, err
		}
		hdeps, err := pl.headerDeps(n, t)
		if err != nil {
			return nil, err
		}
		var deps []string
		for _, d := range hdeps {
			deps = append(deps, workingIncludes(d, paths)...)
		}
		own := ownIncludes(n, t)
		for _, f := range files {
			if !isSource(f) {
				continue
			}
			lang := language(f)
			cc := compiler(lang)
			if p := paths["gcc"]; p != "" {
				cc = p + "/bin/" + cc
			}
			argv := append([]string{cc}, compileArgs(t, c, f, "", own, deps)...)
			argv = append(argv, "-c", f, "-o", "/dev/null")
			entries = append(entries, compileCommand{Directory: req.Root, File: f, Arguments: argv, Output: "/dev/null"})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, err
	}
	return []engine.GeneratedFile{{Path: "compile_commands.json", Content: append(b, '\n'), Mode: 0o444,
		Owner: labels[0]}}, nil
}

// nixpkgsPaths looks up the store paths of gcc and of every nixpkgs library the nodes use:
// attr → its dev output (or out); "gcc" → the cc-wrapper.
func (pl *planner) nixpkgsPaths(ctx context.Context, nodes map[string]*ir.Node) (map[string]string, error) {
	out := map[string]string{}
	if pl.req.Nixpkgs == nil {
		return out, nil
	}
	attrs := map[string]bool{"gcc": true}
	for _, n := range nodes {
		var t ccspec.Target
		if json.Unmarshal(n.Spec, &t) != nil {
			continue
		}
		ls, err := pl.headerDeps(n, t)
		if err != nil {
			return nil, err
		}
		for _, l := range ls {
			if l.kind == libNixpkgs && l.n.Nix != nil {
				attrs[l.n.Nix.Attr] = true
			}
		}
	}
	list := make([]string, 0, len(attrs))
	for a := range attrs {
		list = append(list, a)
	}
	sort.Strings(list)
	entries, err := pl.req.Nixpkgs.Lookup(ctx, list)
	if err != nil {
		return nil, err
	}
	for a, e := range entries {
		if o := pickOutput(e.Outputs, "dev", "out"); a != "gcc" && o != "" {
			out[a] = e.Outputs[o]
		}
		if a == "gcc" {
			out[a] = e.Outputs[pickOutput(e.Outputs, "out")]
		}
	}
	return out, nil
}

// workingIncludes are the -I of a dependency in compile_commands.json: bob libraries' public
// headers in the working tree, nixpkgs libraries' include dir in the store. An External's
// headers exist only once it is built and are left out.
func workingIncludes(l *lib, paths map[string]string) []string {
	switch l.kind {
	case libStatic, libShared:
		if l.t.Hdrs != "" && strings.HasPrefix(l.n.Label, "//") {
			return []string{"-I" + joinDir(labelDir(l.n.Label), l.t.Hdrs)}
		}
	case libNixpkgs:
		if l.n.Nix != nil && paths[l.n.Nix.Attr] != "" {
			return []string{"-I" + paths[l.n.Nix.Attr] + "/include"}
		}
	}
	return nil
}
