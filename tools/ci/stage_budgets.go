package main

import (
	"context"
	"fmt"
	"os"

	"github.com/megakuul/bob/tools/internal/budgets"
)

// Stage 15 runs the fast budget rows; the slow stage "bench" runs every row
// (HARNESS.md §12). Both first check that the budgets file matches SPEC.md.
func init() {
	register(Stage{Name: "budgets", Order: 15, Run: func(ctx context.Context, env *Env) error {
		return runBudgets(ctx, env, false)
	}})
	register(Stage{Name: "bench", Order: 101, Slow: true, Run: func(ctx context.Context, env *Env) error {
		return runBudgets(ctx, env, true)
	}})
}

// budgetProfile is set by the -profile flag through BOB_CI_PROFILE, so this
// stage does not depend on how main.go parses flags.
func budgetProfile() string {
	if p := os.Getenv("BOB_CI_PROFILE"); p != "" {
		return p
	}
	return budgets.Profile8c
}

func runBudgets(ctx context.Context, env *Env, slow bool) error {
	f, err := budgets.Check(env.Root)
	if err != nil {
		return err
	}
	results, err := budgets.Run(ctx, f, budgets.Options{Root: env.Root, Profile: budgetProfile(), Slow: slow})
	var measured, pending int
	for _, r := range results {
		if r.Measured {
			measured++
		} else {
			pending++
		}
		fmt.Fprintf(os.Stderr, "budget %s %-5s %-16s %s\n", r.Row.ID, r.Row.Suite, r.Status(), r.Row.What)
	}
	fmt.Fprintf(os.Stderr, "budgets: %d measured, %d no benchmark yet\n", measured, pending)
	return err
}
