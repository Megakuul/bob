package gen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// member is one local crate of the graph (a Cargo workspace member).
type member struct {
	node *ir.Node // the first node of the crate's label (any configuration)
	spec rsspec.Crate
	dir  string // repository-relative crate directory ("" = the root)
	// local are the crate names of the node's local dependencies by dep index.
	local map[int]string
}

// workspace is every local crate of a graph, by crate name.
type workspace struct {
	members map[string]*member
	byID    map[string]*member // node id → member (every configuration)
	names   []string           // sorted crate names
}

// labelDir returns the directory part of a label ("//a/b:c" → "a/b"; "@m//a:b" → "a").
func labelDir(label string) string {
	_, rest, _ := strings.Cut(label, "//")
	d, _, _ := strings.Cut(rest, ":")
	return d
}

// readWorkspace collects the local crates of g. Two crates with one name are an error (Cargo
// identifies workspace members by name).
func readWorkspace(g *ir.Graph) (*workspace, error) {
	ws := &workspace{members: map[string]*member{}, byID: map[string]*member{}}
	byLabel := map[string]*member{}
	ids := map[string]*ir.Node{}
	for _, n := range g.Nodes {
		ids[n.ID] = n
	}
	nodes := append([]*ir.Node(nil), g.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	for _, n := range nodes {
		if n.Kind != rsspec.KindCrate {
			continue
		}
		if m, ok := byLabel[n.Label]; ok {
			ws.byID[n.ID] = m
			continue
		}
		var s rsspec.Crate
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return nil, fmt.Errorf("%s: crate spec: %w", n.Label, err)
		}
		m := &member{node: n, spec: s, dir: labelDir(n.Label), local: map[int]string{}}
		if other, dup := ws.members[s.Name]; dup {
			return nil, fmt.Errorf("%s and %s are both named %q; Rust crates of one repository need distinct names (set Name)", other.node.Label, n.Label, s.Name)
		}
		byLabel[n.Label] = m
		ws.members[s.Name] = m
		ws.byID[n.ID] = m
	}
	for _, m := range ws.members {
		for _, ds := range [][]rsspec.Dep{m.spec.Deps, m.spec.BuildDeps, m.spec.DevDeps} {
			for _, d := range ds {
				if d.Local < 0 {
					continue
				}
				if d.Local >= len(m.node.Deps) {
					return nil, fmt.Errorf("%s: local dependency index out of range", m.node.Label)
				}
				dn := ids[m.node.Deps[d.Local]]
				if dn == nil || dn.Kind != rsspec.KindCrate {
					return nil, fmt.Errorf("%s: dependency %s is not a Rust crate", m.node.Label, m.node.Deps[d.Local])
				}
				var ds rsspec.Crate
				if err := json.Unmarshal(dn.Spec, &ds); err != nil {
					return nil, fmt.Errorf("%s: crate spec: %w", dn.Label, err)
				}
				m.local[d.Local] = ds.Name
			}
		}
	}
	for name := range ws.members {
		ws.names = append(ws.names, name)
	}
	sort.Strings(ws.names)
	return ws, nil
}

// resolveMembers returns the members as the resolver sees them: dependencies in Cargo
// manifest order ([dependencies], [dev-dependencies], [build-dependencies], each by name).
func (ws *workspace) resolveMembers() ([]resolve.Member, error) {
	var out []resolve.Member
	for _, name := range ws.names {
		m := ws.members[name]
		rm := resolve.Member{Name: name, Version: "0.0.0", Features: featureTable(m.spec), ProcMacro: m.spec.Type == rsspec.TypeProcMacro}
		for _, sec := range []struct {
			kind string
			deps []rsspec.Dep
		}{{"", m.spec.Deps}, {"dev", m.spec.DevDeps}, {"build", m.spec.BuildDeps}} {
			deps := append([]rsspec.Dep(nil), sec.deps...)
			sort.SliceStable(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
			for _, d := range deps {
				rd := resolve.Dep{Name: d.Name, Kind: sec.kind, Default: !d.NoDefault, Features: append([]string(nil), d.Features...)}
				if d.Local >= 0 {
					rd.Local, rd.Package = true, m.local[d.Local]
				} else {
					req, err := resolve.ParseReq(d.Req)
					if err != nil {
						return nil, fmt.Errorf("%s: rust.Crate(%q, %q): %w", m.node.Label, d.Crate, d.Req, err)
					}
					rd.Package, rd.Req = d.Crate, req
				}
				rm.Deps = append(rm.Deps, rd)
			}
		}
		out = append(out, rm)
	}
	return out, nil
}

// featureTable is a crate's [features] table including "default".
func featureTable(s rsspec.Crate) map[string][]string {
	t := map[string][]string{}
	for k, v := range s.Features {
		t[k] = append([]string{}, v...)
	}
	if len(s.Default) > 0 {
		t["default"] = append([]string{}, s.Default...)
	}
	return t
}

// declaration is what the rust section is resolved from (R-FW-062): every member with its
// dependencies and features, and the toolchain version the resolution prefers.
type declaration struct {
	Members []declMember `json:"members"`
	Rust    string       `json:"rust"`
}

type declMember struct {
	Name     string              `json:"name"`
	Features map[string][]string `json:"features"`
	Deps     []declDep           `json:"deps"`
}

type declDep struct {
	Name      string   `json:"name"`
	Package   string   `json:"package"`
	Req       string   `json:"req,omitempty"`
	Local     bool     `json:"local,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	NoDefault bool     `json:"noDefault,omitempty"`
	Features  []string `json:"features"`
}

func declare(members []resolve.Member, rust string) declaration {
	d := declaration{Members: []declMember{}, Rust: rust}
	for _, m := range members {
		dm := declMember{Name: m.Name, Features: m.Features, Deps: []declDep{}}
		for _, x := range m.Deps {
			dd := declDep{Name: x.Name, Package: x.Package, Local: x.Local, Kind: x.Kind, NoDefault: !x.Default, Features: x.Features}
			if !x.Local {
				dd.Req = x.Req.String()
			}
			if dd.Features == nil {
				dd.Features = []string{}
			}
			dm.Deps = append(dm.Deps, dd)
		}
		d.Members = append(d.Members, dm)
	}
	return d
}
