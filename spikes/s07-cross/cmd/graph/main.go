// Command graph writes the S2 fixture graph or a synthetic Go workspace graph as JSON.
//
//	go run ./cmd/graph -fixture -o fixture.graph.json        (run in spikes/s02-go)
//	go run ./cmd/graph -gosyn 200 -width 20 -dir DIR -o DIR/graph.json
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"bobspike/s07/fixturegraph"
	"bobspike/s07/ir"
)

func main() {
	fix := flag.Bool("fixture", false, "emit the fixture graph (root = current directory)")
	syn := flag.Int("gosyn", 0, "emit a synthetic Go workspace with N packages")
	width := flag.Int("width", 20, "packages per layer (synthetic)")
	dir := flag.String("dir", "", "directory for synthetic sources")
	out := flag.String("o", "graph.json", "output file")
	flag.Parse()
	var g *ir.Graph
	var err error
	switch {
	case *fix:
		root, _ := filepath.Abs(".")
		var lock map[string]string
		if lock, err = fixturegraph.LoadLock(filepath.Join(root, "lock.json")); err == nil {
			g, err = fixturegraph.Fixture(root, lock)
		}
	case *syn > 0 && *dir != "":
		abs, _ := filepath.Abs(*dir)
		g, err = fixturegraph.GoSynthetic(abs, *syn, *width)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err == nil {
		err = g.Write(*out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "graph:", err)
		os.Exit(1)
	}
}
