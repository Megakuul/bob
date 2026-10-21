// Command bob-builder is the program that runs inside bob derivations (SPEC §1.12).
package main

import (
	"fmt"
	"os"

	"github.com/megakuul/bob/pkg/bob/diag"
)

func main() {
	err := diag.NotImplemented("bob-builder")
	fmt.Fprintf(os.Stderr, "error[BOB-E-%04d]: %s\n", uint16(err.Code), err.Error())
	os.Exit(diag.ExitCode([]*diag.Error{err}))
}
