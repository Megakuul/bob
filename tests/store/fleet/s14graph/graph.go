// Package s14graph holds the S14 workload graphs and the chunking algorithm.
// Copied from spikes/s14-sched/graph (own code) so that the planner test runs on the same graphs as S14.
package s14graph

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Node is one derivation. Cost is the build time on one slot in seconds, without
// the per-derivation fixed cost (Nix start, sandbox, register). Size is output bytes.
type Node struct {
	ID   string
	Deps []int
	Cost float64
	Size int64
	Pkg  string // grouping label (package / crate / module)
}

type Graph struct {
	Name  string
	Nodes []Node // topological order: deps have smaller index
}

func (g *Graph) TotalCost() float64 {
	s := 0.0
	for _, n := range g.Nodes {
		s += n.Cost
	}
	return s
}

// BottomLevel returns, per node, cost of the longest path from the node to a sink
// (inclusive), with per-node extra cost added.
func (g *Graph) BottomLevel(extra float64) []float64 {
	succ := g.Succ()
	bl := make([]float64, len(g.Nodes))
	for i := len(g.Nodes) - 1; i >= 0; i-- {
		m := 0.0
		for _, s := range succ[i] {
			if bl[s] > m {
				m = bl[s]
			}
		}
		bl[i] = m + g.Nodes[i].Cost + extra
	}
	return bl
}

func (g *Graph) CriticalPath(extra float64) float64 {
	m := 0.0
	for _, v := range g.BottomLevel(extra) {
		m = math.Max(m, v)
	}
	return m
}

func (g *Graph) Succ() [][]int {
	s := make([][]int, len(g.Nodes))
	for i, n := range g.Nodes {
		for _, d := range n.Deps {
			s[d] = append(s[d], i)
		}
	}
	return s
}

func logn(r *rand.Rand, mean, sigma float64) float64 {
	mu := math.Log(mean) - sigma*sigma/2
	return math.Exp(mu + sigma*r.NormFloat64())
}

func pick(r *rand.Rand, lo, hi, k int) []int {
	if hi-lo <= k {
		out := []int{}
		for i := lo; i < hi; i++ {
			out = append(out, i)
		}
		return out
	}
	seen := map[int]bool{}
	out := []int{}
	for len(out) < k {
		x := lo + r.Intn(hi-lo)
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Ints(out)
	return out
}

// Layered builds a graph of `layers` layers of `width` nodes; each node depends on k
// nodes of the layers below (mostly the previous one), then one sink ("link").
func Layered(name string, seed int64, layers, width, k int, cost func(*rand.Rand) float64, size int64, sinkCost float64, sinkSize int64) *Graph {
	r := rand.New(rand.NewSource(seed))
	g := &Graph{Name: name}
	for l := 0; l < layers; l++ {
		for w := 0; w < width; w++ {
			n := Node{ID: fmt.Sprintf("n%d_%d", l, w), Cost: cost(r), Size: size, Pkg: fmt.Sprintf("p%d", w%10)}
			if l > 0 {
				lo := (l - 1) * width
				if l > 1 && r.Intn(3) == 0 {
					lo = 0
				}
				n.Deps = pick(r, lo, l*width, k)
			}
			g.Nodes = append(g.Nodes, n)
		}
	}
	sink := Node{ID: "link", Cost: sinkCost, Size: sinkSize, Pkg: "main"}
	// the sink depends on the top layer and a sample of everything below
	sink.Deps = pick(r, (layers-1)*width, layers*width, width)
	g.Nodes = append(g.Nodes, sink)
	return g
}

// Workloads returns the S14 workloads. Calibration (see write-up §3.1):
//   - go200: S2 syn200 package mode, 201 drvs; cold [2c/4G] 12.7 s on 2 slots.
//   - fixture: S2 fixture package mode (32 Go pkgs + libfoo + rcore + protobuf), cold [2c] 12.7 s.
//   - rust49: S3 demo, 120 drvs, cold [2c] 148 s on 2 slots; heavy-tailed crate times.
//   - rust129: S3 web, ~300 drvs, cold 180 s on 4 slots.
//   - wide2000: synthetic, 2,000 drvs of 50 ms.
//   - bigcpp: 20 small deps, one 900 s C++ compile, 10 dependents, 30 s link.
func Workloads() []*Graph {
	return []*Graph{Fixture(), Go200(), Rust(49, 120, 2.2, 1), Rust(129, 300, 2.2, 2), Wide2000(), BigCpp()}
}

func Go200() *Graph {
	return Layered("go200", 2, 10, 20, 3, func(r *rand.Rand) float64 { return logn(r, 0.10, 0.5) }, 200<<10, 1.5, 8<<20)
}

func Fixture() *Graph {
	r := rand.New(rand.NewSource(3))
	g := &Graph{Name: "fixture"}
	add := func(id string, cost float64, size int64, pkg string, deps ...int) int {
		g.Nodes = append(g.Nodes, Node{ID: id, Cost: cost, Size: size, Pkg: pkg, Deps: deps})
		return len(g.Nodes) - 1
	}
	proto := add("protoc-gen", 0.5, 50<<10, "proto")
	libfoo := add("libfoo", 1.5, 2<<20, "libfoo")
	rc1 := add("rcore-dep", 2.0, 1<<20, "rcore")
	rc2 := add("rcore-bs", 1.0, 1<<20, "rcore", rc1)
	rc3 := add("rcore", 2.0, 2<<20, "rcore", rc2)
	var pb []int
	for i := 0; i < 8; i++ {
		var d []int
		if i > 0 {
			d = []int{pb[i-1]}
		}
		pb = append(pb, add(fmt.Sprintf("protobuf%d", i), 0.4+r.Float64()*0.6, 500<<10, "protobuf", d...))
	}
	var gp []int
	for i := 0; i < 24; i++ {
		d := []int{pb[r.Intn(len(pb))]}
		if i >= 6 {
			d = append(d, gp[r.Intn(i-3)])
		}
		if i == 20 {
			d = append(d, proto)
		}
		gp = append(gp, add(fmt.Sprintf("go%d", i), logn(r, 0.15, 0.5), 200<<10, "api", d...))
	}
	api := add("api-cgo", 1.0, 1<<20, "api", libfoo, gp[23], gp[22])
	add("main-link", 1.5, 12<<20, "main", api, rc3, gp[21])
	return g
}

// Rust approximates a cargo graph: units = crates; ~1/5 have a build script (compile
// script, run script, compile lib = 3 drvs). Crate times lognormal; a few big crates.
func Rust(units, drvs int, mean float64, seed int64) *Graph {
	r := rand.New(rand.NewSource(seed + 100))
	g := &Graph{Name: fmt.Sprintf("rust%d", units)}
	crateNode := []int{}
	for len(g.Nodes) < drvs-1 {
		c := len(crateNode)
		// deps: crates earlier, prefer recent (deep chains like syn->serde_derive->serde)
		var deps []int
		if c > 0 {
			nd := 1 + r.Intn(4)
			for i := 0; i < nd; i++ {
				lo := c - 12
				if lo < 0 || r.Intn(3) == 0 {
					lo = 0
				}
				deps = append(deps, crateNode[lo+r.Intn(c-lo)])
			}
		}
		pkg := fmt.Sprintf("crate%d", c)
		cost := logn(r, mean, 1.0)
		if r.Intn(5) == 0 && len(g.Nodes) < drvs-4 {
			g.Nodes = append(g.Nodes, Node{ID: pkg + "-bs", Cost: logn(r, 1.0, 0.5), Size: 2 << 20, Pkg: pkg, Deps: deps})
			g.Nodes = append(g.Nodes, Node{ID: pkg + "-run", Cost: logn(r, 0.5, 1.0), Size: 100 << 10, Pkg: pkg, Deps: []int{len(g.Nodes) - 1}})
			deps = append([]int{len(g.Nodes) - 1}, deps...)
		}
		dedup := map[int]bool{}
		var dd []int
		for _, d := range deps {
			if !dedup[d] {
				dedup[d] = true
				dd = append(dd, d)
			}
		}
		sort.Ints(dd)
		g.Nodes = append(g.Nodes, Node{ID: pkg, Cost: cost, Size: int64(1+r.Intn(4)) << 20, Pkg: pkg, Deps: dd})
		crateNode = append(crateNode, len(g.Nodes)-1)
	}
	// binary: depends on the last 8 crates
	g.Nodes = append(g.Nodes, Node{ID: "bin", Cost: 6, Size: 20 << 20, Pkg: "bin", Deps: crateNode[len(crateNode)-8:]})
	return g
}

func Wide2000() *Graph {
	r := rand.New(rand.NewSource(5))
	g := &Graph{Name: "wide2000"}
	for i := 0; i < 50; i++ {
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("base%d", i), Cost: 0.05, Size: 50 << 10, Pkg: fmt.Sprintf("g%d", i)})
	}
	for i := 0; i < 1898; i++ {
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("mid%d", i), Cost: 0.05, Size: 50 << 10, Pkg: fmt.Sprintf("g%d", i%50), Deps: pick(r, 0, 50, 2)})
	}
	for i := 0; i < 50; i++ {
		var d []int
		for j := i; j < 1898; j += 50 {
			d = append(d, 50+j)
		}
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("agg%d", i), Cost: 0.05, Size: 50 << 10, Pkg: fmt.Sprintf("g%d", i), Deps: d})
	}
	var d []int
	for i := 0; i < 50; i++ {
		d = append(d, 1948+i)
	}
	g.Nodes = append(g.Nodes, Node{ID: "top", Cost: 0.05, Size: 50 << 10, Pkg: "top", Deps: d})
	return g
}

func BigCpp() *Graph {
	g := &Graph{Name: "bigcpp"}
	var small []int
	for i := 0; i < 20; i++ {
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("dep%d", i), Cost: 0.5, Size: 1 << 20, Pkg: "deps"})
		small = append(small, i)
	}
	g.Nodes = append(g.Nodes, Node{ID: "bigcc", Cost: 900, Size: 200 << 20, Pkg: "big", Deps: small})
	big := len(g.Nodes) - 1
	var mid []int
	for i := 0; i < 10; i++ {
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("use%d", i), Cost: 5, Size: 2 << 20, Pkg: "use", Deps: []int{big}})
		mid = append(mid, len(g.Nodes)-1)
	}
	g.Nodes = append(g.Nodes, Node{ID: "link", Cost: 30, Size: 300 << 20, Pkg: "main", Deps: mid})
	return g
}

// ---------------------------------------------------------------------------
// Chunking (option c).

// Chunk is a convex set of nodes (no path leaves the chunk and comes back), so the
// chunk graph is a DAG.
type Chunk struct {
	Nodes []int
	Deps  []int // chunk ids
	Cost  float64
}

// ChunkGraph partitions g into convex chunks of estimated cost <= capCost. It walks
// nodes in topological order; each node joins the chunk of its critical predecessor
// (largest top level) if that keeps the chunk under the cap and does not create a
// cycle between chunks; otherwise it tries the other predecessors' chunks, then
// starts a new chunk. extra = per-derivation fixed cost added to the estimate.
// minWidth: when a chunk's predecessor already has more than `slots` ready siblings
// in the same chunk, start a new chunk instead (spreads wide layers).
func ChunkGraph(g *Graph, capCost, extra float64) []Chunk {
	n := len(g.Nodes)
	chunkOf := make([]int, n)
	var chunks []Chunk
	// reach[c] = set of chunks that c (transitively) depends on, incl. c.
	var reach []map[int]bool
	tl := make([]float64, n) // top level: finish time if unlimited parallelism
	for i, nd := range g.Nodes {
		st := 0.0
		for _, d := range nd.Deps {
			st = math.Max(st, tl[d])
		}
		tl[i] = st + nd.Cost + extra
		// candidate chunks ordered by predecessor top level
		preds := append([]int(nil), nd.Deps...)
		sort.Slice(preds, func(a, b int) bool { return tl[preds[a]] > tl[preds[b]] })
		joined := -1
		tried := map[int]bool{}
		cands := []int{}
		for _, p := range preds {
			cands = append(cands, chunkOf[p])
		}
		// then the newest chunk (packs independent roots and spill-over together)
		if len(chunks) > 0 {
			cands = append(cands, len(chunks)-1)
		}
		for _, c := range cands {
			if tried[c] {
				continue
			}
			tried[c] = true
			if chunks[c].Cost+nd.Cost+extra > capCost {
				continue
			}
			// joining c adds edges other->c for every other pred chunk. Cycle iff c is
			// already an ancestor of such a chunk (other depends on c, other != c).
			ok := true
			for _, q := range nd.Deps {
				oc := chunkOf[q]
				if oc != c && reach[oc][c] {
					ok = false
					break
				}
			}
			if ok {
				joined = c
				break
			}
		}
		if joined < 0 {
			chunks = append(chunks, Chunk{})
			reach = append(reach, map[int]bool{len(chunks) - 1: true})
			joined = len(chunks) - 1
		}
		chunkOf[i] = joined
		chunks[joined].Nodes = append(chunks[joined].Nodes, i)
		chunks[joined].Cost += nd.Cost + extra
		for _, q := range nd.Deps {
			oc := chunkOf[q]
			if oc != joined && !hasInt(chunks[joined].Deps, oc) {
				chunks[joined].Deps = append(chunks[joined].Deps, oc)
				// propagate reachability to joined and every chunk that reaches joined
				for c2 := range chunks {
					if reach[c2][joined] {
						for x := range reach[oc] {
							reach[c2][x] = true
						}
					}
				}
			}
		}
	}
	return chunks
}

func hasInt(s []int, x int) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// ChunkOf returns node -> chunk index.
func ChunkOf(n int, cs []Chunk) []int {
	m := make([]int, n)
	for ci, c := range cs {
		for _, v := range c.Nodes {
			m[v] = ci
		}
	}
	return m
}

// Convex checks that the chunk DAG is acyclic (for tests).
func Convex(cs []Chunk) bool {
	state := make([]int, len(cs))
	var visit func(int) bool
	visit = func(c int) bool {
		if state[c] == 1 {
			return false
		}
		if state[c] == 2 {
			return true
		}
		state[c] = 1
		for _, d := range cs[c].Deps {
			if !visit(d) {
				return false
			}
		}
		state[c] = 2
		return true
	}
	for c := range cs {
		if !visit(c) {
			return false
		}
	}
	return true
}
