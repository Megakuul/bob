package bob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

// node is one graph node: a package in one projected configuration (SPEC §1.6).
type node struct {
	id    string
	kind  graph.Kind
	label Label
	pos   Position
	p     *P // configuration the node is built for (p.Host() for host kinds)
	evalP *P // configuration whose evaluation created it
	deps  []*node
	srcs  []srcGroup
	spec  json.RawMessage
	nix   json.RawMessage // nixpkgs kinds: the IR "nix" object
	proj  projection

	allowed []Platform // platforms the node may be built for (ext.Restrict); nil = all

	evals map[*P]bool   // configurations whose evaluation created or reused the node
	defs  []*definition // definitions that created it
}

// projection is the part of a configuration a node depends on: the axes its kind declares,
// plus those of its dependencies (a node built from different inputs is a different node).
type projection struct {
	has        [graph.NumAxes]bool
	platform   Platform
	profile    Profile
	sanitizers []Sanitizer
	race       bool
	features   map[Label][]string
}

func init() {
	for _, k := range []graph.Kind{
		{Name: "bob.run", Axes: []int{graph.AxisPlatform}},
		{Name: "bob.file"},
		{Name: "bob.fetch"},
		{Name: "bob.devshell", Axes: []int{graph.AxisPlatform}, Host: true},
	} {
		graph.Register(k)
	}
	graph.NewNode = func(p any, kind, name string, deps, srcs, spec any) any {
		pp, _ := p.(*P)
		ds, _ := deps.([]Package)
		ss, _ := srcs.([]Sources)
		return newNode(pp, kind, name, ds, ss, spec)
	}
	graph.NewNodeIn = func(p any, kind, dir, name string, deps, srcs, spec any) any {
		pp, _ := p.(*P)
		ds, _ := deps.([]Package)
		ss, _ := srcs.([]Sources)
		return newNodeAt(pp, kind, &dir, name, ds, ss, spec)
	}
	graph.NewNodeAt = func(p any, at any, kind, name string, deps, srcs, spec any) any {
		pp, _ := p.(*P)
		ds, _ := deps.([]Package)
		ss, _ := srcs.([]Sources)
		st, _ := at.(site)
		return newNode(pp, kind, name, ds, ss, spec, st)
	}
	graph.Site = func() any { return bobSite() }
	graph.ConfigOf = func(n any) graph.Config {
		nn, _ := n.(*Node)
		return configOf(nn)
	}
	graph.Restrict = func(n, platforms any) {
		nn, _ := n.(Node)
		ps, _ := platforms.([]Platform)
		restrict(nn.n, ps)
	}
	graph.CallerDir = func() (string, bool) {
		s := bobSite()
		return s.dir, s.ok
	}
	graph.Misuse = func(msg string) {
		if ev := active; ev != nil {
			ev.misuse(msg)
			return
		}
		reg.early = append(reg.early, diagnostic{msg: msg})
	}
}

// newNode is ext.New: it creates (or, for an identical node of another configuration, reuses)
// the node of kind for p. Errors are recorded on the evaluation and give a zero Node.
func newNode(p *P, kindName, name string, deps []Package, srcs []Sources, spec any, at ...site) Node {
	return newNodeAt(p, kindName, nil, name, deps, srcs, spec, at...)
}

// newNodeAt is newNode with an optional label directory (ext.NewIn) replacing the calling
// bob.go's directory, and an optional earlier call site (ext.NewAt) whose position becomes the
// node's definedAt.
func newNodeAt(p *P, kindName string, dir *string, name string, deps []Package, srcs []Sources, spec any, at ...site) Node {
	ev := active
	if ev == nil || p == nil || p.ev != ev {
		msg := fmt.Sprintf("ext.New(%q, %q) was called outside evaluation; call constructors only inside a bob.Define function", kindName, name)
		if ev != nil {
			ev.misuse(msg)
		} else {
			reg.early = append(reg.early, diagnostic{msg: msg})
		}
		return Node{}
	}
	s := bobSite()
	if !s.ok {
		ev.misuse(fmt.Sprintf("ext.New(%q, %q) was called from %s, which is not a bob.go file of this build", kindName, name, s.file))
		return Node{}
	}
	k, ok := graph.Lookup(kindName)
	if !ok {
		ev.misuse(fmt.Sprintf("%s: unknown node kind %q (register it with ext.Register)", s.pos, kindName))
		return Node{}
	}
	var l Label
	if strings.HasPrefix(kindName, "nixpkgs.") {
		if err := checkAttr(name); err != nil {
			ev.misuse(fmt.Sprintf("%s: %v", s.pos, err))
			return Node{}
		}
		l = Label{Nix: name}
	} else {
		if err := checkName(name); err != nil || name == "all" {
			if err == nil {
				err = fmt.Errorf(`package name "all" is reserved for the pattern "//dir:all"`)
			}
			ev.misuse(fmt.Sprintf("%s: %v", s.pos, err))
			return Node{}
		}
		l = Label{Module: s.mod, Dir: s.dir, Name: name}
		if dir != nil {
			if err := checkDir(*dir); err != nil {
				ev.misuse(fmt.Sprintf("%s: label directory %q: %v", s.pos, *dir, err))
				return Node{}
			}
			l.Dir = *dir
		}
	}
	pk := p
	if k.Host {
		pk = p.Host()
	}
	pos := s.pos
	if len(at) > 0 && at[0].ok && at[0].mod == s.mod {
		pos = at[0].pos
	}
	n := &node{kind: k, label: l, pos: pos, p: pk, evalP: p, evals: map[*P]bool{}}
	for i, dep := range deps {
		dn := nodeOf(dep)
		if dn == nil {
			ev.nilDep(fmt.Sprintf("%s: dependency %d of %s is nil", s.pos, i, l))
			continue
		}
		n.deps = append(n.deps, dn)
	}
	for _, src := range srcs {
		n.srcs = append(n.srcs, src.groups...)
	}
	n.srcs = mergeGroups(n.srcs)
	raw, err := encodeSpec(spec)
	if err != nil {
		ev.misuse(fmt.Sprintf("%s: %s: the spec of kind %s %v", s.pos, l, kindName, err))
		return Node{}
	}
	if l.Nix != "" {
		n.nix, n.spec = raw, json.RawMessage("{}")
	} else {
		n.spec = raw
	}
	n.proj = project(k, pk, l, n.deps)
	n.id = nodeID(l, n.proj)
	if l.Nix != "" {
		n.id += nixTokens(kindName, n.nix)
	}

	if old, ok := ev.nodes[n.id]; ok {
		switch {
		case old.sameAs(n):
			old.evals[p] = true
			old.addDef(ev)
			return Node{n: old}
		case old.pos != n.pos:
			dg := diagnostic{pos: n.pos, label: l.String(),
				msg: fmt.Sprintf("%s: package %s is already defined at %s", n.pos, l, old.pos)}
			dg.defs = append(slices.Clone(old.defs), ev.topDefs()...)
			ev.diags = append(ev.diags, dg)
		default:
			ev.misuse(fmt.Sprintf("%s: package %s differs between two calls for one configuration; its constructor must give the same result every time", n.pos, l))
		}
		return Node{n: old}
	}
	n.evals[p] = true
	n.addDef(ev)
	ev.nodes[n.id] = n
	return Node{n: n}
}

// restrict is ext.Restrict: it records the platforms n allows. Unknown platforms are a
// definition error of the running constructor.
func restrict(n *node, ps []Platform) {
	if n == nil || active == nil {
		return
	}
	var allowed []Platform
	for _, p := range ps {
		if !slices.Contains(platforms, p) {
			active.misuse(fmt.Sprintf("%s: %s: unknown platform %q in Platforms (known: linux-amd64, linux-arm64)", n.pos, n.label, p))
			return
		}
		if !slices.Contains(allowed, p) {
			allowed = append(allowed, p)
		}
	}
	slices.Sort(allowed)
	n.allowed = allowed
}

// nilDep reports a nil dependency. A nil package is usually the result of an earlier error (its
// constructor failed), so it is reported only when nothing else explains it.
func (ev *evaluation) nilDep(msg string) {
	if len(ev.diags) == 0 && len(ev.reg.early) == 0 {
		ev.misuse(msg)
	}
}

func (ev *evaluation) topDefs() []*definition {
	if top := ev.top(); top != nil {
		return []*definition{top.def}
	}
	return nil
}

// addDef records the running definition as a creator of n.
func (n *node) addDef(ev *evaluation) {
	top := ev.top()
	if top == nil {
		return
	}
	if !slices.Contains(n.defs, top.def) {
		n.defs = append(n.defs, top.def)
	}
	top.def.created[top.p] = append(top.def.created[top.p], n)
}

// sameAs reports whether o is the same node as n (same id assumed): same kind, payload,
// sources and dependencies.
func (n *node) sameAs(o *node) bool {
	if n.kind.Name != o.kind.Name || !bytes.Equal(n.spec, o.spec) || !bytes.Equal(n.nix, o.nix) ||
		len(n.deps) != len(o.deps) || len(n.srcs) != len(o.srcs) {
		return false
	}
	for i := range n.deps {
		if n.deps[i] != o.deps[i] {
			return false
		}
	}
	for i := range n.srcs {
		if n.srcs[i].dir != o.srcs[i].dir || !slices.Equal(n.srcs[i].patterns, o.srcs[i].patterns) {
			return false
		}
	}
	return true
}

// encodeSpec serialises a kind payload; it must be a JSON object (nil = {}).
func encodeSpec(spec any) (json.RawMessage, error) {
	if isNil(spec) {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("cannot be encoded: %w", err)
	}
	b, err = canonicalJSON(b)
	if err != nil {
		return nil, fmt.Errorf("cannot be encoded: %w", err)
	}
	if len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("must encode to a JSON object (a struct or a map), not %.40s", b)
	}
	return b, nil
}

// project computes the projection of a node of kind k built for p: the kind's declared axes
// from p, then every axis a dependency depends on that the node has not set (from the
// dependency; on a conflict between dependencies, p's value).
func project(k graph.Kind, p *P, l Label, deps []*node) projection {
	var pr projection
	v := p.variant
	set := func(a int) {
		pr.has[a] = true
		switch a {
		case graph.AxisPlatform:
			pr.platform = p.platform
		case graph.AxisProfile:
			pr.profile = v.Profile
		case graph.AxisSanitizers:
			pr.sanitizers = slices.Clone(v.Sanitize)
		case graph.AxisGoRace:
			pr.race = v.Race
		case graph.AxisFeatures:
			if fs := v.Features[l]; len(fs) > 0 {
				pr.features = map[Label][]string{l: slices.Clone(fs)}
			}
		}
	}
	own := pr.has
	for _, a := range k.Axes {
		set(a)
		own[a] = true
	}
	for _, d := range deps {
		dp := d.proj
		for a := 0; a < graph.NumAxes; a++ {
			if !dp.has[a] || own[a] {
				continue
			}
			if a == graph.AxisFeatures {
				pr.has[a] = true
				for fl, fs := range dp.features {
					if pr.features == nil {
						pr.features = map[Label][]string{}
					}
					if old, ok := pr.features[fl]; ok && !slices.Equal(old, fs) {
						pr.features[fl] = slices.Clone(v.Features[fl])
						continue
					}
					pr.features[fl] = fs
				}
				continue
			}
			if !pr.has[a] {
				pr.has[a] = true
				switch a {
				case graph.AxisPlatform:
					pr.platform = dp.platform
				case graph.AxisProfile:
					pr.profile = dp.profile
				case graph.AxisSanitizers:
					pr.sanitizers = dp.sanitizers
				case graph.AxisGoRace:
					pr.race = dp.race
				}
				continue
			}
			if !pr.equalAxis(dp, a) {
				set(a)
			}
		}
	}
	return pr
}

func (pr projection) equalAxis(o projection, a int) bool {
	switch a {
	case graph.AxisPlatform:
		return pr.platform == o.platform
	case graph.AxisProfile:
		return pr.profile == o.profile
	case graph.AxisSanitizers:
		return slices.Equal(pr.sanitizers, o.sanitizers)
	case graph.AxisGoRace:
		return pr.race == o.race
	}
	return true
}

// json returns the IR "proj" object: only the projected axes.
func (pr projection) json() map[string]any {
	out := map[string]any{}
	if pr.has[graph.AxisPlatform] {
		out["platform"] = pr.platform
	}
	if pr.has[graph.AxisProfile] {
		out["profile"] = pr.profile
	}
	if pr.has[graph.AxisSanitizers] {
		out["sanitizers"] = nonNil(pr.sanitizers)
	}
	if pr.has[graph.AxisGoRace] {
		out["race"] = pr.race
	}
	if pr.has[graph.AxisFeatures] {
		out["features"] = featureMap(pr.features)
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func featureMap(f map[Label][]string) map[string][]string {
	out := map[string][]string{}
	for l, fs := range f {
		out[l.String()] = nonNil(fs)
	}
	return out
}

// nodeID is the canonical configured label of a node (SPEC §1.9): the label, then "@", the
// platform (if projected) and the tokens of the projection: profile (omitted if release), asan,
// ubsan, race, and "f-<hash8>" for Rust features. A node that depends on no axis is its label.
func nodeID(l Label, pr projection) string {
	var toks []string
	if pr.has[graph.AxisProfile] && pr.profile != Release {
		toks = append(toks, string(pr.profile))
	}
	for _, s := range pr.sanitizers {
		toks = append(toks, string(s))
	}
	if pr.race {
		toks = append(toks, "race")
	}
	if len(pr.features) > 0 {
		b, _ := json.Marshal(featureMap(pr.features)) // map keys are sorted; cannot fail
		h := sha256.Sum256(b)
		toks = append(toks, "f-"+hex.EncodeToString(h[:4]))
	}
	plat := ""
	if pr.has[graph.AxisPlatform] {
		plat = string(pr.platform)
	}
	if plat == "" && len(toks) == 0 {
		return l.String()
	}
	return l.String() + "@" + strings.Join(append([]string{plat}, toks...), "+")
}

// nixTokens is the identity suffix of a nixpkgs node (A91): its kind ("tool", "lib", "pkg"),
// "cross" for the cross package set, and "n-<hash8>" of its options (outputs, modules, allow)
// when any is set. One attribute used as Tool and Lib, or with different Modules, Outputs or
// Allow options, is then two nodes rather than a definition error on the native platform,
// where host and target platforms are equal.
func nixTokens(kindName string, raw json.RawMessage) string {
	toks := "+" + strings.TrimPrefix(kindName, "nixpkgs.")
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil {
		return toks
	}
	if string(o["set"]) == `"cross"` {
		toks += "+cross"
	}
	delete(o, "attr")
	delete(o, "set")
	b, err := json.Marshal(o) // keys sorted; raw values are canonical (encodeSpec)
	if err != nil || string(b) == nixDefaultOptions {
		return toks
	}
	h := sha256.Sum256(b)
	return toks + "+n-" + hex.EncodeToString(h[:4])
}

// nixDefaultOptions is the options part of a nixpkgs "nix" object without options (R-FW-047).
const nixDefaultOptions = `{"allow":{"insecure":[],"unfree":false},"outputs":[]}`

// configOf is ext.ConfigOf.
func configOf(n *Node) graph.Config {
	if n == nil || n.n == nil {
		return graph.Config{}
	}
	nn := n.n
	c := graph.Config{Kind: nn.kind.Name, Axes: nn.kind.Axes, Platform: string(nn.p.platform),
		Profile: string(nn.p.variant.Profile), Race: nn.p.variant.Race, Features: map[string][]string{}}
	for _, s := range nn.p.variant.Sanitize {
		c.Sanitizers = append(c.Sanitizers, string(s))
	}
	for l, fs := range nn.p.variant.Features {
		c.Features[l.String()] = slices.Clone(fs)
	}
	return c
}

// sortedIDs returns the ids of ns, sorted.
func sortedIDs(ns []*node) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.id)
	}
	sort.Strings(out)
	return slices.Compact(out)
}
