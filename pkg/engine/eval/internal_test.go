package eval

import (
	"encoding/json"
	"testing"
)

func TestInternalRoot(t *testing.T) {
	for _, c := range []struct {
		d, root string
		ok      bool
	}{
		{"internal", ".", true},
		{"internal/a", ".", true},
		{"api/internal/ffi", "api", true},
		{"a/internal/b/internal/c", "a/internal/b", true},
		{"a/internalx/b", "", false},
		{"lib", "", false},
	} {
		root, ok := internalRoot(c.d)
		if root != c.root || ok != c.ok {
			t.Errorf("internalRoot(%q) = %q, %v; want %q, %v", c.d, root, ok, c.root, c.ok)
		}
	}
}

// internalRepo has bob.go packages below internal/ directories at several depths (the go
// fixture's api/internal/ffi among them): the evaluator's main at the repository root may not
// import them under Go's internal rule, so they are registered through per-root stubs (A540).
func internalRepo() map[string]string {
	files := coreRepo()
	def := func(pkg, name string) string {
		return "//go:build bob\n\npackage " + pkg + "\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar " + name +
			" = bob.Define(func(p *bob.P) *bob.Step {\n\treturn bob.Run(p, \"" + pkg + "\", bob.RunOpts{Cmd: []string{\"true\"}, Outputs: []string{\"out\"}})\n})\n"
	}
	files["internal/top/bob.go"] = def("top", "Top")
	files["api/internal/ffi/bob.go"] = def("ffi", "Ffi")
	files["api/internal/ffi/ffi.go"] = "package ffi\n"
	files["api/internal/ffi2/bob.go"] = def("ffi2", "Ffi2")
	files["a/b/internal/c/internal/d/bob.go"] = def("d", "D")
	// A sibling package of the same internal root imports one of them.
	files["api/bob.go"] = `//go:build bob

package api

import (
	"example.com/core/api/internal/ffi"
	"github.com/megakuul/bob/pkg/bob"
)

var API = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "api", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"ffi": ffi.Ffi(p)}})
})
`
	return files
}

func TestEvaluateInternalPackages(t *testing.T) {
	res, err := evaluate(t, writeRepo(t, "core", internalRepo()))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes []struct {
			Label string `json:"label"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res.IR, &doc); err != nil {
		t.Fatalf("IR: %v\n%s", err, res.IR)
	}
	got := map[string]bool{}
	for _, n := range doc.Nodes {
		got[n.Label] = true
	}
	for _, l := range []string{"//internal/top:top", "//api/internal/ffi:ffi", "//api/internal/ffi2:ffi2",
		"//a/b/internal/c/internal/d:d", "//api:api", "//lib:gen"} {
		if !got[l] {
			t.Errorf("label %s missing; got %v", l, got)
		}
	}
}
