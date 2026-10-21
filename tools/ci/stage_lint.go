package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// noNixDirs must never contain a .nix file: Nix stays invisible to users
// (AGENTS.md §5.5, HARNESS.md §1.5).
var noNixDirs = []string{"tests/fixtures/repos", "examples", "docs/guide"}

func init() {
	register(Stage{Name: "lint", Order: 5, Run: runLint})
}

func runLint(ctx context.Context, env *Env) error {
	if _, err := goTool(ctx, env.Root, "staticcheck", "./...", "./tools/..."); err != nil {
		return err
	}
	// bob's own analyzers (tools/lint) run on bob's module; the layering
	// stage runs importboundary on its own.
	if _, err := goCmd(ctx, env.Root, nil, "run", "./tools/lint/cmd/boblint",
		"-nopanic", "-noexit", "-nolist", "./..."); err != nil {
		return err
	}
	return checkNoNixFiles(env.Root)
}

func checkNoNixFiles(root string) error {
	var found []string
	for _, d := range noNixDirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.HasSuffix(path, ".nix") {
				rel, _ := filepath.Rel(root, path)
				found = append(found, rel)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return harnessErr(fmt.Errorf("walk %s: %w", d, err))
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("no .nix files may appear in user-facing trees (AGENTS.md §5.5):\n%s", strings.Join(found, "\n"))
	}
	return nil
}
