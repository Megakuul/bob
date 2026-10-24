// Command graph writes an ir.Graph as JSON.
//
//	go run ./cmd/graph -fixture -o fixture.graph.json
//	go run ./cmd/graph -synthetic 100 -dir /tmp/syn100 -o /tmp/syn100/graph.json
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"bobspike/s15/fixturegraph"
	"bobspike/s15/ir"
)

func main() {
	fix := flag.Bool("fixture", false, "emit the mixed fixture graph")
	syn := flag.Int("synthetic", 0, "emit a synthetic graph with N C libraries")
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
		g, err = fixturegraph.Synthetic(abs, *syn)
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
