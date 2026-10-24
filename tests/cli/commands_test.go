package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

func appendFile(t testing.TB, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func read(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRCLI009Check: `bob check` reports drift without changing anything (tree hash before =
// after); a clean tree gives exit 0 and `bob: no problems`; a hand edit of the generated go.mod
// gives exit 3 with BOB-E-1011 naming go.mod; `--fix` restores it; removing bob's block from
// .git/info/exclude gives BOB-E-1013.
func TestRCLI009Check(t *testing.T) {
	parity.Covers(t, "R-CLI-009")
	dir := harness.Fixture(t, "acme")
	env := home(t)

	// Baseline: generated files written and ignore entries in place.
	for _, args := range [][]string{{"gen"}, {"check", "--fix"}} {
		r := bob(t, dir, env, args...)
		if harness.Red(t, r) {
			return
		}
		if r.Exit != 0 {
			t.Fatalf("bob %s: exit %d\n%s", strings.Join(args, " "), r.Exit, r.Combined())
		}
	}
	before := tree(t, dir)
	r := bob(t, dir, env, "check")
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "bob: no problems" {
		t.Errorf("clean tree: bob check exit %d, output %q; want exit 0 and `bob: no problems`", r.Exit, r.Combined())
	}
	if tree(t, dir) != before {
		t.Errorf("bob check changed a file on a clean tree")
	}

	// A hand edit of the generated go.mod (what `go get` of a new module does).
	gomod := filepath.Join(dir, "go.mod")
	orig := read(t, gomod)
	appendFile(t, gomod, "\nrequire example.com/handedit v1.0.0\n")
	before = tree(t, dir)
	r = bob(t, dir, env, "check")
	if r.Exit != 3 {
		t.Errorf("hand-edited go.mod: bob check exit %d, want 3\n%s", r.Exit, r.Combined())
	}
	if !regexp.MustCompile(`error\[BOB-E-1011\]: generated file .*go\.mod was edited by hand`).MatchString(r.Combined()) {
		t.Errorf("want BOB-E-1011 naming go.mod:\n%s", r.Combined())
	}
	if !strings.Contains(r.Combined(), "hint: move the change into bob.go; bob check --fix regenerates the file") {
		t.Errorf("1011 lacks its hint:\n%s", r.Combined())
	}
	if tree(t, dir) != before {
		t.Errorf("bob check changed a file while reporting drift (tree hash differs)")
	}
	noLeak(t, r)
	j := bob(t, dir, env, "check", "--json")
	evs := checkStream(t, j, false)
	found := false
	for _, e := range evs {
		if e["event"] == "result" && e["command"] == "check" {
			data, _ := e["data"].(map[string]any)
			fs, _ := data["findings"].([]any)
			found = len(fs) > 0
		}
	}
	if !found {
		t.Errorf("check --json: result.data.findings is empty or the result event is missing")
	}

	r = bob(t, dir, env, "check", "--fix")
	if r.Exit != 0 {
		t.Errorf("bob check --fix: exit %d\n%s", r.Exit, r.Combined())
	}
	r = bob(t, dir, env, "check")
	if r.Exit != 0 {
		t.Errorf("after --fix, bob check: exit %d, want 0\n%s", r.Exit, r.Combined())
	}
	if now := read(t, gomod); strings.Contains(now, "handedit") || now != orig {
		t.Errorf("after --fix the hand edit of go.mod is still there:\n%s", now)
	}

	// 1013: bob's block removed from .git/info/exclude.
	excl := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.WriteFile(excl, []byte("# git ls-files --others --exclude-from=.git/info/exclude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = bob(t, dir, env, "check")
	if r.Exit != 3 || !strings.Contains(r.Combined(), "BOB-E-1013") {
		t.Errorf("bob's block removed from .git/info/exclude: exit %d, want 3 with BOB-E-1013\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(r.Combined(), "is not ignored by git") {
		t.Errorf("1013 message `{path} is not ignored by git` missing:\n%s", r.Combined())
	}
	r = bob(t, dir, env, "check", "--fix")
	if r.Exit != 0 {
		t.Errorf("bob check --fix after 1013: exit %d\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(read(t, excl), ".bob") {
		t.Errorf("--fix did not add .bob to .git/info/exclude")
	}
}

// TestRCLI009CheckLock: a stale lock gives BOB-E-1012 from check, `--fix` runs bob lock; `build
// --lock` re-resolves a stale section instead of failing with 1012.
func TestRCLI009CheckLock(t *testing.T) {
	parity.Covers(t, "R-CLI-009")
	dir := harness.Fixture(t, "acme")
	env := home(t)
	if r := bob(t, dir, env, "lock"); harness.Red(t, r) {
		return
	}
	web := filepath.Join(dir, "web", "bob.go")
	src := read(t, web)
	src = strings.Replace(src, `npm.Dep("svelte", "^5"),`, `npm.Dep("svelte", "^5"),`+"\n\t\tnpm.Dep(\"zod\", \"^3\"),", 1)
	if err := os.WriteFile(web, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	r := bob(t, dir, env, "check")
	if r.Exit != 3 || !strings.Contains(r.Combined(), "BOB-E-1012") || !strings.Contains(r.Combined(), "hint: run bob lock") {
		t.Errorf("stale lock: bob check exit %d, want 3 with BOB-E-1012 and `hint: run bob lock`\n%s", r.Exit, r.Combined())
	}
	r = bob(t, dir, env, "build", "//web:web")
	if r.Exit != 3 || !strings.Contains(r.Combined(), "BOB-E-1012") {
		t.Errorf("stale lock: bob build exit %d, want 3 with BOB-E-1012\n%s", r.Exit, r.Combined())
	}
	r = bob(t, dir, env, "build", "--lock", "//web:web")
	if r.Exit != 0 {
		t.Errorf("bob build --lock re-resolves a stale lock: exit %d\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(read(t, filepath.Join(dir, "bob.lock")), "zod") {
		t.Errorf("bob build --lock did not pin zod in bob.lock")
	}
}

// TestRCLI010Lock: `bob lock` is a no-op on a locked repository (byte-identical file, output
// `bob.lock: no changes`); adding one npm dependency gives exactly one `+` line and one new
// entry; `--update <name>` touches only that name's ecosystem entries; `--offline` with an
// uncached need exits 4 with BOB-E-3002.
func TestRCLI010Lock(t *testing.T) {
	parity.Covers(t, "R-CLI-010")
	dir := harness.Fixture(t, "acme")
	env := home(t)
	r := bob(t, dir, env, "lock")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("initial bob lock: exit %d\n%s", r.Exit, r.Combined())
	}
	lock := filepath.Join(dir, "bob.lock")
	locked := read(t, lock)
	r = bob(t, dir, env, "lock")
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "bob.lock: no changes" {
		t.Errorf("bob lock on a locked fixture: exit %d, output %q; want `bob.lock: no changes`", r.Exit, r.Combined())
	}
	if read(t, lock) != locked {
		t.Errorf("bob lock changed bob.lock although there was nothing to do")
	}

	web := filepath.Join(dir, "web", "bob.go")
	src := read(t, web)
	if err := os.WriteFile(web, []byte(strings.Replace(src, `npm.Dep("svelte", "^5"),`, `npm.Dep("svelte", "^5"),`+"\n\t\tnpm.Dep(\"zod\", \"^3\"),", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	r = bob(t, dir, env, "lock")
	if r.Exit != 0 {
		t.Fatalf("bob lock after adding zod: exit %d\n%s", r.Exit, r.Combined())
	}
	plus := regexp.MustCompile(`(?m)^  \+ npm +zod +\S+$`).FindAllString(r.Stdout, -1)
	if len(plus) != 1 {
		t.Errorf("want exactly one `+ npm zod …` line, got %d:\n%s", len(plus), r.Stdout)
	}
	nplus := 0
	for _, l := range lines(r.Stdout) {
		if strings.HasPrefix(l, "  +") {
			nplus++
		}
	}
	if nplus != 1 {
		t.Errorf("want exactly one `+` line in total, got %d:\n%s", nplus, r.Stdout)
	}
	for _, l := range lines(r.Stdout) {
		if strings.HasPrefix(l, "  ~") || strings.HasPrefix(l, "  -") {
			t.Errorf("unexpected change line after adding one dependency: %q", l)
		}
	}
	if !strings.HasPrefix(r.Stdout, "bob.lock: 1 change") {
		t.Errorf("header is not `bob.lock: 1 change…`:\n%s", r.Stdout)
	}
	after := read(t, lock)
	if after == locked || strings.Count(after, "zod") == 0 {
		t.Errorf("bob.lock has no new zod entry")
	}
	// existing pins are not updated by a plain `bob lock`: every line of the old lock survives.
	for _, l := range strings.Split(locked, "\n") {
		if strings.TrimSpace(l) != "" && !strings.Contains(after, l) && !strings.Contains(l, "lockVersion") && strings.TrimSpace(l) != "}" && strings.TrimSpace(l) != "{" {
			t.Errorf("an existing lock line disappeared: %q", l)
		}
	}

	// --update <name>: only that module's entries (plus the transitive ones it requires) change.
	// The scenario is built here: a file:// GOPROXY first offers golang.org/x/net v0.1.0 (needs
	// x/text v0.1.0) and golang.org/x/crypto v0.5.0; `bob lock` pins that state; then the proxy
	// publishes x/net v0.2.0 (needs x/text v0.2.0). A plain `bob lock` must keep the old pins,
	// `--update golang.org/x/net` must move exactly x/net and x/text and leave x/crypto alone.
	proxy := t.TempDir()
	goProxyAdd(t, proxy, "golang.org/x/text", "v0.1.0", "module golang.org/x/text\n\ngo 1.20\n")
	goProxyAdd(t, proxy, "golang.org/x/net", "v0.1.0", "module golang.org/x/net\n\ngo 1.20\n\nrequire golang.org/x/text v0.1.0\n")
	goProxyAdd(t, proxy, "golang.org/x/crypto", "v0.5.0", "module golang.org/x/crypto\n\ngo 1.20\n")
	gofix := harness.Fixture(t, "go")
	for _, d := range []string{"api", "lib", "native", "proto"} {
		if err := os.RemoveAll(filepath.Join(gofix, d)); err != nil {
			t.Fatal(err)
		}
	}
	toolsGo := filepath.Join(gofix, "tools", "bob.go")
	src = read(t, toolsGo)
	oldReq := `golang.Require("golang.org/x/sync", "v0.20.0")`
	if !strings.Contains(src, oldReq) {
		t.Fatalf("fixture go/tools/bob.go changed: no %s", oldReq)
	}
	if err := os.WriteFile(toolsGo, []byte(strings.Replace(src, oldReq, `golang.Require("golang.org/x/net", "v0.1.0"), golang.Require("golang.org/x/crypto", "v0.5.0")`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gofix, "tools", "cmd", "stamp", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	penv := append(home(t), "BOB_GOPROXY=file://"+proxy)
	if r := bob(t, gofix, penv, "lock"); r.Exit != 0 {
		t.Fatalf("lock on the go scenario: exit %d\n%s", r.Exit, r.Combined())
	}
	pinned := goLockModules(t, filepath.Join(gofix, "bob.lock"))
	for p, v := range map[string]string{"golang.org/x/net": "v0.1.0", "golang.org/x/text": "v0.1.0", "golang.org/x/crypto": "v0.5.0"} {
		if got, _ := pinned[p]["version"].(string); got != v {
			t.Fatalf("scenario setup: %s pinned at %q, want %s (lock: %v)", p, got, v, pinned)
		}
	}
	goProxyAdd(t, proxy, "golang.org/x/text", "v0.2.0", "module golang.org/x/text\n\ngo 1.20\n")
	goProxyAdd(t, proxy, "golang.org/x/net", "v0.2.0", "module golang.org/x/net\n\ngo 1.20\n\nrequire golang.org/x/text v0.2.0\n")
	lockPath := filepath.Join(gofix, "bob.lock")
	before := read(t, lockPath)
	if r := bob(t, gofix, penv, "lock"); r.Exit != 0 || strings.TrimSpace(r.Stdout) != "bob.lock: no changes" || read(t, lockPath) != before {
		t.Errorf("plain bob lock must not update existing pins: exit %d, output %q", r.Exit, r.Combined())
	}
	r = bob(t, gofix, penv, "lock", "--update", "golang.org/x/net")
	if r.Exit != 0 {
		t.Fatalf("bob lock --update golang.org/x/net: exit %d\n%s", r.Exit, r.Combined())
	}
	changed := map[string]string{}
	var nchange int
	for _, l := range lines(r.Stdout)[1:] {
		m := regexp.MustCompile(`^  ([~+-]) go +(\S+) +(.*)$`).FindStringSubmatch(l)
		if m == nil {
			t.Errorf("--update golang.org/x/net: change line %q is not `  <~|+|-> go <module> …`", l)
			continue
		}
		nchange++
		changed[m[2]] = m[1] + " " + m[3]
	}
	if len(changed) != 2 || !strings.HasPrefix(changed["golang.org/x/net"], "~ v0.1.0 \u2192 v0.2.0") || !strings.HasPrefix(changed["golang.org/x/text"], "~ v0.1.0 \u2192 v0.2.0") {
		t.Errorf("--update golang.org/x/net must list exactly x/net and its requirement x/text as v0.1.0 → v0.2.0, got %v\n%s", changed, r.Stdout)
	}
	if want := fmt.Sprintf("bob.lock: %d change", nchange); !strings.HasPrefix(r.Stdout, want) {
		t.Errorf("header does not match the %d listed changes:\n%s", nchange, r.Stdout)
	}
	updated := goLockModules(t, lockPath)
	for p, v := range map[string]string{"golang.org/x/net": "v0.2.0", "golang.org/x/text": "v0.2.0"} {
		if got, _ := updated[p]["version"].(string); got != v {
			t.Errorf("after --update: %s at %q, want %s", p, got, v)
		}
	}
	if !reflect.DeepEqual(updated["golang.org/x/crypto"], pinned["golang.org/x/crypto"]) {
		t.Errorf("--update golang.org/x/net changed the unrelated x/crypto entry: %v → %v", pinned["golang.org/x/crypto"], updated["golang.org/x/crypto"])
	}
	if len(updated) != len(pinned) {
		t.Errorf("--update golang.org/x/net added or removed entries: %d → %d", len(pinned), len(updated))
	}

	// --offline with an uncached need: exit 4, BOB-E-3002 (fresh HOME: nothing cached).
	fresh := harness.Fixture(t, "acme")
	r = bob(t, fresh, home(t), "lock", "--offline")
	if r.Exit != 4 || !strings.Contains(r.Combined(), "BOB-E-3002") {
		t.Errorf("bob lock --offline with nothing cached: exit %d, want 4 with BOB-E-3002\n%s", r.Exit, r.Combined())
	}
	noLeak(t, r)
}

// TestRCLI011Graph: `bob graph --why A B` prints the shortest dependency path as the §02.5 tree;
// without a path it prints `bob: A does not depend on B` and exits 0; `--format json` has the
// documented shape and the same edge set as the `--json` result; `--rdeps` reverses the edges.
func TestRCLI011Graph(t *testing.T) {
	parity.Covers(t, "R-CLI-011")
	dir := harness.Fixture(t, "polyglot")
	env := home(t)
	r := bob(t, dir, env, "graph", "--why", "//cmd/api:api", "nixpkgs:openssl")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("graph --why: exit %d\n%s", r.Exit, r.Combined())
	}
	ls := lines(r.Stdout)
	pos := `\S+/bob\.go:\d+:\d+`
	re := []*regexp.Regexp{
		regexp.MustCompile(`^//cmd/api:api {2,}` + pos + `$`),
		regexp.MustCompile(`^└─ //libs/foo:foo {2,}` + pos + `$`),
		regexp.MustCompile(`^   └─ nixpkgs:openssl {2,}` + pos + `$`),
	}
	if len(ls) != len(re) {
		t.Fatalf("want a 3-line path //cmd/api:api → //libs/foo:foo → nixpkgs:openssl, got:\n%s", r.Stdout)
	}
	for i := range re {
		if !re[i].MatchString(ls[i]) {
			t.Errorf("line %d %q does not match %s", i+1, ls[i], re[i])
		}
	}
	if !strings.Contains(ls[1], "libs/foo/bob.go:13:9") {
		t.Errorf("//libs/foo:foo is defined at libs/foo/bob.go:13:9 (the constructor call), got %q", ls[1])
	}
	golden.Assert(t, "tests/goldens/cli/graph/why-api-openssl.txt", []byte(r.Stdout), golden.Reqs("R-CLI-011"))

	no := bob(t, dir, env, "graph", "--why", "//cmd/api:api", "nixpkgs:zlib")
	if no.Exit != 0 || strings.TrimSpace(no.Stdout) != "bob: //cmd/api:api does not depend on nixpkgs:zlib" {
		t.Errorf("no path: exit %d, output %q; want exit 0 and `bob: //cmd/api:api does not depend on nixpkgs:zlib`", no.Exit, no.Combined())
	}

	type graph struct {
		Nodes []struct {
			Label, Kind, DefinedAt string
		} `json:"nodes"`
		Edges []struct{ From, To string } `json:"edges"`
		Why   [][]string                  `json:"why"`
	}
	edgeSet := func(g graph) map[string]bool {
		m := map[string]bool{}
		for _, e := range g.Edges {
			m[e.From+" -> "+e.To] = true
		}
		return m
	}
	jf := bob(t, dir, env, "graph", "--format", "json")
	if jf.Exit != 0 {
		t.Fatalf("graph --format json: exit %d\n%s", jf.Exit, jf.Combined())
	}
	var g graph
	if err := json.Unmarshal([]byte(jf.Stdout), &g); err != nil {
		t.Fatalf("graph --format json is not JSON: %v\n%s", err, jf.Stdout)
	}
	nodes := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Label == "" || n.Kind == "" {
			t.Errorf("node without label or kind: %+v", n)
		}
		nodes[n.Label] = true
	}
	for _, e := range g.Edges {
		if !nodes[e.From] || !nodes[e.To] {
			t.Errorf("edge %s -> %s names a node that is not in nodes[]", e.From, e.To)
		}
	}
	if !edgeSet(g)["//libs/foo:foo -> nixpkgs:openssl"] {
		t.Errorf("edge //libs/foo:foo -> nixpkgs:openssl missing")
	}
	js := bob(t, dir, env, "graph", "--json")
	var g2 graph
	for _, e := range checkStream(t, js, false) {
		if e["event"] == "result" {
			b, _ := json.Marshal(e["data"])
			if err := json.Unmarshal(b, &g2); err != nil {
				t.Errorf("result.data: %v", err)
			}
		}
	}
	a, b := edgeSet(g), edgeSet(g2)
	if len(a) != len(b) {
		t.Errorf("--format json has %d edges, --json result has %d", len(a), len(b))
	}
	for e := range a {
		if !b[e] {
			t.Errorf("edge %q only in --format json", e)
		}
	}
	rd := bob(t, dir, env, "graph", "--rdeps", "--format", "json", "nixpkgs:openssl")
	var g3 graph
	if err := json.Unmarshal([]byte(rd.Stdout), &g3); err != nil {
		t.Fatalf("graph --rdeps --format json: %v\n%s", err, rd.Stdout)
	}
	if !edgeSet(g3)["nixpkgs:openssl -> //libs/foo:foo"] {
		t.Errorf("--rdeps: want the reversed edge nixpkgs:openssl -> //libs/foo:foo, got %v", edgeSet(g3))
	}
	dot := bob(t, dir, env, "graph", "--format", "dot")
	if !strings.HasPrefix(dot.Stdout, "digraph") || !strings.Contains(dot.Stdout, `"//libs/foo:foo" -> "nixpkgs:openssl"`) {
		t.Errorf("--format dot is not a digraph with labels as node ids:\n%s", dot.Stdout)
	}
}

// TestRCLI012PipelineRun: `bob pipeline run ci` prints one `== step <name>` header per step, in
// order, each followed by that step's package lines; the exit code is that of the first failing
// step; an unknown pipeline is exit 2 with BOB-E-0001.
func TestRCLI012PipelineRun(t *testing.T) {
	parity.Covers(t, "R-CLI-012")
	dir := repo(t, "pipe")
	env := home(t)
	r := bob(t, dir, env, "pipeline", "run", "ci")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("pipeline run ci: exit %d\n%s", r.Exit, r.Combined())
	}
	ls := lines(r.Stdout)
	bi, ti := -1, -1
	for i, l := range ls {
		switch l {
		case "== step build":
			bi = i
		case "== step test":
			ti = i
		}
	}
	if bi < 0 || ti < 0 || bi > ti {
		t.Fatalf("want `== step build` then `== step test`:\n%s", r.Stdout)
	}
	if strings.Count(r.Stdout, "== step ") != 2 {
		t.Errorf("want exactly 2 step headers:\n%s", r.Stdout)
	}
	if !regexp.MustCompile(`(?m)^(built|cached) +//`).MatchString(strings.Join(ls[bi:ti], "\n")) {
		t.Errorf("no package line under `== step build`:\n%s", r.Stdout)
	}
	if !regexp.MustCompile(`(?m)^(PASS|cached) +//`).MatchString(strings.Join(ls[ti:], "\n")) {
		t.Errorf("no test line under `== step test`:\n%s", r.Stdout)
	}
	noLeak(t, r)

	// first failing step decides the exit status: a failing test fails step `test` (exit 1).
	bad := repo(t, "pipe")
	if err := os.WriteFile(filepath.Join(bad, "app", "bad_test.go"), []byte("package main\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) { t.Fatal(\"bad\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = bob(t, bad, env, "pipeline", "run", "ci")
	if r.Exit != 1 || !strings.Contains(r.Stdout, "== step test") || !strings.Contains(r.Combined(), "BOB-E-2005") {
		t.Errorf("failing test step: exit %d, want 1 with `== step test` and BOB-E-2005\n%s", r.Exit, r.Combined())
	}
	// a failing build step: exit 1, and the test step does not run.
	bad = repo(t, "pipe")
	if err := os.WriteFile(filepath.Join(bad, "app", "main.go"), []byte("package main\n\nfunc main() { var x int = \"s\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = bob(t, bad, env, "pipeline", "run", "ci")
	if r.Exit != 1 || !strings.Contains(r.Stdout, "== step build") || strings.Contains(r.Stdout, "== step test") {
		t.Errorf("failing build step: exit %d, want 1, `== step build` and no `== step test`\n%s", r.Exit, r.Combined())
	}
	r = bob(t, dir, env, "pipeline", "run", "nosuch")
	if r.Exit != 2 || !strings.Contains(r.Combined(), "BOB-E-0001") {
		t.Errorf("unknown pipeline: exit %d, want 2 with BOB-E-0001\n%s", r.Exit, r.Combined())
	}
}

// goProxyAdd adds one module version to a file:// GOPROXY directory (layout of §03.2.5:
// <path>/@v/{list,<v>.info,<v>.mod,<v>.zip}); `list` accumulates the versions.
func goProxyAdd(t testing.TB, root, path, version, goMod string) {
	t.Helper()
	v := filepath.Join(root, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(v, 0o755); err != nil {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, body := range map[string]string{"go.mod": goMod, "pkg.go": "package pkg\n"} {
		w, err := zw.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.ReadFile(filepath.Join(v, "list"))
	for name, body := range map[string][]byte{
		"list":            append(prev, []byte(version+"\n")...),
		version + ".info": []byte(`{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`),
		version + ".mod":  []byte(goMod),
		version + ".zip":  zb.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(v, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// goLockModules returns the go.modules entries of a bob.lock by module path.
func goLockModules(t testing.TB, lock string) map[string]map[string]any {
	t.Helper()
	var f struct {
		Go struct {
			Modules []map[string]any `json:"modules"`
		} `json:"go"`
	}
	if err := json.Unmarshal([]byte(read(t, lock)), &f); err != nil {
		t.Fatalf("%s is not valid JSON: %v", lock, err)
	}
	out := map[string]map[string]any{}
	for _, m := range f.Go.Modules {
		p, _ := m["path"].(string)
		out[p] = m
	}
	return out
}
