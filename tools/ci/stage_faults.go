package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The error-quality suite (HARNESS.md §5): every fault catalogue entry of tests/faults runs
// against bob. Entries whose feature is not implemented yet are red; they fail in the parity
// catalogue, not here. A crash, a raw Nix leak or a wrong answer fails the stage.
func init() {
	register(Stage{Name: "errors", Order: 12, Run: runFaults})
}

func runFaults(ctx context.Context, env *Env) error {
	dir := filepath.Join(env.Root, "out", "errors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("errors: %w", err)
	}
	summary := filepath.Join(dir, "summary.json")
	_ = os.Remove(summary)
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "./tests/errors/...")
	cmd.Dir = env.Root
	// -tags=bobtest: the bob the suite builds honours BOB_TEST_NIX_REPLAY (replay entries, A27).
	cmd.Env = append(os.Environ(), "BOB_ERRORS_SUMMARY="+summary,
		"GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -tags=bobtest"))
	// main.go owns -update and -slow (HARNESS.md §1.2); looked up here so this file does not
	// depend on how main parses flags.
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		cmd.Env = append(cmd.Env,
			"BOB_GOLDEN_UPDATE=1",
			"BOB_GOLDEN_CANDIDATES="+filepath.Join(env.Root, "out", "golden-candidates"))
	}
	if f := flag.Lookup("slow"); f != nil && f.Value.String() == "true" {
		cmd.Env = append(cmd.Env, "BOB_ERRORS_LIVE=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go test ./tests/errors/...: %w\n%s", err, out)
	}
	b, err := os.ReadFile(summary)
	if err != nil {
		return fmt.Errorf("errors: no summary: %w", err)
	}
	var results []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &results); err != nil {
		return fmt.Errorf("errors: summary: %w", err)
	}
	n := map[string]int{}
	for _, r := range results {
		n[r.Status]++
	}
	fmt.Printf("errors: %d fault entries: %d pass, %d red (not implemented), %d fail\n",
		len(results), n["pass"], n["red"], n["fail"])
	return nil
}
