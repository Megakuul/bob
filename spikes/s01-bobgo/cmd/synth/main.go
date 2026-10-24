// Command synth writes a synthetic repo with N bob.go files (spike S1).
// Package i depends on i-1, i/2 and i/3 (distinct, >0) through typed imports. Every second
// directory also contains ordinary Go code of the same package (to exercise the merge rule).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	n := flag.Int("n", 100, "number of bob.go files")
	out := flag.String("o", "", "output dir")
	fw := flag.String("fw", "", "absolute path of the framework module (replace target)")
	wide := flag.Bool("wide", false, "depend on i/2 and i/3 only (import depth ~log2 N) instead of a chain through i-1")
	flag.Parse()
	must(os.MkdirAll(*out, 0o755))
	must(os.WriteFile(filepath.Join(*out, "go.mod"), []byte(fmt.Sprintf("module example.com/synth\n\ngo 1.26\n\nrequire github.com/megakuul/bob v0.0.0\n\nreplace github.com/megakuul/bob => %s\n", *fw)), 0o644))
	for i := 1; i <= *n; i++ {
		dir := filepath.Join(*out, rel(i))
		must(os.MkdirAll(dir, 0o755))
		deps := map[int]bool{}
		for _, d := range pick(i, *wide) {
			if d > 0 && d != i {
				deps[d] = true
			}
		}
		var imps, refs []string
		for d := range deps {
			imps = append(imps, fmt.Sprintf("\t%q", "example.com/synth/"+rel(d)))
			refs = append(refs, fmt.Sprintf("%s.Lib(p)", name(d)))
		}
		src := fmt.Sprintf(`//go:build bob

package %s

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
%s
)

// Lib is //%s:%s.
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.StaticLibrary(p, %q, cc.LibraryOpts{
		Srcs: bob.Glob("src/*.c"),
		Hdrs: "include",
		Std:  cc.C17,
		Deps: []cc.Linkable{%s},
	})
})
`, name(i), strings.Join(imps, "\n"), rel(i), name(i), name(i), strings.Join(refs, ", "))
		must(os.WriteFile(filepath.Join(dir, "bob.go"), []byte(src), 0o644))
		if i%2 == 0 {
			must(os.WriteFile(filepath.Join(dir, "code.go"), []byte(fmt.Sprintf("package %s\n\n// Value is ordinary Go code.\nfunc Value() int { return %d }\n", name(i), i)), 0o644))
		}
	}
}

func pick(i int, wide bool) []int {
	if wide {
		return []int{i / 2, i / 3}
	}
	return []int{i - 1, i / 2, i / 3}
}

func name(i int) string { return fmt.Sprintf("p%04d", i) }
func rel(i int) string  { return fmt.Sprintf("pkgs/g%02d/%s", i/50, name(i)) }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
