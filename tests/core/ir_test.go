package core

// SPEC §1.5–§1.6: R-FW-046 node creation contract, R-FW-047 IR format, R-FW-051
// engine.Ecosystem.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-FW-046 (a): a kind that declared only AxisPlatform and reads the profile panics with
// "ext: kind <k> reads the profile but did not declare AxisProfile".
func TestRFW046UndeclaredAxisPanics(t *testing.T) {
	parity.Covers(t, "R-FW-046")
	r := bobRun(t, repo(t, "toy"), "graph", "//toy:bad", "--json")
	if harness.Red(t, r) {
		return
	}
	if r.Exit == 0 {
		t.Fatalf("evaluating a kind that reads an undeclared axis succeeded")
	}
	want := "ext: kind toy.bad reads the profile but did not declare AxisProfile"
	if !strings.Contains(r.Combined(), want) {
		t.Errorf("output lacks %q:\n%s", want, r.Combined())
	}
}

// R-FW-046 (b): two P differing only in profile produce IR nodes with equal config projections
// (here one node, because the projection drops the profile).
func TestRFW046ProjectionIgnoresUndeclaredAxes(t *testing.T) {
	parity.Covers(t, "R-FW-046")
	g, ok := evalIR(t, repo(t, "toy"), "//toy:a@linux-arm64", "//toy:a@linux-arm64+debug", "//toy:b@linux-arm64", "//toy:b@linux-arm64+debug")
	if !ok {
		return
	}
	var a, b []*ir.Node
	for _, n := range g.Nodes {
		switch n.Label {
		case "//toy:a":
			a = append(a, n)
		case "//toy:b":
			b = append(b, n)
		}
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("release and debug must project to one node per package, got %d //toy:a and %d //toy:b", len(a), len(b))
	}
	if _, has := a[0].Proj["profile"]; has {
		t.Errorf("proj of a platform-only kind holds the profile: %v", a[0].Proj)
	}
	if _, has := a[0].Proj["platform"]; !has {
		t.Errorf("proj lacks the declared platform axis: %v", a[0].Proj)
	}
	if len(b[0].Deps) != 1 || b[0].Deps[0] != a[0].ID {
		t.Errorf("//toy:b deps %v, want [%s]", b[0].Deps, a[0].ID)
	}
	if len(g.Configs) < 2 {
		t.Errorf("the two requested configurations are not both in configs[]: %+v", g.Configs)
	}
	// Equal projections give equal derivation paths: both configurations are built in one
	// store and every label has the same output path (input-addressed: same derivation) in both.
	bin := buildBobtoy(t)
	dir := repo(t, "toy")
	env := []string{"BOB_HOME=" + t.TempDir()}
	pathsOf := func(cfg string) map[string]string {
		r := harness.RunPath(t, bin, harness.Spec{Dir: dir, Env: env}, "build", "//toy:b@linux-arm64"+cfg, "--print-paths", "--json")
		noLeak(t, r)
		if harness.Red(t, r) || !exitIs(t, r, 0) {
			return nil
		}
		out := map[string]string{}
		for _, e := range eventsNamed(events(t, r), "package.finish") {
			if ps, _ := e["paths"].([]any); len(ps) > 0 {
				out[e.str("label")], _ = ps[0].(string)
			}
		}
		return out
	}
	rel, dbg := pathsOf(""), pathsOf("+debug")
	if rel == nil || dbg == nil {
		return
	}
	for _, l := range []string{"//toy:a", "//toy:b"} {
		if rel[l] == "" || rel[l] != dbg[l] {
			t.Errorf("%s: output path %q for release, %q for debug; equal projections must give equal derivation paths", l, rel[l], dbg[l])
		}
	}
}

// buildBobtoy builds bob with the toy ecosystem linked in (tests/core/toyeco).
func buildBobtoy(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bobtoy")
	cmd := exec.Command("go", "build", "-o", bin, "./tests/core/toyeco/cmd/bobtoy")
	cmd.Dir = harness.Root(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: go build bobtoy: %v\n%s", err, out)
	}
	return bin
}

// R-FW-051: a test ecosystem registered from its own package (tests/core/toyeco) builds a
// two-node graph with no change to pkg/engine.
func TestRFW051ToyEcosystem(t *testing.T) {
	parity.Covers(t, "R-FW-051")
	root := harness.Root(t)
	// pkg/engine must not know the toy ecosystem.
	_ = filepath.WalkDir(filepath.Join(root, "pkg", "engine"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
			if b, _ := os.ReadFile(p); bytes.Contains(bytes.ToLower(b), []byte("toy")) {
				t.Errorf("%s mentions the toy ecosystem", p)
			}
		}
		return nil
	})
	bin := buildBobtoy(t)
	dir := repo(t, "toy")
	r := harness.RunPath(t, bin, harness.Spec{Dir: dir}, "build", "//toy:b", "--print-paths", "--json")
	noLeak(t, withoutPaths(r))
	if harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}
	evs := events(t, r)
	built := map[string]string{}
	for _, e := range eventsNamed(evs, "package.finish") {
		if e.str("status") != "built" {
			t.Errorf("%s: %s, want built", e.str("label"), e.str("status"))
		}
		if ps, _ := e["paths"].([]any); len(ps) > 0 {
			built[e.str("label")], _ = ps[0].(string)
		}
	}
	for _, l := range []string{"//toy:a", "//toy:b"} {
		p, ok := built[l]
		if !ok {
			t.Errorf("no built output for %s; got %v", l, built)
			continue
		}
		if got := readFile(t, filepath.Join(p, "result.txt")); got != l+" built\n" {
			t.Errorf("%s result.txt = %q", l, got)
		}
	}
	if c := counts(t, evs); c["built"] != 2 {
		t.Errorf("built %v derivations, want exactly the two nodes of the graph", c["built"])
	}
}

// withoutPaths drops package.finish `paths[]` from r's --json lines: they hold host store paths
// by SPEC §02.6 (--print-paths), the one field the leak check must not see (as tests/cli
// checkStream does). Lines that are not JSON are kept as they are.
func withoutPaths(r harness.Result) harness.Result {
	lines := strings.Split(r.Stdout, "\n")
	for i, line := range lines {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		delete(e, "paths")
		if b, err := json.Marshal(e); err == nil {
			lines[i] = string(b)
		}
	}
	r.Stdout = strings.Join(lines, "\n")
	return r
}

// ownVersion is the bobVersion of the IR documents below.
const ownVersion = "0.1.0"

type irNode struct {
	ID, Kind, Label string
	Deps            []string
}

// irDoc renders a canonical IR v1 document of nodes (sorted by id).
func irDoc(nodes ...irNode) []byte {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var ns []string
	for _, n := range nodes {
		deps, _ := json.Marshal(n.Deps)
		if n.Deps == nil {
			deps = []byte("[]")
		}
		ns = append(ns, fmt.Sprintf(`{"definedAt":{"file":"%s/bob.go","line":%d},"deps":%s,"id":%q,"kind":%q,"label":%q,"proj":{},"spec":{},"srcs":[]}`,
			strings.TrimPrefix(strings.Split(n.Label, ":")[0], "//"), 10, deps, n.ID, n.Kind, n.Label))
	}
	return []byte(`{"bobVersion":"` + ownVersion + `","configs":[],"irVersion":1,"module":"example.com/x","nodes":[` +
		strings.Join(ns, ",") + `],"pipelines":[],"repo":{"nixpkgs":"nixos-26.05","variants":[]},"roots":[]}`)
}

func parseIR(t *testing.T, data []byte) (*ir.Graph, error, bool) {
	t.Helper()
	g, err := ir.Parse(data, ownVersion)
	if isNotImplemented(err) {
		harness.RedFeature(t, "ir.Parse")
		return nil, err, false
	}
	return g, err, true
}

func wantDiag(t *testing.T, err error, class diag.Class, what string) *diag.Error {
	t.Helper()
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Errorf("%s: error %v is not a *diag.Error", what, err)
		return nil
	}
	if d.Code.Class() != class {
		t.Errorf("%s: %s has class %s, want %s", what, d.Code.ID(), d.Code.Class(), class)
	}
	return d
}

// R-FW-047 validation: ir.Parse rejects hostile IR before it is used.
func TestRFW047IRValidation(t *testing.T) {
	parity.Covers(t, "R-FW-047")
	n := func(id string, deps ...string) irNode {
		return irNode{ID: id, Kind: "bob.run", Label: "//" + strings.TrimPrefix(id, "//"), Deps: deps}
	}
	g, err, ok := parseIR(t, irDoc(n("a:one"), n("b:two", "a:one")))
	if !ok {
		return
	}
	if err != nil || g == nil || len(g.Nodes) != 2 {
		t.Fatalf("a valid two-node IR is rejected: %v", err)
	}
	t.Run("missing dependency", func(t *testing.T) {
		_, err := ir.Parse(irDoc(n("a:one", "nope:x")), ownVersion)
		if err == nil {
			t.Fatal("a dependency id that does not exist is accepted")
		}
	})
	t.Run("cycle", func(t *testing.T) {
		_, err := ir.Parse(irDoc(n("a:one", "b:two"), n("b:two", "a:one")), ownVersion)
		if err == nil {
			t.Fatal("a dependency cycle is accepted")
		}
		d := wantDiag(t, err, diag.ClassDefinition, "cycle")
		if d == nil {
			return
		}
		if d.Code.ID() != "BOB-E-1006" {
			t.Errorf("cycle gives %s, want BOB-E-1006", d.Code.ID())
		}
		msg := err.Error()
		for _, want := range []string{"//a:one", "//b:two", "a/bob.go:10", "b/bob.go:10"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the cycle error lacks %q (the chain and each definedAt): %s", want, msg)
			}
		}
	})
	t.Run("unknown kind", func(t *testing.T) {
		bad := irNode{ID: "a:one", Kind: "nosuch.kind", Label: "//a:one"}
		if _, err := ir.Parse(irDoc(bad), ownVersion); err == nil {
			t.Fatal("an unregistered kind is accepted")
		}
	})
	t.Run("string over 64 KiB", func(t *testing.T) {
		long := irNode{ID: "a:one", Kind: "bob.run", Label: "//a:" + strings.Repeat("x", 64<<10)}
		if _, err := ir.Parse(irDoc(long), ownVersion); err == nil {
			t.Fatal("a string longer than 64 KiB is accepted")
		}
	})
	t.Run("more than 1,000,000 nodes", func(t *testing.T) {
		// Short nodes keep the document under the 64 MiB cap, so only the node cap can reject it
		// (SPEC problem: with complete nodes the size cap is reached first).
		var b strings.Builder
		b.WriteString(`{"bobVersion":"` + ownVersion + `","configs":[],"irVersion":1,"module":"m","nodes":[`)
		for i := 0; i <= 1_000_000; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":"n%d","kind":"bob.run"}`, i)
		}
		b.WriteString(`],"pipelines":[],"repo":{"nixpkgs":"x","variants":[]},"roots":[]}`)
		if b.Len() >= ir.MaxBytes {
			t.Fatalf("harness: test document is %d bytes, over the size cap", b.Len())
		}
		if _, err := ir.Parse([]byte(b.String()), ownVersion); err == nil {
			t.Fatal("1,000,001 nodes are accepted")
		}
	})
	t.Run("larger than 64 MiB", func(t *testing.T) {
		doc := irDoc(n("a:one"))
		pad := bytes.Repeat([]byte(" "), 64<<20)
		big := append(append([]byte{}, doc...), pad...)
		if _, err := ir.Parse(big, ownVersion); err == nil {
			t.Fatal("a document larger than 64 MiB is accepted")
		}
	})
	t.Run("not JSON or wrong irVersion", func(t *testing.T) {
		for _, doc := range []string{``, `{`, `[]`, `null`, strings.Replace(string(irDoc(n("a:one"))), `"irVersion":1`, `"irVersion":2`, 1)} {
			if _, err := ir.Parse([]byte(doc), ownVersion); err == nil {
				t.Errorf("ir.Parse accepted %.60q", doc)
			}
		}
	})
}

// FuzzIRParse: the IR parser never panics and rejects or accepts within a bounded amount of
// memory (R-FW-047). Seeds run in every go test; the slow suite runs it with -fuzz.
func FuzzIRParse(f *testing.F) {
	parity.Covers(f, "R-FW-047")
	valid := irDoc(irNode{ID: "a:one", Kind: "bob.run", Label: "//a:one"}, irNode{ID: "b:two", Kind: "bob.run", Label: "//b:two", Deps: []string{"a:one"}})
	f.Add(valid)
	f.Add([]byte(`{"irVersion":1}`))
	f.Add(bytes.ReplaceAll(valid, []byte("//a:one"), []byte("//b:two")))
	f.Add(bytes.Repeat([]byte("["), 100000))
	f.Add([]byte(`{"nodes":[{"deps":["x"],"id":"x"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			// The empty document and the valid seed decide red-or-not once.
			_, err := ir.Parse(valid, ownVersion)
			if isNotImplemented(err) {
				harness.RedFeature(t, "ir.Parse")
				return
			}
			if err != nil {
				t.Fatalf("the valid seed is rejected: %v", err)
			}
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ir.Parse panicked on %.200q: %v", data, r)
				}
			}()
			_, err := ir.Parse(data, ownVersion)
			if isNotImplemented(err) {
				harness.RedFeature(t, "ir.Parse")
			}
		}()
		runtime.ReadMemStats(&after)
		if grown := int64(after.TotalAlloc - before.TotalAlloc); grown > 64*int64(len(data))+(64<<20) {
			t.Errorf("ir.Parse allocated %d bytes for a %d byte input (unbounded)", grown, len(data))
		}
	})
}

// R-FW-047 schema: pkg/engine/ir/ir.schema.json exists and describes exactly the top-level
// fields of the SPEC table, and the node fields.
func TestRFW047SchemaFile(t *testing.T) {
	parity.Covers(t, "R-FW-047")
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "pkg", "engine", "ir", "ir.schema.json"))
	if err != nil {
		t.Fatalf("pkg/engine/ir/ir.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("ir.schema.json is not JSON: %v", err)
	}
	want := []string{"irVersion", "bobVersion", "module", "repo", "configs", "nodes", "roots", "pipelines"}
	props, _ := schema["properties"].(map[string]any)
	for _, k := range want {
		if _, ok := props[k]; !ok {
			t.Errorf("ir.schema.json has no property %q", k)
		}
	}
	if len(props) != len(want) {
		t.Errorf("ir.schema.json has %d top-level properties, the SPEC table has %d", len(props), len(want))
	}
	nodes, _ := props["nodes"].(map[string]any)
	items, _ := nodes["items"].(map[string]any)
	nprops, _ := items["properties"].(map[string]any)
	for _, k := range []string{"id", "kind", "label", "definedAt", "proj", "deps", "srcs", "spec", "nix"} {
		if _, ok := nprops[k]; !ok {
			t.Errorf("ir.schema.json: nodes[] has no property %q", k)
		}
	}
}

// validate checks v against the subset of JSON Schema used by ir.schema.json: type, enum,
// required, properties, additionalProperties=false, items, maxItems, maxLength.
func validate(schema map[string]any, v any, path string) []string {
	return validateIn(schema, schema, v, path)
}

func validateIn(root, schema map[string]any, v any, path string) []string {
	var errs []string
	if ref, ok := schema["$ref"].(string); ok {
		if def, ok := root["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any); ok {
			return validateIn(root, def, v, path)
		}
		return []string{path + ": unresolved " + ref}
	}
	switch schema["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return []string{path + ": not an object"}
		}
		props, _ := schema["properties"].(map[string]any)
		if req, _ := schema["required"].([]any); req != nil {
			for _, r := range req {
				if _, ok := m[r.(string)]; !ok {
					errs = append(errs, path+": missing "+r.(string))
				}
			}
		}
		for k, sub := range m {
			ps, known := props[k].(map[string]any)
			if !known {
				if extra, ok := schema["additionalProperties"].(map[string]any); ok {
					errs = append(errs, validateIn(root, extra, sub, path+"."+k)...)
				} else if schema["additionalProperties"] == false {
					errs = append(errs, path+": unexpected "+k)
				}
				continue
			}
			errs = append(errs, validateIn(root, ps, sub, path+"."+k)...)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return []string{path + ": not an array"}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range a {
				errs = append(errs, validateIn(root, items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return []string{path + ": not a string"}
		}
		if enum, _ := schema["enum"].([]any); enum != nil && !contains(enum, s) {
			errs = append(errs, path+": not in enum")
		}
	case "integer":
		if n, ok := v.(json.Number); !ok || strings.ContainsAny(n.String(), ".eE") {
			errs = append(errs, path+": not an integer")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			errs = append(errs, path+": not a boolean")
		}
	}
	return errs
}

func contains(xs []any, s string) bool {
	for _, x := range xs {
		if xs, ok := x.(string); ok && xs == s {
			return true
		}
	}
	return false
}

// canonical re-encodes JSON with sorted keys and no insignificant whitespace.
func canonical(t *testing.T, data []byte) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("IR is not JSON: %v", err)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

// R-FW-047: the IR of every fixture is canonical (sorted keys, compact, nodes sorted by id),
// UTF-8, validates against ir.schema.json, and parses with ir.Parse.
func TestRFW047FixtureIRIsCanonicalAndValid(t *testing.T) {
	parity.Covers(t, "R-FW-047")
	sb, err := os.ReadFile(filepath.Join(harness.Root(t), "pkg", "engine", "ir", "ir.schema.json"))
	if err != nil {
		t.Fatalf("ir.schema.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(sb, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme", "polyglot", "go", "rust", "npm", "cc", "ocaml", "codegen", "cpp-lib"} {
		t.Run(name, func(t *testing.T) {
			res, ok := evaluate(t, harness.Fixture(t, name))
			if !ok {
				return
			}
			ir1 := bytes.TrimRight(res.IR, "\n")
			if !bytes.Equal(ir1, canonical(t, res.IR)) {
				t.Errorf("IR is not canonical JSON (sorted keys, no insignificant whitespace)")
			}
			if !utf8.Valid(res.IR) {
				t.Errorf("IR is not valid UTF-8")
			}
			dec := json.NewDecoder(bytes.NewReader(res.IR))
			dec.UseNumber()
			var v map[string]any
			if err := dec.Decode(&v); err != nil {
				t.Fatalf("IR is not JSON: %v", err)
			}
			for _, e := range validate(schema, v, "$") {
				t.Errorf("schema: %s", e)
			}
			var head struct {
				BobVersion string `json:"bobVersion"`
				Nodes      []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(res.IR, &head); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(head.Nodes))
			for i, n := range head.Nodes {
				ids[i] = n.ID
			}
			if !sort.StringsAreSorted(ids) {
				t.Errorf("nodes are not sorted by id")
			}
			g, err := ir.Parse(res.IR, head.BobVersion)
			if isNotImplemented(err) {
				harness.RedFeature(t, "ir.Parse")
				return
			}
			if err != nil || len(g.Nodes) == 0 {
				t.Errorf("ir.Parse of the evaluator's own IR: %v (%d nodes)", err, len(g.Nodes))
			}
		})
	}
}

// R-FW-047 / R-FW-043: golden IR for examples/polyglot (and the other fixtures).
func TestRFW047GoldenIR(t *testing.T) {
	parity.Covers(t, "R-FW-047", "R-FW-043")
	for _, name := range []string{"polyglot", "acme"} {
		t.Run(name, func(t *testing.T) {
			res, ok := evaluate(t, harness.Fixture(t, name))
			if !ok {
				return
			}
			golden.Assert(t, "tests/goldens/ir/"+name+".json", res.IR, golden.Reqs("R-FW-047"))
		})
	}
}
