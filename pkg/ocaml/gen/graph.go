package gen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
)

// target is one OCaml node of the graph with its spec.
type target struct {
	n    *ir.Node
	spec mlspec.Target
}

func isOCaml(kind string) bool {
	return kind == mlspec.KindLibrary || kind == mlspec.KindExecutable || kind == mlspec.KindTest
}

// targets lists the OCaml nodes of g, sorted by id.
func targets(g *ir.Graph) ([]*target, error) {
	var out []*target
	for _, n := range g.Nodes {
		if !isOCaml(n.Kind) {
			continue
		}
		t, err := targetOf(n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n.ID < out[j].n.ID })
	return out, nil
}

func targetOf(n *ir.Node) (*target, error) {
	t := &target{n: n}
	if err := json.Unmarshal(n.Spec, &t.spec); err != nil {
		return nil, fmt.Errorf("%s: OCaml spec: %w", n.Label, err)
	}
	for _, i := range append(append([]int{}, t.spec.Local...), t.spec.Native...) {
		if i < 0 || i >= len(n.Deps) {
			return nil, fmt.Errorf("%s: dep index %d out of range", n.Label, i)
		}
	}
	for _, i := range t.spec.Tools {
		if i < 0 || i >= len(n.Deps) {
			return nil, fmt.Errorf("%s: tool index %d out of range", n.Label, i)
		}
	}
	for _, c := range t.spec.Conf {
		for _, i := range c.Libs {
			if i < 0 || i >= len(n.Deps) {
				return nil, fmt.Errorf("%s: conf index %d out of range", n.Label, i)
			}
		}
	}
	return t, nil
}

// request is one opam requirement of the repository: the package and the version formula
// (what bob lock resolves; the findlib library does not matter for resolution).
type request struct {
	Name string `json:"name"`
	Req  string `json:"req"`
}

// requests are the sorted, unique opam requirements of the targets, with the first target
// that declared each (for positions in errors).
func requests(ts []*target) ([]request, map[request]*ir.Node) {
	first := map[request]*ir.Node{}
	var out []request
	for _, t := range ts {
		for _, o := range t.spec.Opam {
			r := request{Name: o.Name, Req: o.Req}
			if _, ok := first[r]; ok {
				continue
			}
			first[r] = t.n
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Req < out[j].Req
	})
	return out, first
}

// labelDir is the repository-relative directory of a label ("//geom:geom" → "geom").
func labelDir(label string) string {
	d, _, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")
	return d
}
