package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/tools/internal/budgets"
)

// Both the gate stage and the slow bench stage pass when the budgets file is current and every
// measured row is within its limit. The registered benchmarks themselves are not run here: they
// take minutes and want a quiet host, and the budgets/bench stages run them. A stub answers each
// registered command with every one of its rows at exactly its limit, in a scratch root, so the
// repository's baseline file is not touched (correction 71, I-B38).
func TestBudgetStagesPass(t *testing.T) {
	f, err := budgets.Check("../..")
	if err != nil {
		t.Fatal(err)
	}
	profile := budgetProfile()
	stub := func(_ context.Context, _, command string) ([]byte, error) {
		var b strings.Builder
		for _, r := range f.Rows {
			if r.Bench != command {
				continue
			}
			if v, ok := r.Limit[profile]; ok {
				fmt.Fprintf(&b, "BUDGET %s %s\n", r.ID, strconv.FormatFloat(v, 'g', -1, 64))
			}
		}
		return []byte(b.String()), nil
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests", "budgets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, slow := range []bool{false, true} {
		if _, err := budgets.Run(context.Background(), f, budgets.Options{Root: root, Profile: profile, Slow: slow, Exec: stub}); err != nil {
			t.Fatalf("slow=%v: %v", slow, err)
		}
	}
}
