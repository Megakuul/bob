package build

import (
	"errors"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// TestPickShell: --shell, else "default", else the only shell; an unknown name is BOB-E-1014
// listing the names; a repository without shells has an implicit default; two shells of one
// name are BOB-E-1020.
func TestPickShell(t *testing.T) {
	g := &ir.Graph{Nodes: []*ir.Node{
		{ID: "a", Kind: DevShellKind, Label: "//:default"},
		{ID: "b", Kind: DevShellKind, Label: "//:ci"},
		{ID: "b2", Kind: DevShellKind, Label: "//:ci"}, // a second configuration of the same label
		{ID: "c", Kind: "go.module", Label: "//:module"},
	}}
	shells, err := devShells(g)
	if err != nil || len(shells) != 2 {
		t.Fatalf("devShells = %v, %v", shells, err)
	}
	names := []string{"ci", "default"}
	for _, c := range []struct{ want, got string }{{"", "default"}, {"ci", "ci"}, {"default", "default"}} {
		n, err := pickShell(shells, names, c.want)
		if err != nil || n == nil || shellName(n) != c.got {
			t.Errorf("pickShell(%q) = %v, %v; want %s", c.want, n, err, c.got)
		}
	}
	_, err = pickShell(shells, names, "nope")
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1014 || d.Args["names"] != "ci, default" {
		t.Errorf("unknown shell: %v", err)
	}
	only := map[string]*ir.Node{"ci": shells["ci"]}
	if n, err := pickShell(only, []string{"ci"}, ""); err != nil || shellName(n) != "ci" {
		t.Errorf("the only shell: %v %v", n, err)
	}
	if n, err := pickShell(map[string]*ir.Node{}, nil, ""); n != nil || err != nil {
		t.Errorf("no shells: %v %v, want the implicit default", n, err)
	}
	if _, err := pickShell(only, []string{"ci"}, "x"); !errors.As(err, &d) || d.Code != 1014 {
		t.Errorf("no such shell among one: %v", err)
	}
	dup := &ir.Graph{Nodes: []*ir.Node{
		{ID: "a", Kind: DevShellKind, Label: "//:default"},
		{ID: "b", Kind: DevShellKind, Label: "//web:default", DefinedAt: ir.Pos{File: "web/bob.go", Line: 3}},
	}}
	if _, err := devShells(dup); !errors.As(err, &d) || d.Code != 1020 || d.Args["option"] != "Name" {
		t.Errorf("duplicate shell names: %v", err)
	}
}
