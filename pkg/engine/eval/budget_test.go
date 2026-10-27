package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestEvalBudgets measures the evaluation budgets of SPEC §1.17 on a synthetic 1,000-package
// wide bob.go graph and prints them in the budgets runner's format ("BUDGET <row> <seconds>"):
//
//	B-001 no change (eval key hit), B-002 one body change, B-003 cold (empty GOCACHE).
//
// It runs only with BOB_EVAL_BUDGETS=1 (minutes of work), e.g.
//
//	BOB_EVAL_BUDGETS=1 go test ./pkg/engine/eval -run TestEvalBudgets -v -timeout 30m
func TestEvalBudgets(t *testing.T) {
	if os.Getenv("BOB_EVAL_BUDGETS") != "1" {
		t.Skip("set BOB_EVAL_BUDGETS=1 to measure the evaluation budgets")
	}
	const n, runs = 1000, 5
	root := filepath.Join(t.TempDir(), "wide")
	writeWide(t, root, n)
	ctx := context.Background()
	eval := func() (*Result, time.Duration) {
		t.Helper()
		start := time.Now()
		res, err := Evaluate(ctx, Request{Root: root, Patterns: []string{"//..."}})
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		return res, d
	}
	// B-003: cold = no per-repository cache and no evaluator; the trusted seed (std and the
	// framework, built once per toolchain) exists, as on any machine that evaluated before.
	var cold []time.Duration
	for i := 0; i < runs; i++ {
		if err := os.RemoveAll(filepath.Join(root, genDir)); err != nil {
			t.Fatal(err)
		}
		res, d := eval()
		if res.Cached {
			t.Fatal("cold evaluation reported cached")
		}
		cold = append(cold, d)
	}
	var hit []time.Duration
	for i := 0; i < runs; i++ {
		res, d := eval()
		if !res.Cached {
			t.Fatal("unchanged repository was not cached")
		}
		hit = append(hit, d)
	}
	var change []time.Duration
	p := filepath.Join(root, "p0500", "bob.go")
	for i := 0; i < runs; i++ {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src = []byte(strings.Replace(string(src), fmt.Sprintf(`"v%d"`, i), fmt.Sprintf(`"v%d"`, i+1), 1))
		if err := os.WriteFile(p, src, 0o644); err != nil {
			t.Fatal(err)
		}
		res, d := eval()
		if res.Cached {
			t.Fatal("changed repository was cached")
		}
		change = append(change, d)
	}
	fmt.Printf("BUDGET B-001 %.3f\n", median(hit))
	fmt.Printf("BUDGET B-002 %.3f\n", median(change))
	fmt.Printf("BUDGET B-003 %.3f\n", median(cold))
	t.Logf("key hit %v, one change %v, cold %v", hit, change, cold)
}

func median(ds []time.Duration) float64 {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2].Seconds()
}

// writeWide writes a repository with n bob.go packages, each defining one step.
func writeWide(t *testing.T, root string, n int) {
	t.Helper()
	files := map[string]string{
		"go.mod":   "module example.com/wide\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   "//go:build bob\n\npackage wide\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})\n",
	}
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("p%04d/bob.go", i)] = fmt.Sprintf(`//go:build bob

package p%04d

import "github.com/megakuul/bob/pkg/bob"

// Step is //p%04d:s.
var Step = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "s", bob.RunOpts{Cmd: []string{"true", "v0"}, Outputs: []string{"o"}})
})
`, i, i)
	}
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
