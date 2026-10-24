package translate

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Definition errors SPEC writes as `✗ <message>` (R-CC-002a: BOB-E-1020, R-PLAT-006:
// BOB-E-0006) render with that head; other codes keep `error[CODE]:` (the S15 goldens).
func TestDefinitionLine(t *testing.T) {
	flag := diag.New(1020, "label", "libs/mathx/bob.go:12", "option", "build definition",
		"reason", `//libs/mathx:mathx: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`)
	flag.Label = "//libs/mathx:mathx"
	flag.Location = &diag.Pos{File: "libs/mathx/bob.go", Line: 12}
	plat := diag.New(6, "label", "//libs/restricted:foo", "platform", "linux-amd64", "platforms", "linux-arm64")
	plat.DefinedAt = diag.Pos{File: "libs/restricted/bob.go", Line: 4}
	plat.Excerpt = []string{"//apps/app:app → //libs/restricted:foo"}
	opt := diag.New(1020, "label", "//img:img", "option", "MaxLayers", "reason", "must be between 2 and 125 (got 1)")
	opam := diag.New(1015, "ecosystem", "opam", "root", "//app:app", "reason", `ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches`)
	golang := diag.New(1015, "ecosystem", "Go", "root", "this repository", "reason", "boom")
	cycle := diag.New(1006, "cycle", "//a:a → //a:a")
	typeErr := diag.New(1020, "label", "cmd/api/bob.go:31:13", "option", "build definition",
		"reason", `cannot use "x" (untyped string constant) as int value in variable declaration`)
	typeErr.Label, typeErr.Location = "", &diag.Pos{File: "cmd/api/bob.go", Line: 31, Col: 13}

	cases := []struct {
		e    *diag.Error
		want []string
	}{
		{flag, []string{
			`✗ //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`,
			"  --> libs/mathx/bob.go:12",
		}},
		{plat, []string{
			"✗ //libs/restricted:foo does not support linux-amd64 (it allows: linux-arm64)",
			"  package defined at libs/restricted/bob.go:4",
			"   | //apps/app:app → //libs/restricted:foo",
		}},
		{opt, []string{"✗ //img:img: invalid MaxLayers: must be between 2 and 125 (got 1)"}},
		{opam, []string{`✗ cannot resolve opam packages: ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches`}},
		{golang, []string{"error[BOB-E-1015]: cannot resolve Go dependencies for this repository: boom"}},
		{cycle, []string{"error[BOB-E-1006]: dependency cycle: //a:a → //a:a"}},
		{typeErr, []string{ // R-FW-043 (c): a compiler message keeps Go's position prefix
			`✗ cmd/api/bob.go:31:13: cannot use "x" (untyped string constant) as int value in variable declaration`,
			"  --> cmd/api/bob.go:31:13",
		}},
	}
	for _, c := range cases {
		var b strings.Builder
		Block(&b, c.e, "", Style{})
		got := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
		for i, w := range c.want {
			if i >= len(got) || got[i] != w {
				t.Errorf("%s: block\n%s\nwant line %d %q", c.e.Code.ID(), b.String(), i, w)
				break
			}
		}
	}
	// Colour: the glyph takes the error colour.
	var b strings.Builder
	Block(&b, plat, "", Style{Color: true})
	if !strings.HasPrefix(b.String(), cRedBold+"✗"+cReset+" ") {
		t.Errorf("coloured block %q", b.String())
	}
}
