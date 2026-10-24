// Package safe starts goroutines that cannot crash the process (AGENTS.md §5.3). It is the
// only package of internal/ and cmd/ with a `go` statement (lint rule safego).
package safe

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// ErrPanic marks an error that was a recovered panic.
var ErrPanic = errors.New("panic")

// Go runs fn in a new goroutine. The returned channel receives fn's result exactly once and
// is then closed; a panic in fn arrives as an error wrapping ErrPanic (logged with its stack).
func Go(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- Run(fn)
	}()
	return done
}

// Run calls fn and converts a panic into an error wrapping ErrPanic.
func Run(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("%w: %v", ErrPanic, r)
		}
	}()
	return fn()
}
