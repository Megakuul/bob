package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/ir"
)

func TestParse(t *testing.T) {
	cmd, _ := lookup("graph")
	a, err := parse([]string{"//a:a", "--why", "//a:a", "nixpkgs:x", "-j4", "--json", "--format=dot", "-vv", "--", "x", "--y"}, cmd.flags)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.vals["why"]; len(got) != 2 || got[1] != "nixpkgs:x" {
		t.Errorf("why = %v", got)
	}
	if a.str("j") != "4" || !a.bool("json") || a.str("format") != "dot" || !a.bool("vv") {
		t.Errorf("flags = %v", a.vals)
	}
	if len(a.pos) != 1 || a.pos[0] != "//a:a" || len(a.pass) != 2 || !a.dash {
		t.Errorf("pos %v pass %v", a.pos, a.pass)
	}
	b, _ := lookup("build")
	for _, bad := range [][]string{{"--nope"}, {"--json=maybe"}, {"-j"}, {"-j", "x"}, {"--platform"}} {
		_, err := parse(bad, b.flags)
		var d *diag.Error
		if !asDiag(err, &d) || d.Code.Class() != diag.ClassUsage {
			t.Errorf("parse %v: %v, want a usage error", bad, err)
		}
	}
	_, err = parse([]string{"--prnt-paths"}, b.flags)
	var d *diag.Error
	if !asDiag(err, &d) || !strings.Contains(d.Hint(), "--print-paths") {
		t.Errorf("unknown flag suggestion: %v", err)
	}
}

func asDiag(err error, d **diag.Error) bool {
	e, ok := err.(*diag.Error)
	*d = e
	return ok
}

func TestMainUsage(t *testing.T) {
	var so, se bytes.Buffer
	if got := Main([]string{"buil"}, &so, &se); got != 2 || !strings.Contains(se.String(), "error[BOB-E-0001]") || !strings.Contains(se.String(), "did you mean build?") {
		t.Errorf("exit %d, stderr %q", got, se.String())
	}
	so.Reset()
	if got := Main([]string{"frobnicate", "--json"}, &so, &se); got != 2 {
		t.Errorf("exit %d", got)
	}
	lines := strings.Split(strings.TrimSpace(so.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"run.start"`) || !strings.Contains(lines[1], `"BOB-E-0001"`) || !strings.Contains(lines[2], `"exitCode":2`) {
		t.Errorf("--json usage error:\n%s", so.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Errorf("not JSON: %s", l)
		}
	}
	so.Reset()
	if got := Main([]string{"version", "--json"}, &so, &se); got != 0 || !strings.Contains(so.String(), `"event":"result","time"`) || !strings.Contains(so.String(), `"nixpkgs":"nixos-`) {
		t.Errorf("version --json: exit %d\n%s", got, so.String())
	}
}

func TestOutsideRepository(t *testing.T) {
	dir := t.TempDir()
	var so, se bytes.Buffer
	if got := Main([]string{"-C", dir, "build"}, &so, &se); got != 2 || !strings.Contains(se.String(), "BOB-E-0002") {
		t.Errorf("exit %d: %s", got, se.String())
	}
}

func TestPrintChanges(t *testing.T) {
	var b bytes.Buffer
	o := &out{w: &b}
	printChanges(o, []build.LockChange{
		{Ecosystem: "go", Name: "golang.org/x/net", From: "v0.30.0", To: "v0.31.0", Op: "update"},
		{Ecosystem: "npm", Name: "react-dom", To: "19.3.0", Op: "add"},
		{Ecosystem: "crate", Name: "serde_derive", From: "1.0.210", Op: "remove"},
	})
	want := "bob.lock: 3 changes\n" +
		"  ~ go     golang.org/x/net   v0.30.0 → v0.31.0\n" +
		"  + npm    react-dom          19.3.0\n" +
		"  - crate  serde_derive       1.0.210\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
	// Q4: a fresh lock printed "~ go go → " and "+ cc cc": a section without names or versions
	// prints only its ecosystem, a workspace its go version.
	b.Reset()
	printChanges(o, []build.LockChange{
		{Ecosystem: "go", Name: "example.com/hello", To: "go 1.26.0", Op: "add"},
		{Ecosystem: "cc", Name: "cc", Op: "add"},
		{Ecosystem: "go", Name: "go", Op: "update"},
	})
	want = "bob.lock: 3 changes\n" +
		"  + go     example.com/hello   go 1.26.0\n" +
		"  + cc\n" +
		"  ~ go\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
	b.Reset()
	printChanges(o, nil)
	if b.String() != "bob.lock: no changes\n" {
		t.Errorf("got %q", b.String())
	}
}

func testGraph() *ir.Graph {
	n := func(id, label, file string, line int, deps ...string) *ir.Node {
		return &ir.Node{ID: id, Kind: "k.x", Label: label, DefinedAt: ir.Pos{File: file, Line: line}, Deps: deps}
	}
	return &ir.Graph{Nodes: []*ir.Node{
		n("1", "//cmd/api:api", "cmd/api/bob.go", 9, "2", "4"),
		n("2", "//libs/foo:foo", "libs/foo/bob.go", 12, "3"),
		n("3", "nixpkgs:openssl", "libs/foo/bob.go", 15),
		n("4", "//libs/bar:bar", "libs/bar/bob.go", 3),
	}}
}

func TestGraphWhy(t *testing.T) {
	g := graphOf(testGraph())
	p := g.paths("//cmd/api:api", "nixpkgs:openssl", 1)
	if len(p) != 1 || strings.Join(p[0], " ") != "//cmd/api:api //libs/foo:foo nixpkgs:openssl" {
		t.Fatalf("paths = %v", p)
	}
	var b bytes.Buffer
	printPath(&out{w: &b}, g, p[0])
	want := "//cmd/api:api                   cmd/api/bob.go:9\n" +
		"└─ //libs/foo:foo               libs/foo/bob.go:12\n" +
		"   └─ nixpkgs:openssl           libs/foo/bob.go:15\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
	if len(g.paths("//libs/bar:bar", "nixpkgs:openssl", 1)) != 0 {
		t.Error("path where none exists")
	}
	r := g.reverse()
	if d := r.deps["nixpkgs:openssl"]; len(d) != 1 || d[0] != "//libs/foo:foo" {
		t.Errorf("rdeps = %v", d)
	}
	in := g.closure([]string{"//libs/foo:foo"}, -1)
	if len(in) != 2 || !in["nixpkgs:openssl"] {
		t.Errorf("closure = %v", in)
	}
}

func TestMatchLabel(t *testing.T) {
	for _, c := range []struct {
		p, l string
		ok   bool
	}{
		{"//...", "//a:a", true}, {"//...", "nixpkgs:x", false}, {"//libs/...", "//libs/foo:foo", true},
		{"//libs/...", "//libsx:x", false}, {"//libs/foo:all", "//libs/foo:bar", true}, {"//a:a", "//a:a", true},
	} {
		if matchLabel(c.p, c.l) != c.ok {
			t.Errorf("matchLabel(%q, %q) != %v", c.p, c.l, c.ok)
		}
	}
}

func TestWriteExclude(t *testing.T) {
	root := t.TempDir()
	ex := filepath.Join(root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ex, []byte("# user\n*.o\n"+excludeBegin+"\n/old\n"+excludeEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeExclude(root, []string{".bob/", "web/node_modules"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ex)
	want := "# user\n*.o\n" + excludeBegin + "\n/.bob/\n/web/node_modules\n" + excludeEnd + "\n"
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

func TestComplete(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bob.lock"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(words ...string) string {
		var b strings.Builder
		runComplete(words, root, func(s string) { b.WriteString(s) })
		return b.String()
	}
	if got := run("build", "//li"); got != "" {
		t.Errorf("without .bob/labels: %q", got)
	}
	recordLabels(root, []string{"//libs/foo:foo", "//cmd/api:api", "//libs/foo:foo"})
	if got := run("build", "//li"); got != "//libs/foo:foo\n" {
		t.Errorf("labels: %q", got)
	}
	if got := run("ch"); got != "check\n" {
		t.Errorf("commands: %q", got)
	}
	if got := run("env", "--format", ""); got != "sh\nfish\njson\n" {
		t.Errorf("values: %q", got)
	}
}
