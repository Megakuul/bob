package gen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// outputOf returns the first output path of label from a build of target.
func outputOf(t *testing.T, dir string, env []string, label string) (string, bool) {
	t.Helper()
	paths, ok := ecotest.OutputPaths(t, dir, env, label)
	if !ok {
		return "", false
	}
	if len(paths[label]) == 0 {
		t.Errorf("no output path for %s", label)
		return "", false
	}
	return paths[label][0], true
}

// R-GEN-001a (e2e): bob build //... generates and compiles the fixture; the Go server answers
// the Go connect client and the TS connect-es client; every codegen derivation rebuilds
// bit-identically on a second store.
func TestRGEN001aProtoConnectEndToEnd(t *testing.T) {
	parity.Covers(t, "R-GEN-001a")
	homeA, homeB := t.TempDir(), t.TempDir()
	dir, envA, ok := lockedFixture(t, homeA)
	if !ok {
		return
	}
	r := ecotest.BobEnv(t, dir, envA, "build", "//...", "--print-paths", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	paths := ecotest.FinishPaths(t, r)
	server, client := paths["//svc/cmd/server:server"], paths["//svc/cmd/client:client"]
	if len(server) == 0 || len(client) == 0 {
		t.Fatalf("no output paths for the server or the client: %v", paths)
	}
	url, _ := serve(t, filepath.Join(server[0], "bin", "server"))

	g := ecotest.RunEnvI(t, filepath.Join(client[0], "bin", "client"), "-url", url)
	if g.Exit != 0 || !strings.Contains(g.Stdout, "go client: hello go-client (request_id=r-1)") || !strings.Contains(g.Stdout, "empty name -> code=invalid_argument") {
		t.Errorf("Go client exited %d and printed %q\n%s", g.Exit, g.Stdout, g.Stderr)
	}
	ts := ecotest.BobEnv(t, dir, envA, "run", "//web:client", "--", url)
	if ts.Exit != 0 || !strings.Contains(ts.Stdout, "ts client: hello ts-client (request_id=r-1)") {
		t.Errorf("TS client exited %d and printed %q\n%s", ts.Exit, ts.Stdout, ts.Stderr)
	}

	// Each codegen derivation rebuilds bit-identically (a second store, same inputs).
	envB := ecotest.Registry(t, homeB)
	if lr := ecotest.BobEnv(t, dir, envB, "lock"); !ecotest.OK(t, lr) {
		return
	}
	other, ok := ecotest.OutputPaths(t, dir, envB, "//...")
	if !ok {
		return
	}
	gens := 0
	for label, ps := range paths {
		if !strings.Contains(label, ":gen-") {
			continue
		}
		gens++
		if len(other[label]) != len(ps) || len(ps) == 0 {
			t.Errorf("%s: %d outputs on the first store, %d on the second", label, len(ps), len(other[label]))
			continue
		}
		for i := range ps {
			a := filepath.Join(homeA, "nix", "store", filepath.Base(ps[i]))
			b := filepath.Join(homeB, "nix", "store", filepath.Base(other[label][i]))
			if filepath.Base(ps[i]) != filepath.Base(other[label][i]) || ecotest.TreeDigest(t, a) != ecotest.TreeDigest(t, b) {
				t.Errorf("%s is not bit-identical across builds", label)
			}
		}
	}
	if gens < 2 {
		t.Errorf("only %d codegen packages (labels containing :gen-) were built, want the connect Go and TS recipes at least", gens)
	}
}

// R-GEN-001a (syntax error): a proto syntax error prints the defined-at line of svc/bob.go and
// the generator's own message with the repo path; no raw Nix text.
func TestRGEN001aProtoSyntaxError(t *testing.T) {
	parity.Covers(t, "R-GEN-001a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	// S6 fault F1: the field on line 8 loses its ";", so buf stops at the "}" on line 9.
	ecotest.ReplaceIn(t, fixtureFile(dir, "proto/greet/v1/greet.proto"), "  string name = 1;\n}", "  string name = 1\n}")
	line := lineOf(t, fixtureFile(dir, "svc/bob.go"), "connect.Go(")
	r := ecotest.BobEnv(t, dir, env, "build", "//svc:gen-proto-greet-v1")
	if harness.Red(t, r) {
		return
	}
	if r.Exit == 0 {
		t.Fatalf("a proto syntax error did not fail the build")
	}
	want := fmt.Sprintf("✗ //svc:gen-proto-greet-v1 code generation failed (defined at svc/bob.go:%d)", line)
	if !strings.Contains(r.Stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, r.Stderr)
	}
	// buf 1.73 (the pin's) quotes with backticks; excerpt lines start with "|" (§02.5 rule 2;
	// "│" only on a terminal).
	if !strings.Contains(r.Stderr, "| proto/greet/v1/greet.proto:9:1:unexpected `}` after definition") {
		t.Errorf("stderr lacks the buf message:\n%s", r.Stderr)
	}
	golden.Assert(t, "tests/goldens/gen/proto-syntax-error.txt", []byte(r.Stderr), golden.Human(), golden.Reqs("R-GEN-001a"))
}

// R-GEN-002a (works): the gqlgen output compiles with the user's resolvers and answers a query;
// the graphql-codegen output type-checks the TS operations (tsc through the shim).
func TestRGEN002aGraphQLWorks(t *testing.T) {
	parity.Covers(t, "R-GEN-002a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	server, ok := outputOf(t, dir, env, "//svc/cmd/server:server")
	if !ok {
		return
	}
	url, _ := serve(t, filepath.Join(server, "bin", "server"))
	body, err := httpPost(url+"/graphql", `{"query":"{ hello(name: \"gql\") { greeting length } }"}`)
	if err != nil || !strings.Contains(body, `"greeting":"hello gql"`) || !strings.Contains(body, `"length":9`) {
		t.Errorf("POST /graphql = %q, %v", body, err)
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "env", "--format", "json"); !ecotest.OK(t, r) {
		return
	}
	web := filepath.Join(dir, "web")
	r := harness.RunPath(t, filepath.Join(dir, ".bob", "bin", "tsc"), harness.Spec{Dir: web, Env: []string{"BOB_HOME=" + home}}, "--noEmit", "-p", ".")
	if r.Exit != 0 {
		t.Errorf("tsc --noEmit over the generated GraphQL types exited %d:\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
}

// R-GEN-002a (error): an undefined type in the schema gives the generators' messages, without a
// Node stack trace into the store.
func TestRGEN002aUndefinedTypeInSchema(t *testing.T) {
	parity.Covers(t, "R-GEN-002a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	schema := fixtureFile(dir, "schema/schema.graphqls")
	ecotest.WriteFile(t, schema, strings.TrimRight(ecotest.ReadFile(t, schema), "\n")+"\ntype Other { ff: Nope }\n")
	for _, c := range []struct{ label, want string }{
		{"//svc:gen-graph", "| failed to load schema: schema/schema.graphqls:9:18: Undefined type Nope."},
		{"//web:gen-graphql", `| Unknown type: "Nope".`},
	} {
		r := ecotest.BobEnv(t, dir, env, "build", c.label)
		if harness.Red(t, r) {
			return
		}
		if r.Exit == 0 || !strings.Contains(r.Stderr, c.want) || !strings.Contains(r.Stderr, "✗ "+c.label+" code generation failed") {
			t.Errorf("bob build %s exited %d; stderr lacks %q:\n%s", c.label, r.Exit, c.want, r.Stderr)
		}
		noStack(t, r.Combined())
	}
}

const itemsClient = `import type { paths } from "../gen/openapi/index.js";

type Item = paths["/items/{id}"]["get"]["responses"]["200"]["content"]["application/json"];

export async function getItem(base: string, id: string): Promise<Item> {
  const res = await fetch(base + "/rest/items/" + id);
  return (await res.json()) as Item;
}

export const sample: Item = { id: "1", name: "one" };
`

// R-GEN-003a (works): the oapi-codegen StdHTTP server and models compile and serve items.yaml;
// openapi.TS types type-check a fetch client added to the web fixture (the output file is
// gen/openapi/index.d.ts: the SPEC leaves the name open).
func TestRGEN003aOpenAPIWorks(t *testing.T) {
	parity.Covers(t, "R-GEN-003a")
	home := t.TempDir()
	dir := harness.Fixture(t, "codegen")
	env := ecotest.Registry(t, home)
	webGo := fixtureFile(dir, "web/bob.go")
	ecotest.ReplaceIn(t, webGo, `"github.com/megakuul/bob/pkg/graphql"`, "\"github.com/megakuul/bob/pkg/graphql\"\n\t\"github.com/megakuul/bob/pkg/openapi\"")
	ecotest.ReplaceIn(t, webGo, "graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob(\"src/**/*.graphql\")}),", "graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob(\"src/**/*.graphql\")}),\n\t\t\topenapi.TS(schema.Items(p), openapi.TSOpts{}),")
	ecotest.WriteFile(t, fixtureFile(dir, "web/src/items.ts"), itemsClient)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return
	}
	server, ok := outputOf(t, dir, env, "//svc/cmd/server:server")
	if !ok {
		return
	}
	url, _ := serve(t, filepath.Join(server, "bin", "server"))
	body, err := httpGet(url + "/rest/items/42")
	if err != nil || !strings.Contains(body, `"id":"42"`) || !strings.Contains(body, `"name":"item 42"`) {
		t.Errorf("GET /rest/items/42 = %q, %v", body, err)
	}
	if r := ecotest.BobEnv(t, dir, env, "gen"); !ecotest.OK(t, r) {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "env", "--format", "json"); !ecotest.OK(t, r) {
		return
	}
	r := harness.RunPath(t, filepath.Join(dir, ".bob", "bin", "tsc"), harness.Spec{Dir: filepath.Join(dir, "web"), Env: []string{"BOB_HOME=" + home}}, "--noEmit", "-p", ".")
	if r.Exit != 0 {
		t.Errorf("tsc --noEmit over the fetch client and the openapi.TS types exited %d:\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
}

// R-GEN-003a (error): a bad $ref prints the generator's message with the spec path.
func TestRGEN003aBadRef(t *testing.T) {
	parity.Covers(t, "R-GEN-003a")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	ecotest.ReplaceIn(t, fixtureFile(dir, "schema/items.yaml"), "#/components/schemas/Item\"", "#/components/schemas/Itm\"")
	r := ecotest.BobEnv(t, dir, env, "build", "//svc:gen-rest")
	if harness.Red(t, r) {
		return
	}
	if r.Exit == 0 {
		t.Fatalf("a bad $ref did not fail the build")
	}
	for _, want := range []string{"schema/items.yaml", `map key "Itm" not found`, "code generation failed"} {
		if !strings.Contains(r.Stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.Stderr)
		}
	}
	noStack(t, r.Combined())
}

// R-GEN-005 (cutoff): a whitespace-only change to greet.proto followed by bob build //...
// rebuilds exactly the two codegen derivations (connect Go and connect TS) and no Go or TS
// consumer, because the regenerated output is byte-identical.
func TestRGEN005WhitespaceChangeRebuildsOnlyCodegen(t *testing.T) {
	parity.Covers(t, "R-GEN-005")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "build", "//..."); !ecotest.OK(t, r) {
		return
	}
	ecotest.ReplaceIn(t, fixtureFile(dir, "proto/greet/v1/greet.proto"), "package greet.v1;\n", "package greet.v1;\n\n\n")
	r := ecotest.BobEnv(t, dir, env, "build", "//...", "--json")
	if !ecotest.OK(t, r) {
		return
	}
	labels := ecotest.BuiltLabels(t, r)
	if n := ecotest.BuiltCount(t, r); n != 2 || len(labels) != 2 {
		t.Errorf("a whitespace change rebuilt %d derivations (%v), want exactly the 2 codegen derivations", n, labels)
	}
	for _, l := range labels {
		if !strings.Contains(l, ":gen-") {
			t.Errorf("%s was rebuilt: only codegen derivations (:gen-*) may rebuild", l)
		}
	}
}

// R-GEN-005 (cost): the two-phase planning adds at most 0.5 s to a no-change build [8c/15G].
// S6 measured the no-change build of this fixture at 1.43 s with cutoff, 0.93 s baseline plus
// the 0.5 s allowance, so the median of five no-change builds of //... must stay within
// 0.93 s + 0.5 s = 1.43 s (the baseline is S6's measurement; see docs/review/owner-items.md
// FIX-C for a baseline measured on the test host).
func TestRGEN005NoChangeBuildCost(t *testing.T) {
	parity.Covers(t, "R-GEN-005")
	home := t.TempDir()
	dir, env, ok := lockedFixture(t, home)
	if !ok {
		return
	}
	if r := ecotest.BobEnv(t, dir, env, "build", "//..."); !ecotest.OK(t, r) {
		return
	}
	const baseline = 930 * time.Millisecond // S6 quiet no-change build before the two-phase planning
	var times []time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		if r := ecotest.BobEnv(t, dir, env, "build", "//..."); !ecotest.OK(t, r) {
			return
		}
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if med := times[len(times)/2]; med > baseline+500*time.Millisecond {
		t.Errorf("median no-change build of //... took %s, want <= %s (baseline %s + 0.5 s) (%v)", med, baseline+500*time.Millisecond, baseline, times)
	}
}

// brokenRuntime pins make the generator and its runtime disagree (fixture faults F4 and F5).
var brokenRuntime = []struct {
	name, file, old, new, label, want string
}{
	{"F4 gqlgen runtime v0.17.89", "svc/bob.go", "Go:   \"1.26\",",
		"Go:   \"1.26\",\n\t\tRequire: []golang.Requirement{golang.Require(\"github.com/99designs/gqlgen\", \"v0.17.89\")},",
		"//svc:gen-graph", "but svc/bob.go requires v0.17.89"},
	{"F5 @bufbuild/protobuf 2.10.0", "web/bob.go", "npm.Dep(\"@connectrpc/connect\", \"^2\"),",
		"npm.Dep(\"@connectrpc/connect\", \"^2\"),\n\t\t\tnpm.Dep(\"@bufbuild/protobuf\", \"2.10.0\"),",
		"//web:gen-proto-greet-v1", "@bufbuild/protobuf"},
}

// R-GEN-006: a pinned runtime that does not match the generator fails with BOB-E-1021 (exit 3)
// before Nix runs (no package starts), with the golden text and the fix hint.
func TestRGEN006RuntimeMismatchIsAPreCheck(t *testing.T) {
	parity.Covers(t, "R-GEN-006")
	for _, c := range brokenRuntime {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			dir := harness.Fixture(t, "codegen")
			env := ecotest.Registry(t, home)
			ecotest.ReplaceIn(t, fixtureFile(dir, c.file), c.old, c.new)
			// The pre-check may fire at lock or at build; either way before Nix.
			r := ecotest.BobEnv(t, dir, env, "lock", "--json")
			if harness.Red(t, r) {
				return
			}
			if r.Exit == 0 {
				r = ecotest.BobEnv(t, dir, env, "build", "//...", "--json")
			}
			if !ecotest.ExitIs(t, r, 3) {
				return
			}
			found := false
			for _, code := range ecotest.ErrorCodes(t, r) {
				found = found || code == "BOB-E-1021"
			}
			if !found {
				t.Errorf("no BOB-E-1021 error event; codes %v\n%s", ecotest.ErrorCodes(t, r), r.Stdout)
			}
			for _, e := range ecotest.Named(ecotest.Events(t, r), "package.start") {
				t.Errorf("a package started (%v): the check must run before Nix", e)
			}
			human := ecotest.BobEnv(t, dir, env, "build", "//...")
			if !strings.Contains(human.Stderr, "does not match its runtime") || !strings.Contains(human.Stderr, c.want) || !strings.Contains(human.Stderr, c.label) {
				t.Errorf("stderr lacks the mismatch text (%q, %s):\n%s", c.want, c.label, human.Stderr)
			}
			name := strings.ToLower(strings.Fields(c.name)[0])
			golden.Assert(t, "tests/goldens/gen/runtime-mismatch-"+name+".txt", []byte(human.Stderr), golden.Human(), golden.Reqs("R-GEN-006"))
		})
	}
}
