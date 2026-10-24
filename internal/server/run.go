// Package server is the process frame of `bob-server orchestrator` and `bob-server worker`:
// configuration from the environment, start-up checks through internal/store, the
// components a process runs (listeners, controllers, the worker loop) and graceful shutdown.
//
// The controllers (internal/orchestrator) and the worker loop (internal/worker) plug in as
// Components through OrchestratorParts and WorkerParts; this package never reaches into them.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/megakuul/bob/internal/safe"
)

// Component is a long-running part of a server process.
type Component interface {
	Name() string
	// Run works until ctx is done and then returns nil. An error (or a panic) means the
	// component failed; it is restarted with backoff unless it is Critical.
	Run(ctx context.Context) error
}

// Critical marks a component whose failure ends the process (a listener that stopped
// serving), instead of restarting it.
type Critical interface{ Critical() bool }

// Func adapts a function to a Component.
type Func struct {
	N string
	F func(ctx context.Context) error
}

// Name implements Component.
func (f Func) Name() string { return f.N }

// Run implements Component.
func (f Func) Run(ctx context.Context) error { return f.F(ctx) }

// Backoff bounds of restarting a failed component.
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Run runs the components until ctx is done or a critical component fails, then cancels the
// rest and waits for them up to grace. A failing non-critical component is logged and
// restarted with exponential backoff (1 s … 30 s): a controller bug must not take the
// orchestrator's API down with it.
func Run(ctx context.Context, log *slog.Logger, grace time.Duration, cs ...Component) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var dones []<-chan error
	for _, c := range cs {
		dones = append(dones, safe.Go(func() error { return supervise(ctx, cancel, log, c) }))
	}
	<-ctx.Done()
	cause := context.Cause(ctx)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for i, d := range dones {
		select {
		case <-d:
		case <-timer.C:
			log.Warn("component did not stop in time", "component", cs[i].Name(), "grace", grace)
			return errors.Join(failure(cause), fmt.Errorf("server: %s did not stop within %s", cs[i].Name(), grace))
		}
	}
	return failure(cause)
}

// failure is the error of a run that ended with cause: nil for an orderly stop.
func failure(cause error) error {
	if cause == nil || errors.Is(cause, context.Canceled) {
		return nil
	}
	return cause
}

func supervise(ctx context.Context, fail context.CancelCauseFunc, log *slog.Logger, c Component) error {
	backoff := minBackoff
	for {
		start := time.Now()
		err := safe.Run(func() error { return c.Run(ctx) })
		if ctx.Err() != nil {
			return nil
		}
		if crit, ok := c.(Critical); ok && crit.Critical() {
			if err == nil {
				err = errors.New("stopped unexpectedly")
			}
			err = fmt.Errorf("server: %s: %w", c.Name(), err)
			fail(err)
			return err
		}
		if err == nil {
			return nil // finished its work
		}
		if time.Since(start) > maxBackoff {
			backoff = minBackoff
		}
		log.Error("component failed; restarting", "component", c.Name(), "err", err.Error(), "backoff", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}
