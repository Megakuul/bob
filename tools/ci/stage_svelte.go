package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// nixpkgsPin is the nixpkgs revision the harness takes non-Go tools from
// (HARNESS.md §1.4, nixos-26.05).
const nixpkgsPin = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

// bannedWebDeps are class-composition helpers AGENTS.md §8 rules out.
var bannedWebDeps = []string{"clsx", "class-variance-authority", "cva", "tailwind-merge"}

func init() {
	register(Stage{Name: "web", Order: 9, Run: runWeb})
}

func runWeb(ctx context.Context, env *Env) error {
	dir := filepath.Join(env.Root, "web")
	if err := checkWebDeps(dir); err != nil {
		return err
	}
	node, err := nixTool(ctx, env.Root, "nodejs_22")
	if err != nil {
		return err
	}
	nodeBin := filepath.Join(node, "bin", "node")
	path := []string{"PATH=" + filepath.Join(node, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); os.IsNotExist(err) {
		// Lifecycle scripts stay off: the harness exception to D15 covers
		// installing from the committed lock file only (HARNESS.md §1.4).
		if _, err := command(ctx, dir, path, filepath.Join(node, "bin", "npm"), "ci", "--ignore-scripts"); err != nil {
			return harnessErr(err)
		}
	}
	steps := [][]string{
		{"node_modules/@sveltejs/kit/svelte-kit.js", "sync"},
		{"node_modules/svelte-check/bin/svelte-check", "--tsconfig", "./tsconfig.json", "--fail-on-warnings"},
	}
	if !env.Fast {
		steps = append(steps, []string{"node_modules/vite/bin/vite.js", "build"})
	}
	for _, s := range steps {
		if _, err := command(ctx, dir, path, nodeBin, s...); err != nil {
			return err
		}
	}
	return nil
}

// nixTool realises attr from the pinned nixpkgs and returns its store path.
func nixTool(ctx context.Context, root, attr string) (string, error) {
	out, err := command(ctx, root, nil, "nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths", "github:NixOS/nixpkgs/"+nixpkgsPin+"#"+attr)
	if err != nil {
		return "", harnessErr(err)
	}
	lines := strings.Fields(out)
	if len(lines) == 0 {
		return "", harnessErr(fmt.Errorf("nix build %s printed no store path", attr))
	}
	return lines[len(lines)-1], nil
}

func checkWebDeps(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return harnessErr(err)
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return fmt.Errorf("web/package.json: %w", err)
	}
	var bad []string
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name := range deps {
			if slices.Contains(bannedWebDeps, name) {
				bad = append(bad, name)
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("web: banned class-composition dependencies (AGENTS.md §8): %s", strings.Join(bad, ", "))
	}
	return nil
}
