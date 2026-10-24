package build

import (
	"context"
	"sort"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Request is one run of the pipeline up to source import (SourcePaths).
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns
	Home     string   // state home ("" = default)
}

// SourcePaths runs the pipeline up to source import (R-FW-049) and returns, per IR node id,
// the content-addressed source store path the ecosystems receive in PlanRequest.Sources
// (R-FW-021, R-NIX-009).
func SourcePaths(ctx context.Context, r Request) (map[string]string, error) {
	st, err := openStore(ctx, nil, r.Home, nil)
	if err != nil {
		return nil, err
	}
	g, _, err := evaluate(ctx, st, Options{Root: r.Root, Patterns: r.Patterns})
	if err != nil {
		return nil, err
	}
	return importSources(ctx, st, r.Root, g)
}

// Evaluate runs scan, eval and ir only (bob graph, bob check): the validated graph. Errors are
// *diag.Error or ir.Errors. With Options.Events it reports the scan and eval stage events
// (R-FW-049) like Run, so `bob graph --json` shows whether the evaluator was reused (R-FW-043).
func Evaluate(ctx context.Context, o Options) (*ir.Graph, error) {
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return nil, err
	}
	r := &runner{o: o}
	g, res, err := r.evaluate(ctx, st)
	if o.Events != nil && res != nil {
		// The eval stage ends where IR validation starts, as in Run (R-FW-049: scan, eval, ir).
		o.Events(Event{Event: StageScan, DurationMs: res.Scan.Milliseconds()})
		o.Events(Event{Event: StageEval, DurationMs: (r.evalTook - res.Scan).Milliseconds(), Cached: res.Cached})
	}
	return g, err
}

// CheckDeterminism evaluates twice with the evaluator's Go of Evaluate and returns BOB-E-1018
// (as ir.Errors) when the two IRs differ (bob check --determinism, R-FW-008).
func CheckDeterminism(ctx context.Context, o Options) error {
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return err
	}
	r := &runner{o: o}
	goRoot, err := r.evalGoRoot(ctx, st, fetch.New(st, fetch.Policy{}))
	if err != nil {
		return err
	}
	return eval.CheckDeterminism(ctx, evalRequest(o, goRoot))
}

// evaluate runs the evaluator (with the pinned Go) and validates its IR.
func evaluate(ctx context.Context, st *store.Store, o Options) (*ir.Graph, *eval.Result, error) {
	return (&runner{o: o}).evaluate(ctx, st)
}

func (r *runner) evaluate(ctx context.Context, st *store.Store) (*ir.Graph, *eval.Result, error) {
	o := r.o
	t := time.Now()
	goRoot, err := r.evalGoRoot(ctx, st, fetch.New(st, fetch.Policy{}))
	if err != nil {
		return nil, nil, err
	}
	req := evalRequest(o, goRoot)
	res, err := eval.Evaluate(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	r.evaluated = &evaluation{goRoot: goRoot, req: req, res: res}
	r.evalTook = time.Since(t)
	g, err := ir.Parse(res.IR, eval.BobVersion())
	return g, res, err
}

// requestedLabels are the labels of the graph's roots, sorted and unique.
func requestedLabels(g *ir.Graph) []string {
	byID := map[string]*ir.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range g.Roots {
		if n, ok := byID[id]; ok && !seen[n.Label] {
			seen[n.Label] = true
			out = append(out, n.Label)
		}
	}
	sort.Strings(out)
	return out
}

// importSources resolves every node's source set (R-FW-021), imports the trees in one batch
// (R-NIX-009) and returns node id → source store path. Nodes with the same label share their
// sources (sources do not depend on the configuration).
func importSources(ctx context.Context, st *store.Store, root string, g *ir.Graph) (map[string]string, error) {
	var list []srcs.Source
	var ids [][]string
	index := map[string]int{} // label → position in list
	for _, n := range g.Nodes {
		if len(n.Srcs) == 0 {
			continue
		}
		if i, ok := index[n.Label]; ok {
			ids[i] = append(ids[i], n.ID)
			continue
		}
		files, err := srcs.ResolveAll(root, n.Srcs, n.DefinedAt.String(), nil)
		if err != nil {
			return nil, err
		}
		index[n.Label] = len(list)
		list = append(list, srcs.Source{Name: storeName(&ir.Node{Label: n.Label}) + "-src", Tree: srcs.Tree{Root: root, Files: files}})
		ids = append(ids, []string{n.ID})
	}
	if len(list) == 0 {
		return map[string]string{}, nil
	}
	paths, err := srcs.Import(ctx, st, list)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, p := range paths {
		for _, id := range ids[i] {
			out[id] = p
		}
	}
	return out, nil
}

// srcDirs maps each imported source path to its repo path for the translator (§02.5 rule 3):
// the trees keep repo-relative paths, so "<src>/libs/foo/x.cc" becomes "libs/foo/x.cc".
func srcDirs(paths map[string]string) map[string]string {
	out := map[string]string{}
	for _, p := range paths {
		out[p+"/"] = ""
		out[p] = "."
	}
	return out
}
