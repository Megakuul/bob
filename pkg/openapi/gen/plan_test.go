package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/openapi/internal/oaspec"
)

const (
	specSrc  = "/nix/store/ssssssssssssssssssssssssssssssss-schema-openapi-items-src"
	oapiPath = "/nix/store/oooooooooooooooooooooooooooooooo-oapi-codegen-2.5.1"
	builder  = "/nix/store/0000000000000000000000000000000b-bob-builder"
)

type fakeStore struct{}

func (fakeStore) HostPath(p string) string { return filepath.Join("/nonexistent", filepath.Base(p)) }
func (fakeStore) ImportSources(context.Context, []srcs.Source) ([]string, error) {
	return nil, nil
}
func (fakeStore) Ensure(context.Context, fetch.Request) (string, error) { return "", nil }

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// graph is the codegen fixture's OpenAPI part: //schema:openapi-items realised by the Go
// module //svc:module as //svc:gen-rest.
func graph(t *testing.T, g oaspec.Go) (engine.PlanRequest, *ir.Node) {
	t.Helper()
	spec := &ir.Node{ID: "spec", Kind: oaspec.KindSpec, Label: "//schema:openapi-items", Spec: mustJSON(t, oaspec.Spec{File: "schema/items.yaml"})}
	tool := &ir.Node{ID: "tool", Kind: "nixpkgs.tool", Label: "//svc:oapi-codegen"}
	gen := &ir.Node{ID: "gen", Kind: oaspec.KindGo, Label: "//svc:gen-rest", Deps: []string{"spec", "tool"}, Spec: mustJSON(t, g)}
	mod := &ir.Node{ID: "mod", Kind: "go.module", Label: "//svc:module", Deps: []string{"go", "gen"},
		Spec: json.RawMessage(`{"path":"example.com/svc","go":"1.26","generated":[{"dir":"gen/rest","dep":1}]}`)}
	req := engine.PlanRequest{Graph: &ir.Graph{Nodes: []*ir.Node{spec, tool, gen, mod}}, Nodes: []*ir.Node{spec, gen},
		Sources: map[string]string{"spec": specSrc},
		Inputs: map[string]engine.Resolved{"spec": {Outputs: map[string]string{"out": specSrc}},
			"tool": {Outputs: map[string]string{"out": oapiPath}}},
		Builder: builder, Store: fakeStore{}}
	return req, gen
}

// steps decodes the steps of a "run" job strictly into bob-builder's types.
func steps(t *testing.T, d *drv.Derivation) []bspec.Step {
	t.Helper()
	spec, _ := d.Structured["bobSpec"].(map[string]any)
	if spec["kind"] != "run" {
		t.Fatalf("job kind %v, want run", spec["kind"])
	}
	dec := json.NewDecoder(bytes.NewReader(mustJSON(t, spec["args"])))
	dec.DisallowUnknownFields()
	var a bspec.RunArgs
	if err := dec.Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a.Steps
}

// R-GEN-003a: openapi.Go is one oapi-codegen job: the document at its repository path, a
// go.mod with the module's path and Go version, bob's config with the std-http server and
// models, the output in $out; its output becomes svc/gen/rest.
func TestPlanGo(t *testing.T) {
	req, gen := graph(t, oaspec.Go{Spec: 0, Tool: 1, Package: "rest", Server: oaspec.ServerStdHTTP, Models: true})
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drvs) != 1 || res.Nodes["spec"].Outputs["out"] != specSrc {
		t.Fatalf("plan: %d drvs, spec %v", len(res.Drvs), res.Nodes["spec"])
	}
	if len(res.Materialise) != 1 || res.Materialise[0].Dir != "svc/gen/rest" || res.Materialise[0].Label != gen.Label {
		t.Errorf("materialise %+v", res.Materialise)
	}
	out := drv.Placeholder("out")
	var writes = map[string]string{}
	var argv []string
	for _, s := range steps(t, res.Drvs[0]) {
		switch s.Op {
		case "write":
			var w bspec.WriteArgs
			_ = json.Unmarshal(s.Args, &w)
			writes[w.Path] = w.Content
		case "exec":
			var e bspec.ExecArgs
			_ = json.Unmarshal(s.Args, &e)
			argv = e.Argv
			if e.Cwd != buildSrc || len(e.Path) != 1 || e.Path[0] != oapiPath+"/bin" {
				t.Errorf("exec %+v", e)
			}
		}
	}
	if got := writes[buildSrc+"/go.mod"]; got != "module example.com/svc\n\ngo 1.26\n" {
		t.Errorf("go.mod %q", got)
	}
	cfg := writes[buildCfg+"/oapi-codegen.yaml"]
	for _, want := range []string{"package: rest\n", "  std-http-server: true\n", "  models: true\n", "output: " + out + "/items.gen.go\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "client") {
		t.Errorf("config generates a client:\n%s", cfg)
	}
	if strings.Join(argv, " ") != "oapi-codegen -config "+buildCfg+"/oapi-codegen.yaml schema/items.yaml" {
		t.Errorf("argv %q", argv)
	}
	inputs := strings.Join(res.Drvs[0].InputSrcs, " ")
	if !strings.Contains(inputs, specSrc) || !strings.Contains(inputs, oapiPath) {
		t.Errorf("inputs %s", inputs)
	}
}

// The codegen node needs its consumer: without a Go module realising it there is nothing to
// plan; an npm importer cannot realise Go code.
func TestPlanGoConsumer(t *testing.T) {
	req, _ := graph(t, oaspec.Go{Spec: 0, Tool: 1, Package: "rest", Models: true})
	req.Graph.Nodes = req.Graph.Nodes[:3]
	if _, err := (Ecosystem{}).Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "no Go module or npm importer realises it") {
		t.Errorf("no consumer: %v", err)
	}
	req, _ = graph(t, oaspec.Go{Spec: 0, Tool: 1, Package: "rest", Models: true})
	req.Graph.Nodes[3].Kind = "npm.app"
	if _, err := (Ecosystem{}).Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "not a Go module") {
		t.Errorf("npm consumer: %v", err)
	}
}

// Planning is deterministic: the same request gives the same derivation.
func TestPlanDeterministic(t *testing.T) {
	var paths []string
	for i := 0; i < 2; i++ {
		req, _ := graph(t, oaspec.Go{Spec: 0, Tool: 1, Package: "rest", Server: oaspec.ServerStdHTTP, Models: true, Client: true})
		res, err := Ecosystem{}.Plan(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		p, err := res.Drvs[0].Path()
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if paths[0] != paths[1] {
		t.Errorf("two plans: %v", paths)
	}
}
