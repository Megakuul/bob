package gen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// extNode is a cc.External node of the given label.
func extNode(t *testing.T, label, url string) *ir.Node {
	t.Helper()
	spec, err := json.Marshal(ccspec.External{Name: label[strings.LastIndex(label, ":")+1:], URL: url, Strip: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &ir.Node{ID: label, Kind: ccspec.KindExternal, Label: label, Spec: spec, DefinedAt: ir.Pos{File: "x/bob.go", Line: 3}}
}

// TestStalePartialGraph: a pattern that selects only part of the graph (`bob test
// //libs/mathx:mathx_test`) must not report BOB-E-1012 because the section also holds the
// Externals of packages outside it.
func TestStalePartialGraph(t *testing.T) {
	a, b := extNode(t, "//third_party/a:a", "https://example.org/a.tar.gz"), extNode(t, "//third_party/b:b", "https://example.org/b.tar.gz")
	full := &ir.Graph{Nodes: []*ir.Node{a, b}}
	decls, err := externals(full)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := lock.EncodeSection(lock.SectionCC, &lock.CCSection{Inputs: inputs, Externals: map[string]lock.CExternal{
		a.Label: {URL: "https://example.org/a.tar.gz", Strip: 1, NarHash: narHash},
		b.Label: {URL: "https://example.org/b.tar.gz", Strip: 1, NarHash: narHash}}})
	if err != nil {
		t.Fatal(err)
	}
	stale := func(g *ir.Graph) string {
		t.Helper()
		what, _, err := Ecosystem{}.Stale(context.Background(), engine.LockRequest{Graph: g, Prev: sec})
		if err != nil {
			t.Fatal(err)
		}
		return what
	}
	if w := stale(full); w != "" {
		t.Errorf("whole graph: stale %q", w)
	}
	if w := stale(&ir.Graph{Nodes: []*ir.Node{a}}); w != "" {
		t.Errorf("partial graph: stale %q", w)
	}
	if w := stale(&ir.Graph{}); w != "" {
		t.Errorf("graph without Externals: stale %q", w)
	}
	// Real changes are still found in a partial graph.
	if w := stale(&ir.Graph{Nodes: []*ir.Node{extNode(t, a.Label, "https://example.org/other.tar.gz")}}); !strings.Contains(w, "changed its URL") {
		t.Errorf("changed URL in a partial graph: %q", w)
	}
	if w := stale(&ir.Graph{Nodes: []*ir.Node{a, extNode(t, "//third_party/c:c", "https://example.org/c.tar.gz")}}); !strings.Contains(w, "is not locked") {
		t.Errorf("new External in a partial graph: %q", w)
	}
}
