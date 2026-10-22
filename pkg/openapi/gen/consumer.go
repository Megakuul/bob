package gen

import (
	"encoding/json"
	"fmt"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// consumer is the Go module or npm importer that realises a codegen node.
type consumer struct {
	Kind  string `json:"kind"`  // go.module, npm.lib, npm.app, npm.site
	Label string `json:"label"` // its label
	Dir   string `json:"dir"`   // the owned directory relative to the consumer
	Owned string `json:"owned"` // the owned directory, repository-relative
	Path  string `json:"path"`  // Go: the module path
	Go    string `json:"go"`    // Go: the module's Go version
}

var consumerKinds = []string{"go.module", "npm.lib", "npm.app", "npm.site"}

// consumerOf returns the consumer of n: recorded in n's spec by the consumer (so it is known
// when only the codegen package is built), else the node of the graph that lists n.
func consumerOf(g *ir.Graph, n *ir.Node) (consumer, error) {
	var w struct {
		Consumer *consumer `json:"consumer"`
	}
	if json.Unmarshal(n.Spec, &w) == nil && w.Consumer != nil && w.Consumer.Owned != "" {
		return *w.Consumer, nil
	}
	m := engine.ConsumerOf(g, n.ID, consumerKinds...)
	if m == nil {
		return consumer{}, fmt.Errorf("%s: no Go module or npm importer realises it", n.Label)
	}
	var s struct {
		Path      string `json:"path"`
		Go        string `json:"go"`
		Generated []struct {
			Dir string `json:"dir"`
			Dep int    `json:"dep"`
		} `json:"generated"`
	}
	if err := json.Unmarshal(m.Spec, &s); err != nil {
		return consumer{}, fmt.Errorf("%s: spec: %w", m.Label, err)
	}
	for _, gen := range s.Generated {
		if gen.Dep < 0 || gen.Dep >= len(m.Deps) || m.Deps[gen.Dep] != n.ID {
			continue
		}
		owned, err := engine.OwnedDir(engine.LabelDir(m.Label), gen.Dir)
		if err != nil {
			return consumer{}, fmt.Errorf("%s: %w", n.Label, err)
		}
		return consumer{Kind: m.Kind, Label: m.Label, Dir: gen.Dir, Owned: owned, Path: s.Path, Go: s.Go}, nil
	}
	return consumer{}, fmt.Errorf("%s: %s does not list it as generated", n.Label, m.Label)
}
