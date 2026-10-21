package main

import (
	"context"
	"testing"
)

// With no benchmarks registered every row is "no benchmark yet", so both the
// gate stage and the slow bench stage pass, provided the budgets file is current.
func TestBudgetStagesPass(t *testing.T) {
	for _, slow := range []bool{false, true} {
		if err := runBudgets(context.Background(), &Env{Root: "../.."}, slow); err != nil {
			t.Fatalf("slow=%v: %v", slow, err)
		}
	}
}
