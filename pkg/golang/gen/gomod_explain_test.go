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
	explain := explainGoMod("1.26.0", req, nil, "bob.go")
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
