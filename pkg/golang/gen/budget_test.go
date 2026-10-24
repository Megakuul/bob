package gen

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPlanBudget1000 plans a binary over 1,000 workspace packages (SPEC §03.8 "graph →
// derivations, 1,000 Go packages ≤ 5 s"). It runs with BOB_GO_BUDGETS=1 and prints
// "BUDGET go-plan-1000 <s>".
func TestPlanBudget1000(t *testing.T) {
	if os.Getenv("BOB_GO_BUDGETS") == "" {
		t.Skip("set BOB_GO_BUDGETS=1")
	}
	root := t.TempDir()
	files := map[string]string{}
	const n, width = 1000, 50
	for i := 0; i < n; i++ {
		var imps strings.Builder
		if i >= width {
			base := (i/width - 1) * width
			fmt.Fprintf(&imps, "\t_ \"example.com/m/lib/p%04d\"\n\t_ \"example.com/m/lib/p%04d\"\n", base+(i*7)%width, base+(i*13+5)%width)
		}
		files[fmt.Sprintf("m/lib/p%04d/p.go", i)] = fmt.Sprintf("package p%04d\n\nimport (\n%s)\n\n// V is a value.\nfunc V() int { return %d }\n", i, imps.String(), i)
	}
	var main strings.Builder
	for i := n - width; i < n; i++ {
		fmt.Fprintf(&main, "\t_ \"example.com/m/lib/p%04d\"\n", i)
	}
	files["m/cmd/x/main.go"] = "package main\n\nimport (\n" + main.String() + ")\n\nfunc main() {}\n"
	writeTree(t, root, files)
	g := testGraph(t)
	g.Nodes = g.Nodes[:2] // module + binary
	start := time.Now()
	res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, g, &fakeStore{}))
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	t.Logf("BUDGET go-plan-1000 %.2f (%d derivations)", d.Seconds(), len(res.Drvs))
	if d > 5*time.Second {
		t.Errorf("planning 1,000 packages took %s, budget 5 s", d)
	}
}
