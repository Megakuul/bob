package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// protoBaseline is what buf breaking compares against: main until the first
// release, then the last release tag (HARNESS.md §1.3 stage 8).
const protoBaseline = "main"

func init() {
	register(Stage{Name: "proto", Order: 8, Run: runProto})
}

func runProto(ctx context.Context, env *Env) error {
	dir := filepath.Join(env.Root, "proto")
	if _, err := goTool(ctx, dir, "buf", "lint"); err != nil {
		return err
	}
	// A baseline without .proto files has nothing to break; buf would fail
	// on the empty input instead.
	files, err := command(ctx, env.Root, nil, "git", "ls-tree", "-r", "--name-only", protoBaseline, "--", "proto")
	if err != nil {
		return harnessErr(err)
	}
	if !strings.Contains(files, ".proto\n") {
		fmt.Fprintf(os.Stderr, "proto: %s has no .proto files yet, buf breaking skipped\n", protoBaseline)
		return nil
	}
	against := fmt.Sprintf("%s#branch=%s,subdir=proto", filepath.Join(env.Root, ".git"), protoBaseline)
	_, err = goTool(ctx, dir, "buf", "breaking", "--against", against)
	return err
}
