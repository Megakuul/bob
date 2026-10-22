package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() {
	register(Stage{Name: "builder", Order: 3, Run: runBuilder})
}

// builderTablePath is the {system → store path} table of bob-builder (R-NIX-015).
const builderTablePath = "pkg/engine/store/builders.go"

// runBuilder is R-NIX-015's CI check: genbuilders -check rebuilds bob-builder for both systems
// and fails when the committed table is stale or when the builder changed since the last
// commit (HEAD) without a bspec.BuilderVersion bump.
func runBuilder(ctx context.Context, env *Env) error {
	tmp, err := os.MkdirTemp("", "bob-ci-builder-")
	if err != nil {
		return harnessErr(err)
	}
	defer os.RemoveAll(tmp)
	base := filepath.Join(tmp, "builders.go") // stays missing when HEAD has no table
	// A missing HEAD, a missing file at HEAD or no repository at all mean there is no
	// previous builder to compare against (before the first commit).
	if exec.CommandContext(ctx, "git", "-C", env.Root, "cat-file", "-e", "HEAD:"+builderTablePath).Run() == nil {
		cmd := exec.CommandContext(ctx, "git", "-C", env.Root, "show", "HEAD:"+builderTablePath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return harnessErr(fmt.Errorf("git show HEAD:%s: %w: %s", builderTablePath, err, strings.TrimSpace(stderr.String())))
		}
		if err := os.WriteFile(base, out, 0o644); err != nil {
			return harnessErr(err)
		}
	}
	_, err = goCmd(ctx, env.Root, nil, "run", "./cmd/bob-builder/genbuilders", "-check", "-base", base)
	return err
}
