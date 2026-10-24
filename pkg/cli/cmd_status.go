package cli

import (
	"strings"

	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/materialise"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runStatus is `bob status [--json]` (SPEC §02.1, §03.6.6): per generated root fresh | stale |
// missing | edited | orphan, and the freshness of bob.lock. It reads only: nothing is built,
// locked or written, and it exits 0 whatever the states are (bob gen --check is the gate).
func runStatus(e *env) int {
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	res, err := build.Status(e.ctx, build.Options{Root: root, Store: st})
	if err != nil {
		return e.out.fail(err)
	}
	type lockStatus struct {
		State    string                  `json:"state"` // fresh | stale
		Findings []translate.ErrorObject `json:"findings"`
	}
	lk := lockStatus{State: materialise.StateFresh, Findings: []translate.ErrorObject{}}
	for _, f := range res.Lock {
		lk.State = materialise.StateStale
		lk.Findings = append(lk.Findings, translate.NewErrorObject(f, f.Hint()))
	}
	roots := append([]materialise.RootStatus{}, res.Roots...)
	e.out.result(struct {
		Roots []materialise.RootStatus `json:"roots"`
		Lock  lockStatus               `json:"lock"`
	}{roots, lk})

	width := len("bob.lock")
	for _, r := range roots {
		width = max(width, len(rootName(r)))
	}
	for _, r := range roots {
		e.out.printf("%-7s  %-*s  %s\n", r.State, width, rootName(r), r.Label)
		for _, d := range r.Detail {
			e.out.printf("         %s\n", d)
		}
	}
	e.out.printf("%-7s  %s\n", lk.State, "bob.lock")
	for _, f := range res.Lock {
		e.out.printf("         %s\n", strings.TrimSpace(f.Error()))
	}
	return e.out.finish(0, translate.Counts{}, nil)
}

// rootName is the owned directory with a trailing slash ("-" for a root not written yet).
func rootName(r materialise.RootStatus) string {
	if r.Dir == "" {
		return "-"
	}
	return r.Dir + "/"
}
