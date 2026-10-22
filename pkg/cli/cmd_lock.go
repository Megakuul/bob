package cli

import (
	"strings"
	"unicode/utf8"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runLock is `bob lock [--update [names…]] [--nixpkgs <rev>] [--rebuild] [--offline]` (SPEC
// R-CLI-010): add missing pins, drop unused ones, move only what --update names.
func runLock(e *env) int {
	lo := build.LockOptions{Nixpkgs: e.a.str("nixpkgs"), Rebuild: e.a.bool("rebuild")}
	if e.a.bool("update") {
		lo.Update = append([]string{}, e.a.pos...)
		lo.All = len(lo.Update) == 0
	} else if len(e.a.pos) > 0 {
		return e.out.fail(diag.New(4, "flag", "bob lock "+e.a.pos[0], "what", "--update before the names"))
	}
	root, err := e.root(true)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	res, err := build.Lock(e.ctx, build.Options{Root: root, Patterns: []string{"//..."}, Store: st, Offline: e.a.bool("offline")}, lo)
	if err != nil {
		return e.out.fail(err)
	}
	type change struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
		From      string `json:"from"`
		To        string `json:"to"`
		Op        string `json:"op"`
	}
	changes := []change{}
	for _, c := range res.Changes {
		changes = append(changes, change{c.Ecosystem, c.Name, c.From, c.To, c.Op})
	}
	e.out.result(struct {
		Changes []change `json:"changes"`
	}{changes})
	if !e.out.json {
		printChanges(e.out, res.Changes)
		for _, n := range res.Notes {
			e.out.printf("note: %s\n", n)
		}
		if res.Reformatted {
			e.out.printf("note: bob.lock was reformatted\n")
		}
	}
	return e.out.finish(0, translate.Counts{}, nil)
}

// printChanges prints the change list of SPEC §02.5:
//
//	bob.lock: 3 changes
//	  ~ go     golang.org/x/net   v0.30.0 → v0.31.0
func printChanges(o *out, cs []build.LockChange) {
	if len(cs) == 0 {
		o.printf("bob.lock: no changes\n")
		return
	}
	word := "changes"
	if len(cs) == 1 {
		word = "change"
	}
	o.printf("bob.lock: %d %s\n", len(cs), word)
	ew, nw := 5, 0
	for _, c := range cs {
		ew = max(ew, utf8.RuneCountInString(c.Ecosystem))
		nw = max(nw, utf8.RuneCountInString(c.Name))
	}
	for _, c := range cs {
		mark, ver := "~", c.From+" → "+c.To
		switch c.Op {
		case "add":
			mark, ver = "+", c.To
		case "remove":
			mark, ver = "-", c.From
		}
		o.printf("  %s %s%s%s%s%s\n", mark, c.Ecosystem, strings.Repeat(" ", ew-utf8.RuneCountInString(c.Ecosystem)+2),
			c.Name, strings.Repeat(" ", nw-utf8.RuneCountInString(c.Name)+3), ver)
	}
}
