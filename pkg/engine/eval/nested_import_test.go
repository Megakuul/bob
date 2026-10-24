package eval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
)

// nestedRepo has a Go module of the repository at tools/ (bob generated its go.mod, module
// example.com/tools) and a bob.go below it that imports the tools bob.go package by that
// module's path, as the go fixture's tools/tar/bob.go does (R-FW-002 R7, I-M12a NEEDS 2).
func nestedRepo() map[string]string {
	files := coreRepo()
	files["tools/go.mod"] = engine.GoModHeader + "\n// Edit tools/bob.go and run bob lock.\nmodule example.com/tools\n\ngo 1.26.0\n"
	files["tools/bob.go"] = `//go:build bob

package tools

import "github.com/megakuul/bob/pkg/bob"

// Stamp is //tools:stamp.
var Stamp = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "stamp", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"out"}})
})
`
	files["tools/tar/bob.go"] = `//go:build bob

package tar

import (
	"example.com/tools"
	"github.com/megakuul/bob/pkg/bob"
)

// Tar is //tools/tar:tar.
var Tar = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "tar", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"stamp": tools.Stamp(p)}})
})
`
	return files
}

func TestOwnDirNestedModule(t *testing.T) {
	nested := map[string]string{"example.com/tools": "tools", "example.com/core/sub": "sub"}
	for _, c := range []struct {
		p, dir string
		ok     bool
	}{
		{"example.com/core", ".", true},
		{"example.com/core/lib", "lib", true},
		{"example.com/tools", "tools", true},
		{"example.com/tools/tar", "tools/tar", true},
		{"example.com/toolsx", "", false},
		{"example.com/core/sub/x", "sub/x", true},
		{"example.com/other", "", false},
	} {
		dir, ok := ownDirOf("example.com/core", nested, c.p)
		if dir != c.dir || ok != c.ok {
			t.Errorf("ownDirOf(%q) = %q, %v; want %q, %v", c.p, dir, ok, c.dir, c.ok)
		}
	}
}

// The R7 check accepts the import, and the evaluator compiles it as the root module's package
// of that directory (rewritten import on the same line: positions keep their lines).
func TestNestedModuleImport(t *testing.T) {
	root := writeRepo(t, "core", nestedRepo())
	scan, err := scanRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if scan.nested["example.com/tools"] != "tools" {
		t.Fatalf("nested modules %v", scan.nested)
	}
	if err := scan.load(root); err != nil {
		t.Fatal(err)
	}
	own := map[string]*bobFile{}
	for _, d := range scan.bobDirs {
		own[d] = parseBob(d, bobPath(d), scan.content[bobPath(d)])
	}
	rc := &ruleCheck{root: root, module: "example.com/core", scan: scan, own: own, deps: &depSet{mods: map[string]*depModule{}},
		fwPkgs: map[string]bool{frameworkModule + "/pkg/bob": true}}
	if errs, _ := rc.check(); len(errs) > 0 {
		t.Fatalf("rule violations: %v", errs)
	}
	src, err := rewriteImports(own["tools/tar"], func(p string) (string, bool) {
		if p == "example.com/tools" {
			return "example.com/core/tools", true
		}
		return "", false
	})
	if err != nil || !strings.Contains(string(src), "\t\"example.com/core/tools\"\n") ||
		strings.Count(string(src), "\n") != strings.Count(string(own["tools/tar"].src), "\n") {
		t.Fatalf("rewritten source (%v):\n%s", err, src)
	}

	res, err := evaluate(t, root)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes []struct {
			Label     string `json:"label"`
			DefinedAt struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"definedAt"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatal(err)
	}
	at := map[string]string{}
	for _, n := range doc.Nodes {
		at[n.Label] = n.DefinedAt.File + ":" + itoa(n.DefinedAt.Line)
	}
	if at["//tools/tar:tar"] != "tools/tar/bob.go:12" || at["//tools:stamp"] != "tools/bob.go:9" {
		t.Errorf("labels and positions: %v", at)
	}
}
