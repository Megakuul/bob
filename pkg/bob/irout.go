package bob

import (
	"bytes"
	"encoding/json"
	"sort"
)

// The structs below are the IR v1 document as the evaluator writes it (SPEC §1.6). Package
// pkg/engine/ir reads it; it cannot be imported here (L0 must not import the engine).
type irDoc struct {
	IRVersion  int            `json:"irVersion"`
	BobVersion string         `json:"bobVersion"`
	Module     string         `json:"module"`
	Repo       irRepo         `json:"repo"`
	Configs    []irConfig     `json:"configs"`
	Nodes      []irNode       `json:"nodes"`
	Roots      []string       `json:"roots"`
	Pipelines  []pipelineJSON `json:"pipelines"`
}

type irRepo struct {
	Nixpkgs  string      `json:"nixpkgs"`
	Variants []irVariant `json:"variants"`
}

// irVariant is a named variant applied to the zero variant.
type irVariant struct {
	Name       string              `json:"name"`
	Profile    Profile             `json:"profile"`
	Sanitizers []Sanitizer         `json:"sanitizers"`
	Race       bool                `json:"race"`
	Features   map[string][]string `json:"features"`
}

type irConfig struct {
	ID         string              `json:"id"`
	Platform   Platform            `json:"platform"`
	Profile    Profile             `json:"profile"`
	Sanitizers []Sanitizer         `json:"sanitizers"`
	Race       bool                `json:"race"`
	Features   map[string][]string `json:"features"`
	Named      []string            `json:"named"`
}

type irNode struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Label     string          `json:"label"`
	DefinedAt irPos           `json:"definedAt"`
	Proj      map[string]any  `json:"proj"`
	Deps      []string        `json:"deps"`
	Srcs      []irSrcs        `json:"srcs"`
	Spec      json.RawMessage `json:"spec"`
	Nix       json.RawMessage `json:"nix,omitempty"`
}

type irPos struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
}

type irSrcs struct {
	Dir      string   `json:"dir"`
	Patterns []string `json:"patterns"`
}

// encode writes the canonical IR of the nodes reachable from roots.
func (ev *evaluation) encode(repo RepoOpts, pipes []pipelineJSON, targets []target, roots []*node) ([]byte, error) {
	doc := irDoc{IRVersion: irVersion, BobVersion: ev.env.bobVersion, Module: ev.env.mainModule,
		Repo:      irRepo{Nixpkgs: repo.Nixpkgs, Variants: []irVariant{}},
		Configs:   []irConfig{},
		Nodes:     []irNode{},
		Roots:     nonNil(sortedIDs(roots)),
		Pipelines: nonNil(pipes),
	}
	for _, n := range repo.Variants {
		v := Variant{}
		n.apply(&v)
		v = v.normal()
		doc.Repo.Variants = append(doc.Repo.Variants, irVariant{Name: n.name, Profile: v.Profile,
			Sanitizers: nonNil(v.Sanitize), Race: v.Race, Features: featureMap(v.Features)})
	}
	sort.Slice(doc.Repo.Variants, func(i, j int) bool { return doc.Repo.Variants[i].Name < doc.Repo.Variants[j].Name })
	seenCfg := map[*P]bool{}
	for _, t := range targets {
		if seenCfg[t.p] {
			continue
		}
		seenCfg[t.p] = true
		v := t.p.variant
		doc.Configs = append(doc.Configs, irConfig{ID: t.p.id, Platform: t.p.platform, Profile: v.Profile,
			Sanitizers: nonNil(v.Sanitize), Race: v.Race, Features: featureMap(v.Features), Named: nonNil(v.Named)})
	}
	sort.Slice(doc.Configs, func(i, j int) bool { return doc.Configs[i].ID < doc.Configs[j].ID })

	seen := map[*node]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if seen[n] {
			return
		}
		seen[n] = true
		in := irNode{ID: n.id, Kind: n.kind.Name, Label: n.label.String(), DefinedAt: irPos{n.pos.File, n.pos.Line, ev.column(n.pos, n.label.Nix+n.label.Name)},
			Proj: n.proj.json(), Deps: []string{}, Srcs: []irSrcs{}, Spec: n.spec, Nix: n.nix}
		for _, d := range n.deps {
			in.Deps = append(in.Deps, d.id)
			walk(d)
		}
		for _, g := range n.srcs {
			in.Srcs = append(in.Srcs, irSrcs{Dir: g.dir, Patterns: nonNil(g.patterns)})
		}
		doc.Nodes = append(doc.Nodes, in)
	}
	for _, r := range roots {
		walk(r)
	}
	sort.Slice(doc.Nodes, func(i, j int) bool { return doc.Nodes[i].ID < doc.Nodes[j].ID })
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(b)
}

// canonicalJSON re-encodes JSON with object keys sorted, no insignificant whitespace, no HTML
// escaping and numbers kept as written (SPEC §1.6).
func canonicalJSON(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
