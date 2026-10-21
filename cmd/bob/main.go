// Command bob is the bob command line.
package main

import (
	"os"

	"github.com/megakuul/bob/pkg/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
