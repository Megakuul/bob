// Package progress lets slow engine steps announce what they are doing, so that bob's command
// line can show a live status line (SPEC §02.5). The engine only reports phases through the
// context; without a reporter in the context every call does nothing.
package progress

import (
	"context"
	"fmt"
)

// Reporter receives the phases of one command. Begin starts a phase and returns the function
// that ends it; phases nest, and the newest one is the one shown, except that a weak phase (a
// detail of a bigger step, such as the Nix command behind a build) is shown only while no
// other phase runs.
type Reporter interface {
	Begin(text string, weak bool) (end func())
}

type key struct{}

// With returns a context whose Phase calls go to r.
func With(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, key{}, r)
}

// Phase announces what is happening now, in terms of the user ("evaluating bob.go files",
// "fetching go-1.26.8"), and returns the function that ends the phase: call it when the step
// is over, usually with defer. It never returns nil.
func Phase(ctx context.Context, format string, args ...any) (end func()) {
	return begin(ctx, false, format, args)
}

// Detail is Phase for a step inside a bigger one that reports itself: shown only while nothing
// else is.
func Detail(ctx context.Context, format string, args ...any) (end func()) {
	return begin(ctx, true, format, args)
}

func begin(ctx context.Context, weak bool, format string, args []any) func() {
	r, _ := ctx.Value(key{}).(Reporter)
	if r == nil {
		return func() {}
	}
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	if e := r.Begin(format, weak); e != nil {
		return e
	}
	return func() {}
}
