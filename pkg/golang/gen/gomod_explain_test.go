package gen

import (
	"testing"

	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

// TestExplainGoModTwoPartGoDirective: a go.mod written before bob with `go 1.26` (go mod init,
// the quickstart) declares the release bob writes as `go 1.26.0`: no difference in bob.go terms,
// so bob adopts the file instead of reporting a hand edit (BOB-E-1011, A452).
func TestExplainGoModTwoPartGoDirective(t *testing.T) {
	req := []gospec.Requirement{{Path: "golang.org/x/net", Version: "v0.1.0"}}
	explain := explainGoMod("1.26.0", req, nil, nil, "bob.go")
	for _, onDisk := range []string{
		"module example.com/hello\n\ngo 1.26\n",
		"module example.com/hello\n\ngo 1.26.0\n",
		"module example.com/hello\n\ngo 1.26\n\nrequire golang.org/x/net v0.1.0\n",
	} {
		if got := explain([]byte(onDisk)); len(got) != 0 {
			t.Errorf("%q: drift %v, want none", onDisk, got)
		}
	}
	for _, onDisk := range []string{
		"module example.com/hello\n\ngo 1.25\n",
		"module example.com/hello\n\ngo 1.26.1\n",
	} {
		if got := explain([]byte(onDisk)); len(got) != 1 || got[0].Op != "~" {
			t.Errorf("%q: drift %v, want one go line", onDisk, got)
		}
	}
}

// TestExplainFrameworkEdit: a hand edit of the framework's require or replace is one bob.go
// cannot express; the drift line carries the hint saying so (A550).
func TestExplainFrameworkEdit(t *testing.T) {
	req := []gospec.Requirement{{Path: frameworkModule, Version: "v0.0.0-dev"}}
	explain := explainGoMod("1.26.0", req, nil, map[string]string{frameworkModule: FrameworkReplace}, "bob.go")
	got := explain([]byte("module m\n\ngo 1.26.0\n\nrequire github.com/megakuul/bob v0.0.1\n"))
	if len(got) != 2 || got[0].Hint == "" || got[1].Hint == "" {
		t.Fatalf("drift = %+v", got)
	}
	ok := "module m\n\ngo 1.26.0\n\nrequire github.com/megakuul/bob v0.0.0-dev\n\nreplace github.com/megakuul/bob => ./.bob/framework\n"
	if d := explain([]byte(ok)); len(d) != 0 {
		t.Errorf("the generated file is drift: %+v", d)
	}
	block := "module m\n\ngo 1.26.0\n\nrequire (\n\tgithub.com/megakuul/bob v0.0.0-dev\n)\n\nreplace (\n\tgithub.com/megakuul/bob => ./.bob/framework\n)\n"
	if d := explain([]byte(block)); len(d) != 0 {
		t.Errorf("replace block form is drift: %+v", d)
	}
}
