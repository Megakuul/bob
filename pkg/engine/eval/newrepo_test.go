package eval

import "testing"

// A new repository (no root go.mod) gets the module path its bob.go files use: an import of a
// subdirectory, the root Go module's Path, or an import of the root package (ADR 0020: the
// binary's bob.go imports the root package), else the directory name.
func TestNewRepoModule(t *testing.T) {
	file := func(dir, src string) *bobFile { return parseBob(dir, dir+"/bob.go", []byte(src)) }
	head := "//go:build bob\n\npackage x\n\n"
	for name, c := range map[string]struct {
		own  map[string]*bobFile
		want string
	}{
		"subdir import": {map[string]*bobFile{
			".":    file(".", head+"import (\n\t\"example.com/app/libs\"\n\t\"github.com/megakuul/bob/pkg/bob\"\n)\n"),
			"libs": file("libs", head+"import \"github.com/megakuul/bob/pkg/bob\"\n"),
		}, "example.com/app"},
		"root module path": {map[string]*bobFile{
			".":         file(".", head+"import (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/golang\"\n)\n\nvar M = bob.Define(func(p *bob.P) *golang.Module {\n\treturn golang.NewModule(p, golang.ModuleOpts{Path: \"example.com/hello-go\", Go: \"1.26\"})\n})\n"),
			"cmd/hello": file("cmd/hello", head+"import (\n\t\"example.com/hello-go\"\n\t\"github.com/megakuul/bob/pkg/bob\"\n)\n"),
		}, "example.com/hello-go"},
		"root import": {map[string]*bobFile{
			".":       file(".", head+"import (\n\t\"fmt\"\n\t\"github.com/megakuul/bob/pkg/bob\"\n)\n\nvar _ = bob.Import(\"example.com/other\", \"v1.0.0\")\n"),
			"cmd/app": file("cmd/app", head+"import (\n\t\"example.com/other/libs\"\n\t\"example.com/mine\"\n)\n"),
		}, "example.com/mine"},
		"directory name": {map[string]*bobFile{
			".": file(".", head+"import \"github.com/megakuul/bob/pkg/bob\"\n"),
		}, "proj"},
	} {
		if got := newRepoModule("/x/proj", c.own); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
