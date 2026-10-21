// Command gomodcheck exercises go.mod/go.sum generation for the fixture (spike S2):
//
//	gomodcheck -proxy DIR -ws DIR [-write]
//
// It computes the bob.lock Go entries (h1 hashes) from the proxy-layout directory, prints them,
// compares the generated go.sum with -gosum (a go.sum written by the go command), writes
// go.mod/go.sum into -ws with -write, and otherwise reports drift.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"bobspike/s07/gomod"
)

type mv struct {
	path, version    string
	packages, direct bool
}

// The fixture's module graph: three package-providing modules plus the two modules that only
// appear in the pruned module graph (requirements of google.golang.org/protobuf).
var graph = []mv{
	{"github.com/google/uuid", "v1.6.0", true, true},
	{"golang.org/x/sync", "v0.24.0", true, true},
	{"google.golang.org/protobuf", "v1.36.11", true, true},
	{"github.com/golang/protobuf", "v1.5.0", false, false},
	{"github.com/google/go-cmp", "v0.7.0", false, false},
}

func main() {
	proxy := flag.String("proxy", "", "GOPROXY-layout directory")
	ws := flag.String("ws", "", "workspace module directory")
	gosum := flag.String("gosum", "", "go.sum written by the go command, to compare")
	write := flag.Bool("write", false, "write go.mod and go.sum into -ws")
	flag.Parse()
	var mods []gomod.Module
	for _, m := range graph {
		d := filepath.Join(*proxy, m.path, "@v")
		modData, err := os.ReadFile(filepath.Join(d, m.version+".mod"))
		check(err)
		mh, err := gomod.HashMod(modData)
		check(err)
		e := gomod.Module{Path: m.path, Version: m.version, ModH1: mh, Direct: m.direct, Packages: m.packages}
		if m.packages {
			e.H1, err = gomod.HashZip(filepath.Join(d, m.version+".zip"))
			check(err)
		}
		mods = append(mods, e)
	}
	j, _ := json.MarshalIndent(mods, "", "  ")
	fmt.Printf("bob.lock go entries:\n%s\n", j)
	want := map[string]string{
		"go.mod": gomod.GoMod("example.com/api", "1.26", mods),
		"go.sum": gomod.GoSum(mods),
	}
	fmt.Printf("--- generated go.mod\n%s--- generated go.sum\n%s", want["go.mod"], want["go.sum"])
	if *gosum != "" {
		have, err := os.ReadFile(*gosum)
		check(err)
		fmt.Printf("--- go.sum lines written by the go command that bob's go.sum lacks:\n")
		gen := map[string]bool{}
		for _, l := range splitLines(want["go.sum"]) {
			gen[l] = true
		}
		missing := 0
		for _, l := range splitLines(string(have)) {
			if !gen[l] {
				fmt.Println("  ", l)
				missing++
			}
		}
		fmt.Printf("   (%d missing)\n", missing)
	}
	if *write {
		for n, c := range want {
			check(os.WriteFile(filepath.Join(*ws, n), []byte(c), 0o444))
		}
		return
	}
	if err := gomod.Drift(*ws, want); err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	fmt.Println("no drift")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
