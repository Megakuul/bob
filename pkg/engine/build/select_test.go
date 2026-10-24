package build

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// A requested test set stands for its units (bob test //lib:tests); a requested unit or a
// requested non-test package adds nothing.
func TestWithSetUnits(t *testing.T) {
	unit := &drv.Derivation{Args: []string{testrun.Kind}}
	lib := &drv.Derivation{Args: []string{"build"}}
	g := &ir.Graph{
		Nodes: []*ir.Node{
			{ID: "set", Label: "//lib:tests", Deps: []string{"u1", "u2"}},
			{ID: "u1", Label: "//lib/a:go_test", Deps: []string{"l"}},
			{ID: "u2", Label: "//lib/b:go_test", Deps: []string{"l"}},
			{ID: "l", Label: "//lib/a:a"},
			{ID: "bin", Label: "//cmd:bin", Deps: []string{"l"}},
		},
	}
	p := &planner{nodes: map[string]engine.Resolved{}, byPath: map[string]*drv.Derivation{}}
	for _, n := range g.Nodes {
		path := "/nix/store/" + n.ID + ".drv"
		p.nodes[n.ID] = engine.Resolved{Drv: path}
		p.byPath[path] = lib
		if n.ID == "u1" || n.ID == "u2" {
			p.byPath[path] = unit
		}
	}
	for _, c := range []struct {
		roots []string
		req   []string
		want  []string
	}{
		{[]string{"set"}, []string{"//lib:tests"}, []string{"//lib/a:go_test", "//lib/b:go_test", "//lib:tests"}},
		{[]string{"u1"}, []string{"//lib/a:go_test"}, []string{"//lib/a:go_test"}},
		{[]string{"set", "u1"}, []string{"//lib/a:go_test", "//lib:tests"}, []string{"//lib/a:go_test", "//lib/b:go_test", "//lib:tests"}},
		{[]string{"bin"}, []string{"//cmd:bin"}, []string{"//cmd:bin"}},
	} {
		g.Roots = c.roots
		if got := withSetUnits(g, p, c.req); !slices.Equal(got, c.want) {
			t.Errorf("roots %v: %v, want %v", c.roots, got, c.want)
		}
	}
}

// A failure seen after the interrupt is the cancelled run, never a bob bug.
func TestInterrupted(t *testing.T) {
	bug := diag.New(9004, "label", "//x:y", "reason", "context canceled")
	if err := interrupted(context.Background(), bug); err != bug {
		t.Errorf("live context: %v, want the error unchanged", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := interrupted(ctx, bug); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v, want context.Canceled", err)
	}
	if err := interrupted(ctx, nil); err != nil {
		t.Errorf("cancelled context, no error: %v", err)
	}
}

func TestWholeRepo(t *testing.T) {
	for _, c := range []struct {
		p    []string
		want bool
	}{{nil, true}, {[]string{"//..."}, true}, {[]string{"//lib/..."}, false}, {[]string{"//...", "//x:y"}, false}} {
		if got := wholeRepo(c.p); got != c.want {
			t.Errorf("%q: %v, want %v", c.p, got, c.want)
		}
	}
}
