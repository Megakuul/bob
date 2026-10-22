package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// graph is the package graph by label (configurations merged: one node per label).
type graph struct {
	nodes map[string]gnode
	deps  map[string][]string // label → dependency labels, sorted
}

type gnode struct {
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	DefinedAt string `json:"definedAt"`
}

type gedge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// graphOf merges the IR nodes of every configuration by label.
func graphOf(g *ir.Graph) *graph {
	byID := map[string]*ir.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	out := &graph{nodes: map[string]gnode{}, deps: map[string][]string{}}
	seen := map[string]map[string]bool{}
	for _, n := range g.Nodes {
		if _, ok := out.nodes[n.Label]; !ok {
			out.nodes[n.Label] = gnode{Label: n.Label, Kind: n.Kind, DefinedAt: posString(n.DefinedAt)}
			seen[n.Label] = map[string]bool{}
		}
		for _, d := range n.Deps {
			dn, ok := byID[d]
			if !ok || dn.Label == n.Label || seen[n.Label][dn.Label] {
				continue
			}
			seen[n.Label][dn.Label] = true
			out.deps[n.Label] = append(out.deps[n.Label], dn.Label)
		}
	}
	for l := range out.deps {
		sort.Strings(out.deps[l])
	}
	return out
}

// graphLabels returns every label of g.
func graphLabels(g *graph) []string {
	out := make([]string, 0, len(g.nodes))
	for l := range g.nodes {
		out = append(out, l)
	}
	return out
}

// posString renders an IR position as "file:line" (the IR records no column yet).
func posString(p ir.Pos) string {
	return translate.PosString(diag.Pos{File: p.File, Line: p.Line})
}

// reverse returns the graph with every edge reversed (--rdeps).
func (g *graph) reverse() *graph {
	r := &graph{nodes: g.nodes, deps: map[string][]string{}}
	for from, tos := range g.deps {
		for _, to := range tos {
			r.deps[to] = append(r.deps[to], from)
		}
	}
	for l := range r.deps {
		sort.Strings(r.deps[l])
	}
	return r
}

// closure returns the labels reachable from roots within depth edges (depth < 0: unbounded).
func (g *graph) closure(roots []string, depth int) map[string]bool {
	in := map[string]bool{}
	level := roots
	for d := 0; len(level) > 0; d++ {
		var next []string
		for _, l := range level {
			if in[l] {
				continue
			}
			in[l] = true
			if depth < 0 || d < depth {
				next = append(next, g.deps[l]...)
			}
		}
		level = next
	}
	return in
}

// paths returns up to max shortest-first simple paths from a to b (breadth-first).
func (g *graph) paths(a, b string, max int) [][]string {
	var out [][]string
	queue := [][]string{{a}}
	for len(queue) > 0 && len(out) < max {
		p := queue[0]
		queue = queue[1:]
		last := p[len(p)-1]
		if last == b {
			out = append(out, p)
			continue
		}
		for _, d := range g.deps[last] {
			if contains(p, d) {
				continue
			}
			np := append(append([]string{}, p...), d)
			queue = append(queue, np)
		}
		if len(queue) > 100000 { // bound the search on huge graphs
			break
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// matchLabel reports whether label matches a label pattern of SPEC §01 ("//...",
// "//libs/...", "//libs/foo:all", or a label).
func matchLabel(pattern, label string) bool {
	switch {
	case pattern == label:
		return true
	case pattern == "//...":
		return strings.HasPrefix(label, "//")
	case strings.HasSuffix(pattern, "/..."):
		dir := strings.TrimSuffix(pattern, "/...")
		pkg, _, _ := strings.Cut(label, ":")
		return pkg == dir || strings.HasPrefix(pkg, dir+"/")
	case strings.HasSuffix(pattern, ":all"):
		pkg, _, _ := strings.Cut(label, ":")
		return pkg == strings.TrimSuffix(pattern, ":all")
	}
	return false
}

// runGraph is `bob graph` (SPEC R-CLI-011).
func runGraph(e *env) int {
	format := e.a.str("format")
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "dot" && format != "json" {
		return e.out.fail(diag.New(1, "kind", "format", "name", format, "suggest", "text, dot or json"))
	}
	depth := -1
	if e.a.has("depth") {
		depth, _ = strconv.Atoi(e.a.str("depth"))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	ig, err := build.Evaluate(e.ctx, build.Options{Root: root, Patterns: []string{"//..."}, Store: st, Offline: e.a.bool("offline")})
	if err != nil {
		return e.out.fail(err)
	}
	g := graphOf(ig)
	recordLabels(root, graphLabels(g))
	why := e.a.vals["why"]
	var whyPaths [][]string
	if len(why) >= 2 {
		from, to := why[len(why)-2], why[len(why)-1]
		for _, l := range []string{from, to} {
			if _, ok := g.nodes[l]; !ok {
				return e.out.fail(diag.New(3, "pattern", l))
			}
		}
		max := 1
		if e.a.bool("all") {
			max = 20
		}
		whyPaths = g.paths(from, to, max)
		if !e.out.json && format == "text" {
			if len(whyPaths) == 0 {
				e.out.printf("bob: %s does not depend on %s\n", from, to)
				return 0
			}
			for i, p := range whyPaths {
				if i > 0 {
					e.out.printf("\n")
				}
				printPath(e.out, g, p)
			}
			return 0
		}
	}
	if e.a.bool("rdeps") {
		g = g.reverse()
	}
	var roots []string
	pats := e.a.pos
	if len(pats) == 0 && !e.a.bool("rdeps") {
		pats = e.patterns(root, nil)
	}
	for l := range g.nodes {
		for _, p := range pats {
			if matchLabel(p, l) {
				roots = append(roots, l)
				break
			}
		}
	}
	if len(pats) > 0 && len(roots) == 0 {
		return e.out.fail(diag.New(3, "pattern", strings.Join(pats, " ")))
	}
	in := g.closure(roots, depth)
	if len(pats) == 0 { // --rdeps without patterns: the whole graph
		in = map[string]bool{}
		for l := range g.nodes {
			in[l] = true
		}
	}
	var sel []string
	for l := range in {
		sel = append(sel, l)
	}
	sort.Strings(sel)
	data := struct {
		Nodes []gnode    `json:"nodes"`
		Edges []gedge    `json:"edges"`
		Why   [][]string `json:"why"`
	}{Nodes: []gnode{}, Edges: []gedge{}, Why: whyPaths}
	if data.Why == nil {
		data.Why = [][]string{}
	}
	for _, l := range sel {
		data.Nodes = append(data.Nodes, g.nodes[l])
		for _, d := range g.deps[l] {
			if in[d] {
				data.Edges = append(data.Edges, gedge{l, d})
			}
		}
	}
	if e.out.json {
		e.out.result(data)
		return e.out.finish(0, translate.Counts{}, nil)
	}
	switch format {
	case "json":
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return e.out.fail(err)
		}
		e.out.printf("%s\n", b)
	case "dot":
		e.out.printf("digraph bob {\n")
		for _, n := range data.Nodes {
			e.out.printf("  %q;\n", n.Label)
		}
		for _, ed := range data.Edges {
			e.out.printf("  %q -> %q;\n", ed.From, ed.To)
		}
		e.out.printf("}\n")
	default:
		printTree(e.out, g, roots, in, depth)
	}
	return 0
}

// posColumn is where positions start in graph text output (SPEC §02.5 sample).
const posColumn = 32

func treeLine(o *out, prefix, label, pos string) {
	text := prefix + label
	pad := posColumn - utf8.RuneCountInString(text)
	if pad < 2 {
		pad = 2
	}
	o.printf("%s%s%s\n", text, strings.Repeat(" ", pad), pos)
}

// printPath prints one --why path as a chain of └─ lines.
func printPath(o *out, g *graph, p []string) {
	for i, l := range p {
		prefix := ""
		if i > 0 {
			prefix = strings.Repeat("   ", i-1) + "└─ "
		}
		treeLine(o, prefix, l, g.nodes[l].DefinedAt)
	}
}

// printTree prints the dependency tree of each root; a subtree already printed is shown once
// and marked "(…)" afterwards.
func printTree(o *out, g *graph, roots []string, in map[string]bool, depth int) {
	// Print only the roots no other root depends on; the rest appear below them.
	dep := map[string]bool{}
	for _, r := range roots {
		for _, d := range g.deps[r] {
			dep[d] = true
		}
	}
	var tops []string
	for _, r := range roots {
		if !dep[r] {
			tops = append(tops, r)
		}
	}
	if len(tops) > 0 {
		roots = tops
	}
	sort.Strings(roots)
	shown := map[string]bool{}
	var walk func(l, indent string, last bool, top bool, d int)
	walk = func(l, indent string, last, top bool, d int) {
		prefix, child := "", ""
		if !top {
			if last {
				prefix, child = indent+"└─ ", indent+"   "
			} else {
				prefix, child = indent+"├─ ", indent+"│  "
			}
		}
		if shown[l] && len(g.deps[l]) > 0 {
			treeLine(o, prefix, l+" (…)", g.nodes[l].DefinedAt)
			return
		}
		shown[l] = true
		treeLine(o, prefix, l, g.nodes[l].DefinedAt)
		if depth >= 0 && d >= depth {
			return
		}
		var kids []string
		for _, k := range g.deps[l] {
			if in[k] {
				kids = append(kids, k)
			}
		}
		for i, k := range kids {
			walk(k, child, i == len(kids)-1, false, d+1)
		}
	}
	for _, r := range roots {
		if !shown[r] {
			walk(r, "", true, true, 0)
		}
	}
	if len(roots) == 0 {
		fmt.Fprintln(o.w, "bob: no packages")
	}
}
