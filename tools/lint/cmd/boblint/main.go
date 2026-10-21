// Command boblint runs bob's analyzers (tools/lint) as a multichecker.
// Flags named after an analyzer (-nopanic, ...) select a subset.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/megakuul/bob/tools/lint"
)

func main() { multichecker.Main(lint.Analyzers...) }
