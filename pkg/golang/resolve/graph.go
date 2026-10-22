package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Module is one module version.
type Module struct{ Path, Version string }

func (m Module) String() string { return m.Path + "@" + m.Version }

// modInfo is a loaded go.mod.
type modInfo struct {
	raw  []byte
	file *ModFile
	err  error
}

// mods loads and caches go.mod files from the proxy, at most parallel at a time.
type mods struct {
	proxy *Proxy
	mu    sync.Mutex
	cache map[Module]*modInfo
	wait  map[Module]chan struct{}
}

func newMods(p *Proxy) *mods {
	return &mods{proxy: p, cache: map[Module]*modInfo{}, wait: map[Module]chan struct{}{}}
}

// get returns the go.mod of m (loading it once; concurrent callers wait for the first).
func (ms *mods) get(ctx context.Context, m Module) *modInfo {
	ms.mu.Lock()
	if mi, ok := ms.cache[m]; ok {
		ms.mu.Unlock()
		return mi
	}
	if ch, ok := ms.wait[m]; ok {
		ms.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return &modInfo{err: ctx.Err()}
		}
		return ms.get(ctx, m)
	}
	ch := make(chan struct{})
	ms.wait[m] = ch
	ms.mu.Unlock()

	mi := &modInfo{}
	mi.raw, mi.err = ms.proxy.Mod(ctx, m)
	if mi.err == nil {
		mi.file, mi.err = ParseModFile(mi.raw)
		if mi.err != nil {
			mi.err = fmt.Errorf("%s: %w", m, mi.err)
		} else if mi.file.Module != "" && mi.file.Module != m.Path {
			mi.err = fmt.Errorf("%s: its go.mod declares module %s", m, mi.file.Module)
		}
	}
	ms.mu.Lock()
	if mi.err == nil || !errors.Is(mi.err, context.Canceled) {
		ms.cache[m] = mi
	}
	delete(ms.wait, m)
	ms.mu.Unlock()
	close(ch)
	return mi
}

// modGraph is the module graph cmd/go builds from a main module's requirements
// (modload.readModGraph): the roots' go.mod files are loaded; below a module with an
// unpruned go.mod (go < 1.17, or reached through one) every requirement is loaded too; the
// requirements of pruned modules are graph nodes whose own requirements are not read.
type modGraph struct {
	selected map[string]string // path → highest reachable version
	loaded   map[Module]bool   // go.mod files read to build the graph
}

// fetchErr is a go.mod that could not be loaded while building a graph.
type fetchErr struct {
	mod Module
	err error
}

const graphParallel = 16

// buildGraph builds the module graph of a main module with the given requirements.
// mainPruned is false for a main module with go < 1.17. Load failures are returned with the
// partial graph (the toolchain check runs on what could be read).
func buildGraph(ctx context.Context, ms *mods, roots []Module, mainPruned bool) (*modGraph, []fetchErr) {
	g := &modGraph{selected: map[string]string{}, loaded: map[Module]bool{}}
	type item struct {
		m        Module
		unpruned bool
	}
	seen := map[item]bool{}
	reach := func(m Module) {
		if Compare(m.Version, g.selected[m.Path]) > 0 || g.selected[m.Path] == "" {
			g.selected[m.Path] = m.Version
		}
	}
	var queue []item
	for _, r := range roots {
		reach(r)
		it := item{r, !mainPruned}
		if !seen[it] {
			seen[it] = true
			queue = append(queue, it)
		}
	}
	var errs []fetchErr
	for len(queue) > 0 && ctx.Err() == nil {
		level := queue
		queue = nil
		infos := make([]*modInfo, len(level))
		var wg sync.WaitGroup
		sem := make(chan struct{}, graphParallel)
		for i, it := range level {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, m Module) {
				defer func() {
					if v := recover(); v != nil {
						infos[i] = &modInfo{err: fmt.Errorf("internal error loading %s: %v", m, v)}
					}
					<-sem
					wg.Done()
				}()
				infos[i] = ms.get(ctx, m)
			}(i, it.m)
		}
		wg.Wait()
		for i, it := range level {
			mi := infos[i]
			if mi.err != nil {
				errs = append(errs, fetchErr{it.m, mi.err})
				continue
			}
			g.loaded[it.m] = true
			next := it.unpruned || !mi.file.Pruned()
			for _, r := range mi.file.Require {
				reach(r)
				if !next {
					continue
				}
				ni := item{r, true}
				if !seen[ni] {
					seen[ni] = true
					queue = append(queue, ni)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, fetchErr{err: err})
	}
	return g, errs
}

// list returns the selected versions sorted by path.
func (g *modGraph) list() []Module {
	out := make([]Module, 0, len(g.selected))
	for p, v := range g.selected {
		out = append(out, Module{p, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
