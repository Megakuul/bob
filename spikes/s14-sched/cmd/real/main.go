// Command real runs a small real distributed build with W local "workers" (separate
// chroot Nix stores) that pass outputs through a binary cache served over HTTP.
// Modes: a = whole closure on one worker; b = one job per derivation; c = chunks.
// Throwaway spike code (S14).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bobspike/s14/graph"
)

var (
	mode     = flag.String("mode", "b", "a|b|c")
	gname    = flag.String("graph", "layer", "layer|wide|chain")
	workers  = flag.Int("workers", 4, "workers")
	slots    = flag.Int("slots", 2, "parallel builds per worker")
	scale    = flag.Float64("work", 0.2, "seconds of sleep per node")
	capCost  = flag.Float64("cap", 6, "chunk cost cap (c)")
	root     = flag.String("root", "/root/scratch-s14/run", "scratch root")
	port     = flag.Int("port", 21400, "cache port")
	bb       = flag.String("bb", "", "static busybox store path")
	claimLat = flag.Duration("claim", 0, "added per-job latency standing in for dynamitedb claim+complete")
	inc      = flag.Bool("inc", false, "after the cold run, change one leaf and rebuild (same workers)")
)

var gets, heads, misses atomic.Int64

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME=/root/.cache-s14", "NIX_CONFIG=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %v\n%s", name, args, err, tail(string(out)))
	}
	return string(out), nil
}

func tail(s string) string {
	if len(s) > 800 {
		return s[len(s)-800:]
	}
	return s
}

type tgraph struct {
	Salt  string           `json:"salt"`
	Nodes map[string]tnode `json:"nodes"`
}
type tnode struct {
	Deps []string `json:"deps"`
	Work string   `json:"work"`
	Size int64    `json:"size"`
}

func mkGraph() *graph.Graph {
	c := func(*rand.Rand) float64 { return *scale }
	switch *gname {
	case "layer": // 6 layers x 12, k=3 -> 73 drvs
		return graph.Layered("layer", 7, 6, 12, 3, c, 64<<10, *scale, 256<<10)
	case "wide": // 1 layer of 96 + sink
		return graph.Layered("wide", 7, 1, 96, 0, c, 16<<10, *scale, 16<<10)
	case "chain":
		return graph.Layered("chain", 7, 30, 1, 1, c, 16<<10, *scale, 16<<10)
	}
	log.Fatal("graph?")
	return nil
}

type W struct {
	id    int
	store string
	mu    sync.Mutex
	have  map[int]bool // outputs in local store
}

var (
	g       *graph.Graph
	drvOf   []string
	outOf   []string
	cacheD  string
	subURL  string
	ws      []*W
	stats   = map[string]*phase{}
	statsMu sync.Mutex
)

type phase struct {
	n   int
	sum time.Duration
}

func timeit(p string, f func() error) {
	t := time.Now()
	must(f())
	d := time.Since(t)
	statsMu.Lock()
	if stats[p] == nil {
		stats[p] = &phase{}
	}
	stats[p].n++
	stats[p].sum += d
	statsMu.Unlock()
}

func nixStore(w *W) string { return "local?root=" + w.store }

func setup(salt string) {
	tg := tgraph{Salt: salt, Nodes: map[string]tnode{}}
	for _, n := range g.Nodes {
		d := []string{}
		for _, x := range n.Deps {
			d = append(d, g.Nodes[x].ID)
		}
		tg.Nodes[n.ID] = tnode{Deps: d, Work: fmt.Sprintf("%.3f", n.Cost), Size: n.Size}
	}
	gf := filepath.Join(*root, "graph.json")
	b, _ := json.Marshal(tg)
	must(os.WriteFile(gf, b, 0o644))
	nixf, _ := filepath.Abs("nix/graph.nix")
	expr := fmt.Sprintf(`builtins.mapAttrs (n: d: [d.drvPath d.outPath]) (import %s { graphFile = %s; bb = "%s"; })`, nixf, gf, *bb)
	out, err := run("nix", "eval", "--impure", "--json", "--expr", expr)
	must(err)
	var m map[string][2]string
	must(json.Unmarshal([]byte(lastLine(out)), &m))
	drvOf = make([]string, len(g.Nodes))
	outOf = make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		drvOf[i], outOf[i] = m[n.ID][0], m[n.ID][1]
	}
	// eval job uploads the drv closure (sources + drvs) once
	timeit("eval: upload drv closure", func() error {
		_, err := run("nix", append([]string{"copy", "--to", "file://" + cacheD}, drvOf...)...)
		return err
	})
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

var nixOpts = []string{"--option", "require-sigs", "false", "--option", "narinfo-cache-negative-ttl", "0",
	"--option", "substituters", "", "--option", "sandbox", "true"}

// job builds `nodes` on worker w with -j slots and uploads their outputs.
func job(w *W, nodes []int) {
	time.Sleep(*claimLat)
	// drv closure: fetch what the worker's store lacks (Nix needs every input drv)
	var drvs, targets []string
	for _, v := range nodes {
		drvs = append(drvs, drvOf[v])
		targets = append(targets, drvOf[v]+"^out")
	}
	timeit("job: fetch drv closure", func() error {
		_, err := run("nix", append([]string{"copy", "--from", subURL, "--to", nixStore(w)}, append(nixOpts, drvs...)...)...)
		return err
	})
	// inputs from other jobs: substitute explicitly (what Nix would do on demand)
	in := map[int]bool{}
	inSet := map[int]bool{}
	for _, v := range nodes {
		inSet[v] = true
	}
	w.mu.Lock()
	for _, v := range nodes {
		for _, d := range g.Nodes[v].Deps {
			if !inSet[d] && !w.have[d] {
				in[d] = true
			}
		}
	}
	w.mu.Unlock()
	if len(in) > 0 {
		var ps []string
		for d := range in {
			ps = append(ps, outOf[d])
		}
		timeit("job: substitute inputs", func() error {
			_, err := run("nix", append([]string{"copy", "--from", subURL, "--to", nixStore(w)}, append(nixOpts, ps...)...)...)
			return err
		})
	}
	timeit("job: nix build", func() error {
		_, err := run("nix", append([]string{"build", "--store", nixStore(w), "--no-link", "-j", fmt.Sprint(*slots), "--cores", "1"}, append(nixOpts, targets...)...)...)
		return err
	})
	var outs []string
	for _, v := range nodes {
		outs = append(outs, outOf[v])
	}
	timeit("job: upload outputs", func() error {
		_, err := run("nix", append([]string{"copy", "--from", nixStore(w), "--to", "file://" + cacheD}, append(nixOpts, outs...)...)...)
		return err
	})
	w.mu.Lock()
	for _, v := range nodes {
		w.have[v] = true
	}
	for d := range in {
		w.have[d] = true
	}
	w.mu.Unlock()
}

// schedule runs units (sets of nodes) respecting unit deps; each worker has `per`
// concurrent job slots. Affinity: prefer the worker holding most of the unit's inputs.
func schedule(units [][]int, unitDeps [][]int, per int, only map[int]bool) {
	n := len(units)
	done := make([]bool, n)
	running := make([]bool, n)
	free := make([]int, len(ws))
	for i := range free {
		free[i] = per
	}
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	left := n
	mu.Lock()
	for left > 0 {
		// find ready units and free workers
		dispatched := false
		for u := 0; u < n; u++ {
			if done[u] || running[u] {
				continue
			}
			ready := true
			for _, d := range unitDeps[u] {
				if !done[d] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			best, bestScore := -1, -1
			for wi, w := range ws {
				if free[wi] == 0 {
					continue
				}
				sc := 0
				w.mu.Lock()
				for _, v := range units[u] {
					for _, d := range g.Nodes[v].Deps {
						if w.have[d] {
							sc++
						}
					}
				}
				w.mu.Unlock()
				sc = sc*100 + free[wi]
				if sc > bestScore {
					best, bestScore = wi, sc
				}
			}
			if best < 0 {
				break
			}
			free[best]--
			running[u] = true
			dispatched = true
			go func(u, wi int) {
				job(ws[wi], units[u])
				mu.Lock()
				done[u] = true
				running[u] = false
				free[wi]++
				left--
				cond.Broadcast()
				mu.Unlock()
			}(u, best)
		}
		if !dispatched {
			cond.Wait()
		}
	}
	mu.Unlock()
}

func main() {
	flag.Parse()
	if *bb == "" {
		log.Fatal("-bb required")
	}
	g = mkGraph()
	must(os.RemoveAll(*root))
	cacheD = filepath.Join(*root, "cache")
	must(os.MkdirAll(cacheD, 0o755))
	// binary cache over HTTP with request counting
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	must(err)
	fs := http.FileServer(http.Dir(cacheD))
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			heads.Add(1)
		} else {
			gets.Add(1)
		}
		if _, err := os.Stat(filepath.Join(cacheD, filepath.Clean(r.URL.Path))); err != nil {
			misses.Add(1)
		}
		fs.ServeHTTP(rw, r)
	}))
	subURL = fmt.Sprintf("http://127.0.0.1:%d", *port)
	for i := 0; i < *workers; i++ {
		w := &W{id: i, store: filepath.Join(*root, fmt.Sprintf("w%d", i)), have: map[int]bool{}}
		// toolchain is pre-warmed (not timed)
		_, err := run("nix", "copy", "--no-check-sigs", "--to", nixStore(w), *bb)
		must(err)
		ws = append(ws, w)
	}
	salt := fmt.Sprint(time.Now().UnixNano())
	setup(salt)
	gets.Store(0)
	heads.Store(0)
	misses.Store(0)
	t0 := time.Now()
	runMode(nil)
	wall := time.Since(t0)
	report("cold", wall)
	if *inc {
		// one-leaf change: rebuild node 0 and its dependents (new salt only for them is
		// not expressible here, so we delete their outputs everywhere and rebuild).
		aff := map[int]bool{0: true}
		for i, nd := range g.Nodes {
			for _, d := range nd.Deps {
				if aff[d] {
					aff[i] = true
				}
			}
		}
		for i := range aff {
			h := strings.TrimPrefix(outOf[i], "/nix/store/")[:32]
			os.Remove(filepath.Join(cacheD, h+".narinfo"))
			for _, w := range ws {
				run("nix", "store", "delete", "--store", nixStore(w), outOf[i])
				w.mu.Lock()
				delete(w.have, i)
				w.mu.Unlock()
			}
		}
		statsMu.Lock()
		stats = map[string]*phase{}
		statsMu.Unlock()
		gets.Store(0)
		heads.Store(0)
		misses.Store(0)
		t1 := time.Now()
		runMode(aff)
		report(fmt.Sprintf("incremental (%d of %d drvs rebuilt)", len(aff), len(g.Nodes)), time.Since(t1))
	}
}

func runMode(only map[int]bool) {
	keep := func(v int) bool { return only == nil || only[v] }
	switch *mode {
	case "a":
		var all []int
		for i := range g.Nodes {
			if keep(i) {
				all = append(all, i)
			}
		}
		schedule([][]int{all}, [][]int{nil}, 1, only)
	case "b":
		idx := map[int]int{}
		var units [][]int
		for i := range g.Nodes {
			if keep(i) {
				idx[i] = len(units)
				units = append(units, []int{i})
			}
		}
		deps := make([][]int, len(units))
		for i, u := range units {
			for _, d := range g.Nodes[u[0]].Deps {
				if j, ok := idx[d]; ok {
					deps[i] = append(deps[i], j)
				}
			}
		}
		schedule(units, deps, *slots, only)
	case "c":
		cs := graph.ChunkGraph(g, *capCost, 0.15)
		var units [][]int
		var deps [][]int
		idx := map[int]int{}
		for ci, c := range cs {
			var u []int
			for _, v := range c.Nodes {
				if keep(v) {
					u = append(u, v)
				}
			}
			if len(u) == 0 {
				continue
			}
			idx[ci] = len(units)
			units = append(units, u)
		}
		for ci, c := range cs {
			j, ok := idx[ci]
			if !ok {
				continue
			}
			var d []int
			for _, x := range c.Deps {
				if k, ok := idx[x]; ok {
					d = append(d, k)
				}
			}
			for len(deps) <= j {
				deps = append(deps, nil)
			}
			deps[j] = d
		}
		for len(deps) < len(units) {
			deps = append(deps, nil)
		}
		fmt.Printf("chunks: %d (cap %.1f s)\n", len(units), *capCost)
		schedule(units, deps, 1, only)
	}
}

func report(label string, wall time.Duration) {
	files := 0
	filepath.Walk(cacheD, func(p string, i os.FileInfo, e error) error {
		if e == nil && !i.IsDir() {
			files++
		}
		return nil
	})
	fmt.Printf("== %s mode=%s graph=%s drvs=%d workers=%d slots=%d work=%.2fs claim=%v\n", label, *mode, *gname, len(g.Nodes), *workers, *slots, *scale, *claimLat)
	fmt.Printf("wall %.2f s; cache GET %d HEAD %d (404 %d); cache files %d\n", wall.Seconds(), gets.Load(), heads.Load(), misses.Load(), files)
	var ks []string
	for k := range stats {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	w := bufio.NewWriter(os.Stdout)
	for _, k := range ks {
		p := stats[k]
		fmt.Fprintf(w, "  %-28s n=%4d mean %.3f s total %.1f s\n", k, p.n, p.sum.Seconds()/float64(p.n), p.sum.Seconds())
	}
	w.Flush()
}
