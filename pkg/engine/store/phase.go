package store

import (
	"fmt"
	"strings"
)

// phaseText says, for the live status line (pkg/bob/progress), what a Nix command of bob's
// store does in user terms: the packages it fetches or builds, or the kind of store work.
// Quick commands never show up (the line appears only for steps that take a while).
func phaseText(name string, args []string) string {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch {
	case name == "nix-store" && verb == "--realise", name == "nix" && (verb == "build" || verb == "shell"), name == "nix-build":
		var names []string
		for _, a := range args[1:] {
			if n := storeName(a); n != "" {
				names = append(names, n)
			}
		}
		return "fetching or building " + nameList(names)
	case name == "nix-store" && (verb == "--import" || verb == "--restore"):
		return "importing into bob's store"
	case name == "nix-store" && (verb == "--export" || verb == "--dump"):
		return "exporting from bob's store"
	case name == "nix-store" && (verb == "--gc" || verb == "--delete"):
		return "cleaning bob's store"
	case name == "nix-store" && verb == "--verify":
		return "verifying bob's store"
	}
	return "working in bob's store"
}

// storeName is "go-1.26.8" for "/nix/store/<hash>-go-1.26.8.drv^out", "" for anything else.
func storeName(arg string) string {
	base, ok := strings.CutPrefix(arg, "/nix/store/")
	if !ok || len(base) < 34 || base[32] != '-' {
		return ""
	}
	base = base[33:]
	if i := strings.IndexAny(base, "^!/"); i >= 0 {
		base = base[:i]
	}
	return strings.TrimSuffix(base, ".drv")
}

// nameList is "a", "a, b" or "a, b and 3 more".
func nameList(names []string) string {
	switch len(names) {
	case 0:
		return "packages"
	case 1, 2:
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, %s and %d more", names[0], names[1], len(names)-2)
}
