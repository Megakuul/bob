package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/megakuul/bob/tools/internal/parity"
)

// gateH turns an unmapped non-manual requirement from a warning into a
// failure (HARNESS.md §11). Flip it when Gate H passes.
const gateH = false

func init() {
	register(Stage{Name: "parity", Order: 14, Run: runParity})
	setParityOut()
}

// setParityOut points the tests' parity.Covers calls at a fresh results
// file before any stage starts a test binary; children inherit the
// environment. `-only parity` keeps the file of the previous run so the
// catalogue can be regenerated without running the tests again.
func setParityOut() {
	if os.Getenv("BOB_PARITY_OUT") != "" {
		return
	}
	root, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(root, parity.SpecPath)); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return
		}
		root = parent
	}
	out := filepath.Join(root, parity.ResultsPath)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return
	}
	if i := slices.Index(os.Args, "-only"); i < 0 || i+1 >= len(os.Args) || os.Args[i+1] != "parity" {
		_ = os.Remove(out)
	}
	os.Setenv("BOB_PARITY_OUT", out)
}

func runParity(ctx context.Context, env *Env) error {
	cat, err := parity.Generate(ctx, env.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(env.Root, filepath.Dir(parity.DocPath)), 0o755); err != nil {
		return fmt.Errorf("parity: %w", err)
	}
	if err := os.WriteFile(filepath.Join(env.Root, parity.DocPath), []byte(cat.Markdown()), 0o644); err != nil {
		return fmt.Errorf("parity: %w", err)
	}
	var unmapped []string
	for _, r := range cat.Rows {
		if r.Status == parity.StatusUnmapped {
			unmapped = append(unmapped, r.ID)
		}
	}
	if len(cat.Unknown) > 0 {
		return fmt.Errorf("parity: tests cover ids not in SPEC.md: %s", strings.Join(cat.Unknown, ", "))
	}
	if len(unmapped) > 0 {
		msg := fmt.Sprintf("parity: %d requirements have no test: %s", len(unmapped), strings.Join(unmapped, ", "))
		if gateH {
			return fmt.Errorf("%s", msg)
		}
		fmt.Fprintln(os.Stderr, "warning:", msg)
	}
	passed := cat.Count(parity.StatusPass)
	raised, err := parity.CheckFloor(filepath.Join(env.Root, parity.FloorPath), passed)
	if err != nil {
		return err
	}
	fmt.Printf("parity: %d/%d requirements pass, %d fail, %d not run, %d unmapped, %d manual (%d signed)\n",
		passed, len(cat.Rows), cat.Count(parity.StatusFail), cat.Count(parity.StatusNotRun), len(unmapped),
		cat.Count(parity.StatusManual)+cat.Count(parity.StatusManualSigned), cat.Count(parity.StatusManualSigned))
	if raised {
		fmt.Printf("parity: floor raised to %d\n", passed)
	}
	return nil
}
