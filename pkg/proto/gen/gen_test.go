package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/proto/internal/protospec"
)

type resolver struct{}

func (resolver) Modulo(string) ([32]byte, error) { return [32]byte{1}, nil }

// Libraries resolve to their imported sources; Lint is a test unit ("test-run", so bob build
// leaves it out) that runs buf lint on the library's directory with its imports laid out at
// their repository paths.
func TestPlanLibraryAndLint(t *testing.T) {
	lib := func(id, dir string, deps ...string) *ir.Node {
		raw, _ := json.Marshal(protospec.Library{Root: "proto", Dir: dir})
		return &ir.Node{ID: id, Kind: protospec.KindLibrary, Label: "//" + dir + ":proto", Spec: raw, Deps: deps}
	}
	common, greet := lib("common", "proto/common/v1"), lib("greet", "proto/greet/v1", "common")
	raw, _ := json.Marshal(protospec.Check{Mode: protospec.Lint})
	lint := &ir.Node{ID: "lint", Kind: protospec.KindCheck, Label: "//proto/greet/v1:lint", Spec: raw, Deps: []string{"greet", "buf"}}
	g := &ir.Graph{Nodes: []*ir.Node{common, greet, lint}}
	req := engine.PlanRequest{Graph: g, Nodes: []*ir.Node{common, greet}, Resolver: resolver{},
		Builder: "/nix/store/0000000000000000000000000000000b-bob-builder",
		Sources: map[string]string{"common": "/nix/store/cccccccccccccccccccccccccccccccc-source", "greet": "/nix/store/gggggggggggggggggggggggggggggggg-source"}}
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Nodes["greet"].Outputs["out"] != req.Sources["greet"] || res.Nodes["greet"].Drv != "" {
		t.Fatalf("library resolved to %+v", res.Nodes["greet"])
	}
	req.Nodes = []*ir.Node{lint}
	req.Inputs = map[string]engine.Resolved{"common": res.Nodes["common"], "greet": res.Nodes["greet"],
		"buf": {Drv: "/nix/store/dddddddddddddddddddddddddddddddd-buf.drv", Outputs: map[string]string{"out": "/nix/store/oooooooooooooooooooooooooooooooo-buf"}}}
	res, err = Ecosystem{}.Plan(context.Background(), req)
	if err != nil || len(res.Drvs) != 1 {
		t.Fatalf("lint: %v, %d drvs", err, len(res.Drvs))
	}
	d := res.Drvs[0]
	if len(d.Args) != 1 || d.Args[0] != "test-run" {
		t.Fatalf("lint is not a test unit: args %v", d.Args)
	}
	spec, _ := d.Structured["bobSpec"].(map[string]any)
	b, _ := json.Marshal(spec["args"])
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var a bspec.TestRunArgs
	if err := dec.Decode(&a); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Argv, " "); got != `buf lint --config {"modules":[{"path":"proto"}],"version":"v2"} --path proto/greet/v1` {
		t.Errorf("argv %s", got)
	}
	if len(a.Copy) != 2 || a.Copy[0].From != req.Sources["common"] || a.Copy[1].To != buildSrc || a.Cwd != buildSrc {
		t.Errorf("copy %+v cwd %q", a.Copy, a.Cwd)
	}
}

func TestBufConfigRoot(t *testing.T) {
	if got := BufConfig(""); got != `{"modules":[{"path":"."}],"version":"v2"}` {
		t.Errorf("BufConfig(\"\") = %s", got)
	}
}
