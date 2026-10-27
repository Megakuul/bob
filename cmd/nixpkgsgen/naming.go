package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/nixidx/goname"
)

// reserved are the hand-written identifiers of package nixpkgs and the removed string role
// functions (old code must fail with "undefined: nixpkgs.Lib", ADR 0020 §2). An attribute whose
// Go name is one of them takes the fallback name.
var reserved = map[string]bool{
	"Attr": true, "Option": true, "Output": true, "Outputs": true, "Modules": true, "Cross": true,
	"AllowInsecure": true, "AllowUnfree": true, "MainProgram": true, "Package": true,
	"ToolPackage": true, "Program": true, "Tool": true, "Lib": true, "Pkg": true, "Exe": true,
	"Name": true, "Out": true, "OutBin": true, "OutDev": true, "OutLib": true, "OutMan": true,
	"OutDoc": true, "OutDebug": true, "OutStatic": true,
}

// GoName is the Go identifier of a nixpkgs attribute (ADR 0020 §2); the rule lives in
// pkg/engine/nixidx/goname so bob's pin check (BOB-E-1001 variant pin) names the same handle.
func GoName(attr string) string { return goname.Of(attr) }

// fallbackName is the name of every member of a collision group: "A_" + the attribute with
// '-', '.', "'" and '+' replaced by '_'.
func fallbackName(attr string) string {
	return "A_" + strings.NewReplacer("-", "_", ".", "_", "'", "_", "+", "_").Replace(attr)
}

// assignNames maps attributes to unique Go names and returns the collision groups (Go name →
// attributes, sorted) for the file header.
func assignNames(attrs []string) (map[string]string, map[string][]string) {
	byName := map[string][]string{}
	for _, a := range attrs {
		n := GoName(a)
		byName[n] = append(byName[n], a)
	}
	names := map[string]string{}
	groups := map[string][]string{}
	for n, as := range byName {
		sort.Strings(as)
		if len(as) == 1 && !reserved[n] && n != "" {
			names[as[0]] = n
			continue
		}
		groups[n] = as
		for _, a := range as {
			names[a] = fallbackName(a)
		}
	}
	// A fallback may still collide (with a regular name or another fallback): those get a
	// hash suffix.
	count := map[string]int{}
	for _, n := range names {
		count[n]++
	}
	for a, n := range names {
		if count[n] > 1 && strings.HasPrefix(n, "A_") {
			h := fnv.New32a()
			h.Write([]byte(a))
			names[a] = fmt.Sprintf("%s_%08x", n, h.Sum32())
		}
	}
	return names, groups
}
