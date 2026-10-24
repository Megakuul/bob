package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/graphql/internal/gqlspec"
)

const (
	schemaSrc  = "/nix/store/ssssssssssssssssssssssssssssssss-schema-graphql-src"
	gqlgenPath = "/nix/store/qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq-gqlgen-0.17.90"
	goPath     = "/nix/store/gggggggggggggggggggggggggggggggg-go-1.26.8"
	builder    = "/nix/store/0000000000000000000000000000000b-bob-builder"
)

// fakeStore maps store paths to directories of a temporary tree; Ensure "fetches" a module
// tree by returning the path its lock entry pins.
type fakeStore struct{ root string }

func (s fakeStore) HostPath(p string) string { return filepath.Join(s.root, filepath.Base(p)) }
func (fakeStore) ImportSources(context.Context, []srcs.Source) ([]string, error) {
	return nil, nil
}
func (fakeStore) Ensure(_ context.Context, r fetch.Request) (string, error) {
	sum, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.NarHash, "sha256-"))
	if err != nil {
		return "", err
	}
	return drv.FixedOutputPath("sha256", hex.EncodeToString(sum), true, r.Name)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sri(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func h1(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

// gqlgenRequest is the codegen fixture's gqlgen part: //schema:graphql realised by
// //svc:module as //svc:gen-graph; the module's build list has gqlgen and a nested module
// (golang.org/x/tools and golang.org/x/tools/gopls) to show both vendor layouts.
func gqlgenRequest(t *testing.T) engine.PlanRequest {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, filepath.Base(schemaSrc), "schema", "schema.graphqls"), "type Query { a: Int }\n")
	mods := []lock.GoModule{
		{Path: "github.com/99designs/gqlgen", Version: "v0.17.90", Packages: true},
		{Path: "golang.org/x/tools", Version: "v0.42.0", Packages: true},
		{Path: "golang.org/x/tools/gopls", Version: "v0.21.0", Packages: true},
		{Path: "gopkg.in/yaml.v3", Version: "v3.0.1"}, // go.mod only: not vendored
	}
	var list []string
	for i, m := range mods {
		mods[i].ModH1 = h1(m.Path + "/go.mod")
		list = append(list, m.Path+"@"+m.Version)
		if !m.Packages {
			continue
		}
		mods[i].Tree, mods[i].H1 = sri(m.Path), h1(m.Path)
		sp, err := fakeStore{}.Ensure(context.Background(), fetch.Request{NarHash: mods[i].Tree, Name: drv.Name("gomod", m.Path, m.Version)})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, filepath.Base(sp))
		writeFile(t, filepath.Join(dir, "go.mod"), "module "+m.Path+"\n")
		writeFile(t, filepath.Join(dir, "a.go"), "package a\n")
		writeFile(t, filepath.Join(dir, "sub", "b.go"), "package b\n")
		writeFile(t, filepath.Join(dir, "testdata", "c.go"), "package c\n")
		writeFile(t, filepath.Join(dir, "_x", "d.go"), "package d\n")
	}
	sec := lock.GoSection{Inputs: strings.Repeat("ab", 32), Proxy: "https://proxy.golang.org", Modules: mods,
		Workspaces: map[string]lock.GoBuildList{"example.com/svc": {Go: "1.26.0", Modules: list}}}
	raw, err := lock.EncodeSection(lock.SectionGo, &sec)
	if err != nil {
		t.Fatal(err)
	}
	schema := &ir.Node{ID: "schema", Kind: gqlspec.KindSchema, Label: "//schema:graphql", Spec: json.RawMessage(`{}`)}
	gen := &ir.Node{ID: "gen", Kind: gqlspec.KindGqlgen, Label: "//svc:gen-graph", Deps: []string{"schema", "gqlgen", "go"},
		Spec: mustJSON(t, gqlspec.Gqlgen{Module: "example.com/svc", Dir: "gen/graph", Schema: 0, Tool: 1, Go: 2, Models: map[string]string{"UUID": "github.com/google/uuid.UUID"}, Autobind: []string{}})}
	mod := &ir.Node{ID: "mod", Kind: "go.module", Label: "//svc:module", Deps: []string{"tc", "gen"},
		Spec: json.RawMessage(`{"path":"example.com/svc","go":"1.26","generated":[{"dir":"gen/graph","dep":1}]}`)}
	return engine.PlanRequest{Graph: &ir.Graph{Nodes: []*ir.Node{schema, gen, mod}}, Nodes: []*ir.Node{schema, gen},
		Sources: map[string]string{"schema": schemaSrc},
		Inputs: map[string]engine.Resolved{"schema": {Outputs: map[string]string{"out": schemaSrc}},
			"gqlgen": {Outputs: map[string]string{"out": gqlgenPath}}, "go": {Outputs: map[string]string{"out": goPath}}},
		Locks: map[string]json.RawMessage{lock.SectionGo: raw}, Builder: builder, Store: fakeStore{root: root}}
}

// runSteps decodes the steps of a "run" job strictly into bob-builder's types.
func runSteps(t *testing.T, d *drv.Derivation) []bspec.Step {
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

// R-GEN-002a: graphql.Gqlgen is one gqlgen job run offline in a module root: vendor/ holds the
// module's build list (symlinks; a module containing another vendored module is copied),
// vendor/modules.txt lists the packages the go command sees, go.mod requires every vendored
// module, gqlgen.yml names the schema files and the owned directory, and gen/graph is copied
// to $out; svc/gen/graph is materialised.
func TestPlanGqlgen(t *testing.T) {
	req := gqlgenRequest(t)
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drvs) != 1 || len(res.Materialise) != 1 || res.Materialise[0].Dir != "svc/gen/graph" {
		t.Fatalf("plan: %d drvs, materialise %+v", len(res.Drvs), res.Materialise)
	}
	writes := map[string]string{}
	var log []string
	for _, s := range runSteps(t, res.Drvs[0]) {
		switch s.Op {
		case "write":
			var w bspec.WriteArgs
			_ = json.Unmarshal(s.Args, &w)
			writes[w.Path] = w.Content
		case "symlink":
			var l bspec.SymlinkArgs
			_ = json.Unmarshal(s.Args, &l)
			log = append(log, "symlink "+strings.TrimPrefix(l.Link, buildSrc+"/"))
		case "copy":
			var c bspec.CopyArgs
			_ = json.Unmarshal(s.Args, &c)
			log = append(log, "copy "+strings.TrimPrefix(c.To, buildSrc+"/"))
		case "exec":
			var e bspec.ExecArgs
			_ = json.Unmarshal(s.Args, &e)
			log = append(log, "exec "+strings.Join(e.Argv, " "))
			if e.Env["GOFLAGS"] != "-mod=vendor" || e.Env["GOPROXY"] != "off" || e.Cwd != buildSrc ||
				strings.Join(e.Path, ":") != gqlgenPath+"/bin:"+goPath+"/bin" {
				t.Errorf("exec %+v", e)
			}
		}
	}
	out := drv.Placeholder("out")
	want := []string{"copy " + buildSrc, "symlink vendor/github.com/99designs/gqlgen", "copy vendor/golang.org/x/tools",
		"symlink vendor/golang.org/x/tools/gopls", "exec gqlgen generate --config " + buildCfg + "/gqlgen.yml", "copy " + out}
	if strings.Join(log, "\n") != strings.Join(want, "\n") {
		t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(log, "\n"), strings.Join(want, "\n"))
	}
	if got := writes[buildSrc+"/go.mod"]; got != "module example.com/svc\n\ngo 1.26.0\n\nrequire (\n\tgithub.com/99designs/gqlgen v0.17.90\n\tgolang.org/x/tools v0.42.0\n\tgolang.org/x/tools/gopls v0.21.0\n)\n" {
		t.Errorf("go.mod:\n%s", got)
	}
	var mt strings.Builder
	for _, m := range []string{"github.com/99designs/gqlgen v0.17.90", "golang.org/x/tools v0.42.0", "golang.org/x/tools/gopls v0.21.0"} {
		p, _, _ := strings.Cut(m, " ")
		fmt.Fprintf(&mt, "# %s\n## explicit\n%s\n%s/sub\n", m, p, p)
	}
	if got := writes[buildSrc+"/vendor/modules.txt"]; got != mt.String() {
		t.Errorf("modules.txt:\n%s\nwant:\n%s", got, mt.String())
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(writes[buildCfg+"/gqlgen.yml"]), &cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	if got := string(b); got != `{"exec":{"filename":"gen/graph/generated.go","package":"graph"},"model":{"filename":"gen/graph/model/models_gen.go","package":"model"},"models":{"UUID":{"model":["github.com/google/uuid.UUID"]}},"schema":["schema/schema.graphqls"],"skip_mod_tidy":true,"skip_validation":true}` {
		t.Errorf("gqlgen.yml %s", got)
	}
}

// Without the consumer's build list in bob.lock the job cannot be planned: BOB-E-1012 (stale
// lock), so bob lock runs first.
func TestPlanGqlgenNotLocked(t *testing.T) {
	req := gqlgenRequest(t)
	req.Locks = nil
	_, err := Ecosystem{}.Plan(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "example.com/svc is not locked") {
		t.Errorf("err = %v", err)
	}
}

func TestPackageName(t *testing.T) {
	for dir, want := range map[string]string{"gen/graph": "graph", "gen/Graph-API": "graphapi", "gen/1x": "generated"} {
		if got := packageName(dir); got != want {
			t.Errorf("packageName(%q) = %q, want %q", dir, got, want)
		}
	}
}
