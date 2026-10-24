package gen

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
	"github.com/megakuul/bob/pkg/npm/resolve"
)

// importer is one npm importer of the graph (npm.lib, npm.app, npm.site): the first node of
// its label (the spec does not depend on the configuration).
type importer struct {
	node *ir.Node
	dir  string // repo-relative, "." for the root
	spec npmspec.Importer
}

// labelDir returns the directory of a label of this repository ("//web:web" → "web",
// "//:x" → "."); ok is false for labels of dependency repositories.
func labelDir(label string) (string, bool) {
	rest, ok := strings.CutPrefix(label, "//")
	if !ok {
		return "", false
	}
	dir, _, _ := strings.Cut(rest, ":")
	if dir == "" {
		return ".", true
	}
	return dir, true
}

func isImporterKind(kind string) bool {
	return kind == npmspec.KindLib || kind == npmspec.KindApp || kind == npmspec.KindSite
}

// importers reads the npm importers of g, sorted by directory, and the index of each node id.
func importers(g *ir.Graph) ([]*importer, map[string]*importer, error) {
	byLabel := map[string]*importer{}
	byID := map[string]*importer{}
	for _, n := range g.Nodes {
		if !isImporterKind(n.Kind) {
			continue
		}
		dir, ok := labelDir(n.Label)
		if !ok {
			continue // a dependency repository's importer keeps its own lock
		}
		if im, ok := byLabel[n.Label]; ok {
			byID[n.ID] = im
			continue
		}
		var s npmspec.Importer
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return nil, nil, fmt.Errorf("%s: npm spec: %w", n.Label, err)
		}
		im := &importer{node: n, dir: dir, spec: s}
		byLabel[n.Label] = im
		byID[n.ID] = im
	}
	out := make([]*importer, 0, len(byLabel))
	for _, im := range byLabel {
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].dir < out[j].dir || out[i].dir == out[j].dir && out[i].node.Label < out[j].node.Label
	})
	return out, byID, nil
}

// depImporter resolves a dep index of n to the importer it names.
func depImporter(n *ir.Node, i int, byID map[string]*importer) (*importer, error) {
	if i < 0 || i >= len(n.Deps) {
		return nil, fmt.Errorf("%s: dependency index %d out of range", n.Label, i)
	}
	im, ok := byID[n.Deps[i]]
	if !ok || im.node.Kind != npmspec.KindLib {
		return nil, fmt.Errorf("%s: dependency %s is not an npm library of this repository", n.Label, n.Deps[i])
	}
	return im, nil
}

func resolveDep(d npmspec.Dep) resolve.Dep {
	return resolve.Dep{Alias: d.Alias, Name: d.Name, Req: d.Req, Dev: d.Dev, Optional: d.Optional}
}

// resolveInput is the resolver's view of the importers: every Dep, peers of libraries, local
// libraries as links, and the npm.Extend options of every importer (deduplicated).
func resolveInput(ims []*importer, byID map[string]*importer) (resolve.Input, error) {
	var in resolve.Input
	ext := map[string]*resolve.Extension{}
	var extOrder []string
	for _, im := range ims {
		ri := resolve.Importer{Dir: im.dir}
		if im.node.Kind == npmspec.KindLib {
			ri.Name = im.spec.Name
		}
		for _, d := range im.spec.Deps {
			if d.Link >= 0 && d.Alias == "" {
				lib, err := depImporter(im.node, d.Link, byID)
				if err != nil {
					return in, err
				}
				ri.Deps = append(ri.Deps, resolve.Dep{Alias: lib.spec.Name, Link: lib.dir})
				continue
			}
			ri.Deps = append(ri.Deps, resolveDep(d))
		}
		for _, d := range im.spec.Peers {
			ri.Peers = append(ri.Peers, resolveDep(d))
		}
		in.Importers = append(in.Importers, ri)
		for _, x := range im.spec.Options.Extend {
			e := ext[x.Package]
			if e == nil {
				e = &resolve.Extension{Package: x.Package}
				ext[x.Package] = e
				extOrder = append(extOrder, x.Package)
			}
			for _, d := range x.Deps {
				if !hasDep(e.Deps, d.Alias) {
					e.Deps = append(e.Deps, resolveDep(d))
				}
			}
			for _, p := range x.Peers {
				if !hasString(e.Peers, p) {
					e.Peers = append(e.Peers, p)
				}
			}
		}
	}
	for _, p := range extOrder {
		in.Extend = append(in.Extend, *ext[p])
	}
	return in, nil
}

func hasDep(ds []resolve.Dep, alias string) bool {
	for _, d := range ds {
		if d.Alias == alias {
			return true
		}
	}
	return false
}

func hasString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// relDir is the relative path from directory from to directory to (both repo-relative).
func relDir(from, to string) string {
	f := splitDir(from)
	t := splitDir(to)
	i := 0
	for i < len(f) && i < len(t) && f[i] == t[i] {
		i++
	}
	var parts []string
	for range f[i:] {
		parts = append(parts, "..")
	}
	parts = append(parts, t[i:]...)
	if len(parts) == 0 {
		return "."
	}
	return path.Join(parts...)
}

func splitDir(d string) []string {
	if d == "." || d == "" {
		return nil
	}
	return strings.Split(d, "/")
}
