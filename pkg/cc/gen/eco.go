package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"

	// The C/C++ API registers the cc.* kinds the IR names.
	_ "github.com/megakuul/bob/pkg/cc"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem is the C/C++ ecosystem (SPEC §03.1): engine.Ecosystem and engine.LockFreshness.
type Ecosystem struct{}

// Name is the bob.lock section key and the kind prefix.
func (Ecosystem) Name() string { return lock.SectionCC }

// Plan turns the C/C++ nodes into derivations: one cc-compile-link per library, program and
// test (plus the test's run), one cc-external per External.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("cc planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return engine.PlanResult{}, fmt.Errorf("cc planner: no graph")
	}
	return newPlanner(ctx, req).plan()
}

// Native returns compile_commands.json (§03.1.4).
func (Ecosystem) Native(ctx context.Context, req engine.PlanRequest) (files []engine.GeneratedFile, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("cc planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return nil, nil
	}
	return compileCommands(ctx, req)
}

// external is one cc.External declaration of the repository: what its lock entry must match.
type external struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Strip int    `json:"strip"`
	name  string
	pos   ir.Pos
}

// externals lists the repository's own Externals of g, sorted by label.
func externals(g *ir.Graph) ([]external, error) {
	seen := map[string]bool{}
	var out []external
	for _, n := range g.Nodes {
		if n.Kind != ccspec.KindExternal || !strings.HasPrefix(n.Label, "//") || seen[n.Label] {
			continue
		}
		var x ccspec.External
		if err := json.Unmarshal(n.Spec, &x); err != nil {
			return nil, fmt.Errorf("%s: external spec: %w", n.Label, err)
		}
		seen[n.Label] = true
		out = append(out, external{Label: n.Label, URL: x.URL, Strip: x.Strip, name: x.Name, pos: n.DefinedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// Lock pins every External (R-CC-005a): bob downloads the URL, unpacks it with Strip,
// normalises modes and records the narHash. Entries whose URL and Strip did not change keep
// their pin unless bob lock --update names them (by label or by name).
func (Ecosystem) Lock(ctx context.Context, req engine.LockRequest) (json.RawMessage, error) {
	decls, err := externals(req.Graph)
	if err != nil {
		return nil, err
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		return nil, err
	}
	prev, err := lockSection(req.Prev)
	if err != nil {
		prev, _ = lockSection(nil) // an unreadable section is re-resolved
	}
	sec := &lock.CCSection{Inputs: inputs, Externals: map[string]lock.CExternal{}}
	for _, d := range decls {
		old, ok := prev.Externals[d.Label]
		if ok && old.URL == d.URL && old.Strip == d.Strip && !slices.Contains(req.Update, d.Label) && !slices.Contains(req.Update, d.name) {
			sec.Externals[d.Label] = old
			continue
		}
		if req.Fetch == nil {
			return nil, fmt.Errorf("%s: no fetcher to pin %s", d.Label, d.URL)
		}
		_, narHash, err := req.Fetch.Resolve(ctx, fetch.Request{URLs: []string{d.URL}, Kind: fetch.Archive,
			Name: drv.Name("ccx-src", d.name), Strip: d.Strip, Label: d.Label})
		if err != nil {
			return nil, err
		}
		sec.Externals[d.Label] = lock.CExternal{URL: d.URL, Strip: d.Strip, NarHash: narHash}
	}
	return lock.EncodeSection(lock.SectionCC, sec)
}

// Stale reports a section resolved from other Externals than the graph declares (R-FW-062).
// Only the declarations are compared (the section's inputs); a declared External whose entry
// is missing while the declarations match is refused by Plan (BOB-E-1009, R-CC-005a).
func (Ecosystem) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	decls, err := externals(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		return "", ir.Pos{}, err
	}
	sec, err := lockSection(req.Prev)
	if err != nil {
		return "the cc section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	if sec.Inputs == inputs {
		return "", ir.Pos{}, nil
	}
	for _, d := range decls {
		e, ok := sec.Externals[d.Label]
		switch {
		case !ok:
			return fmt.Sprintf("cc.External %s is not locked", d.Label), d.pos, nil
		case e.URL != d.URL:
			return fmt.Sprintf("cc.External %s changed its URL to %s", d.Label, d.URL), d.pos, nil
		case e.Strip != d.Strip:
			return fmt.Sprintf("cc.External %s changed Strip to %d", d.Label, d.Strip), d.pos, nil
		}
	}
	return "the cc.External declarations changed", ir.Pos{}, nil
}
