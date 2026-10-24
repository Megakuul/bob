// Command bobtoy is bob with the toy ecosystem linked in (R-FW-051): the only difference to
// cmd/bob is the blank import.
package main

import (
	"os"

	"github.com/megakuul/bob/pkg/cli"
	_ "github.com/megakuul/bob/tests/core/toyeco"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
