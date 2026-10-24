package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/sweep"
)

// storeFsck is `bob-server store fsck`: every record of the control and fleet buckets must
// decode and validate (R-NFR-007). It LISTs both buckets (an operator command, never part of
// a running server) and prints each problem; exit 1 when there is any.
func storeFsck(getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := store.StorageConfigFromEnv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		return store.ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	c, err := sweep.NewFsckClient(ctx, cfg.Endpoint, cfg.Region)
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		return store.ExitConfig
	}
	bad, checked := 0, 0
	for _, b := range []struct{ name, role string }{{cfg.Control, "control"}, {cfg.Fleet, "fleet"}} {
		keys, err := sweep.Keys(ctx, c, b.name)
		if err != nil {
			fmt.Fprintf(stderr, "✗ %v\n", err)
			return 1
		}
		bucket, err := store.OpenUnprobed(ctx, store.BucketConfig{Endpoint: cfg.Endpoint, Region: cfg.Region, Name: b.name, Role: b.role})
		if err != nil {
			fmt.Fprintf(stderr, "✗ %v\n", err)
			return store.ExitConfig
		}
		problems, err := orchestrator.Fsck(ctx, bucket, b.role, keys)
		if err != nil {
			fmt.Fprintf(stderr, "✗ fsck %s: %v\n", b.name, err)
			return 1
		}
		for _, p := range problems {
			fmt.Fprintln(stdout, p.String())
		}
		bad += len(problems)
		checked += len(keys)
	}
	fmt.Fprintf(stderr, "%d objects checked, %d problems\n", checked, bad)
	if bad > 0 {
		return 1
	}
	return 0
}
