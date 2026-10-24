package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// The golden suite (HARNESS.md §3). With -update the suite writes candidates to
// out/golden-candidates/ instead of failing; tests/ is never touched.
func init() {
	register(Stage{Name: "golden", Order: 11, Run: runGoldens})
}

func runGoldens(ctx context.Context, env *Env) error {
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "./tests/golden/...")
	cmd.Dir = env.Root
	cmd.Env = os.Environ()
	// main.go owns the -update flag (HARNESS.md §1.2); looked up here so this file does not
	// depend on how main parses flags.
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		cmd.Env = append(cmd.Env,
			"BOB_GOLDEN_UPDATE=1",
			"BOB_GOLDEN_CANDIDATES="+filepath.Join(env.Root, "out", "golden-candidates"))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go test ./tests/golden/...: %w\n%s", err, out)
	}
	return nil
}
