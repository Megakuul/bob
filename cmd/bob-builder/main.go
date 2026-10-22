// Command bob-builder is the program that runs inside bob derivations (SPEC §1.12).
//
// It depends on the standard library and pkg/builder only: its store path is an input of every
// bob derivation (R-NIX-015), so unrelated changes must not change its bytes.
package main

import (
	"os"

	"github.com/megakuul/bob/pkg/builder"
)

func main() {
	os.Exit(builder.Main(os.Args[1:], builder.Core(), os.Stdout, os.Stderr))
}
