package bob

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

func init() {
	graph.Register(graph.Kind{Name: "nixpkgs.tool", Axes: []int{graph.AxisPlatform}, Host: true})
	graph.Register(graph.Kind{Name: "nixpkgs.pkg", Axes: []int{graph.AxisPlatform}})
}

type nixObj struct {
	Attr    string   `json:"attr"`
	Set     string   `json:"set"`
	Outputs []string `json:"outputs"`
	Modules []string `json:"modules,omitempty"`
	Allow   struct {
		Insecure []string `json:"insecure"`
		Unfree   bool     `json:"unfree"`
	} `json:"allow"`
}

func nixNode(p *P, kind, attr, set string, modules ...string) *thing {
	o := nixObj{Attr: attr, Set: set, Outputs: []string{}, Modules: modules}
	o.Allow.Insecure = []string{}
	return &thing{Node: newNode(p, kind, attr, nil, nil, o)}
}

// F3 / A91: one nixpkgs attribute used as Tool and Lib, or as two Libs with different Modules,
// on the native platform (host == target) gives distinct nodes, not a definition error.
func TestNixpkgsSameAttrDifferentUse(t *testing.T) {
	isolate(t)
	Define(func(p *P) *thing {
		return mk(p, "test.thing", "a",
			nixNode(p, "nixpkgs.tool", "protobuf", "host"),
			nixNode(p, "nixpkgs.lib", "protobuf", "target"),
			nixNode(p, "nixpkgs.pkg", "protobuf", "target"))
	})
	Define(func(p *P) *thing {
		return mk(p, "test.thing", "b", nixNode(p, "nixpkgs.lib", "openssl", "target", "libcrypto"))
	})
	Define(func(p *P) *thing {
		return mk(p, "test.thing", "c",
			nixNode(p, "nixpkgs.lib", "openssl", "target", "libssl"),
			nixNode(p, "nixpkgs.lib", "openssl", "cross"),
			nixNode(p, "nixpkgs.lib", "openssl", "target"))
	})
	d := decode(t, evalOK(t, "//..."))
	ids := map[string]bool{}
	for _, n := range d.Nodes {
		if strings.HasPrefix(n.ID, "nixpkgs:") {
			if ids[n.ID] {
				t.Errorf("duplicate id %s", n.ID)
			}
			ids[n.ID] = true
		}
	}
	for _, want := range []string{
		"nixpkgs:protobuf@linux-arm64+tool",
		"nixpkgs:protobuf@linux-arm64+lib",
		"nixpkgs:protobuf@linux-arm64+pkg",
		"nixpkgs:openssl@linux-arm64+lib",
		"nixpkgs:openssl@linux-arm64+lib+cross",
	} {
		if !ids[want] {
			t.Errorf("node %s missing; ids %v", want, ids)
		}
	}
	if len(ids) != 7 {
		t.Errorf("want 7 nixpkgs nodes (two openssl Modules variants hashed), got %d: %v", len(ids), ids)
	}

	// Identical requests from two packages are still one node.
	isolate(t)
	Define(func(p *P) *thing {
		return mk(p, "test.thing", "a", nixNode(p, "nixpkgs.lib", "zlib", "target", "z"))
	})
	Define(func(p *P) *thing {
		return mk(p, "test.thing", "b", nixNode(p, "nixpkgs.lib", "zlib", "target", "z"))
	})
	n := 0
	for _, nd := range decode(t, evalOK(t, "//...")).Nodes {
		if strings.HasPrefix(nd.ID, "nixpkgs:zlib@linux-arm64+lib+n-") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("identical Lib requests: %d nodes, want 1", n)
	}
}
