// Command bob-builder is the program that runs inside bob derivations (SPEC §1.12).
//
// It depends on the standard library, pkg/builder and the ecosystems' builder operations
// (pkg/<eco>/bop) only: its store path is an input of every bob derivation (R-NIX-015), so
// unrelated changes must not change its bytes.
package main

import (
	"os"

	"github.com/megakuul/bob/pkg/builder"
	gobop "github.com/megakuul/bob/pkg/golang/bop"
)

func main() {
	os.Exit(builder.Main(os.Args[1:], kinds(), os.Stdout, os.Stderr))
}

// kinds are the core kinds plus every ecosystem's.
func kinds() map[string]builder.Kind {
	k := builder.Core()
	for name, kind := range gobop.Kinds() {
		k[name] = kind
	}
	return k
}
