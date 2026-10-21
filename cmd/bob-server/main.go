// Command bob-server is the bob build and cache server (modes: orchestrator, worker, admin).
package main

import (
	"fmt"
	"os"

	"github.com/megakuul/bob/pkg/bob/diag"
)

func main() {
	mode := "bob-server"
	if len(os.Args) > 1 {
		mode += " " + os.Args[1]
	}
	err := diag.NotImplemented(mode)
	fmt.Fprintf(os.Stderr, "error[BOB-E-%04d]: %s\n", uint16(err.Code), err.Error())
	os.Exit(diag.ExitCode([]*diag.Error{err}))
}
