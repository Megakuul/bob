package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/megakuul/bob/tools/internal/budgets"
	"github.com/megakuul/bob/tools/internal/parity"
)

func init() {
	register(Stage{Name: "gen", Order: 3, Run: runGen})
}

// runGen regenerates the generated files and fails if one changed, i.e. the
// committed copy was stale (HARNESS.md §1.3 stage 3). It compares file
// contents instead of running git diff, so unrelated uncommitted work never
// fails it. docs/parity.md is left out: it changes with every run's test
// results and is written by the parity stage. TODO(M-proto, M0.7): buf
// generate and the codes.lock check join here.
func runGen(ctx context.Context, env *Env) error {
	if _, err := parity.Generate(ctx, env.Root); err != nil {
		return err
	}
	path := filepath.Join(env.Root, budgets.FilePath)
	before, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return harnessErr(err)
	}
	if err := budgets.Write(env.Root); err != nil {
		return err
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return harnessErr(err)
	}
	if !bytes.Equal(before, after) {
		return fmt.Errorf("gen: %s was stale and has been regenerated; review and commit it", budgets.FilePath)
	}
	return nil
}
