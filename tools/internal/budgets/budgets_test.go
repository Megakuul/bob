package budgets

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite tests/budgets/budgets.json from SPEC.md")

const root = "../../.."

func TestProposalRowsPresent(t *testing.T) {
	f, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) != 97 {
		t.Fatalf("got %d Appendix B rows, want 97", len(f.Rows))
	}
	for _, r := range f.Rows {
		if r.Status != "proposal" || r.Unit == "" || r.Metric == "" || r.Source == "" || len(r.Limit) == 0 ||
			(r.Suite != "fast" && r.Suite != "slow") {
			t.Errorf("incomplete row %+v", r)
		}
	}
}

func TestBudgetsFileUpToDate(t *testing.T) {
	if *update {
		if err := Write(root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Check(root); err != nil {
		t.Fatal(err)
	}
}

func TestParseRow(t *testing.T) {
	tests := []struct {
		line    string
		unit    string
		stat    string
		limit8c float64
		limit2c float64
	}{
		{"| §01 §1.17 (R-NFR-001/002) | `examples/polyglot` no-op `bob build` (all stages) | ≤ 0.6 s / ≤ 0.8 s | [8c/15G] / [2c/4G] | x |", "s", "median", 0.6, 0.8},
		{"| §01 §1.17 (R-NFR-001/002) | graph → drvs, N=1,000 new / unchanged | ≤ 3.5 s / ≤ 1.2 s / ≤ 3.5 s / ≤ 1.3 s | [8c/15G] / [2c/4G] | x |", "s", "median", 3.5, 3.5},
		{"| §02 | R-DEV-006 `bob dev -- true` | ≤ 1.0 s [2c/4G], ≤ 0.5 s [8c/15G] | [2c/4G] unless marked | x |", "s", "median", 0.5, 1.0},
		{"| §05 §05.12 | R-NFR-102 | Requests per job ≤ 26 (no LIST) | — | x |", "count", "value", 26, 26},
		{"| §06 | R-UI-007 first load | JS ≤ 250 KiB gz, CSS ≤ 30 KiB gz | [8c/15G] / [2c/4G] | x |", "KiB", "value", 250, 250},
		{"| §02 | R-DEV-007 hook fast path p95 | ≤ 10 ms / ≤ 20 ms | [8c/15G] / [2c/4G] | x |", "ms", "p95", 10, 20},
	}
	for _, tt := range tests {
		rows, err := Parse([]byte("\n# Appendix B\n" + tt.line + "\n"))
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: rows=%d err=%v", tt.line, len(rows), err)
		}
		r := rows[0]
		if r.Unit != tt.unit || r.Stat != tt.stat || r.Limit[Profile8c] != tt.limit8c || r.Limit[Profile2c] != tt.limit2c {
			t.Errorf("%s: got unit=%s stat=%s limit=%v", tt.line, r.Unit, r.Stat, r.Limit)
		}
	}
	if _, err := Parse([]byte("\n# Appendix B\n| §01 | x | fast | — | y |\n")); err == nil {
		t.Error("unparseable budget accepted")
	}
}

func TestRun(t *testing.T) {
	row := func(id, bench string) Row {
		return Row{ID: id, What: id, Unit: "s", Stat: "median", Suite: "fast", Bench: bench,
			Limit: map[string]float64{Profile8c: 1}}
	}
	f := &File{Tolerance: DefaultTolerance, Rows: []Row{
		row("B-001", ""),           // no benchmark yet
		row("B-002", "bench ok"),   // 1.05 s: within 10 % tolerance
		row("B-003", "bench slow"), // 1.2 s: over
		{ID: "B-004", Suite: "slow", Bench: "bench never", Limit: map[string]float64{Profile8c: 1}},
	}}
	exec := func(_ context.Context, _, cmd string) ([]byte, error) {
		if cmd == "bench never" {
			t.Error("slow row ran in a fast run")
		}
		return []byte("noise\nBUDGET B-002 1.05\nBUDGET B-003 1.2\n"), nil
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests/budgets"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), f, Options{Root: dir, Exec: exec})
	if err == nil || !strings.Contains(err.Error(), "B-003") || strings.Contains(err.Error(), "B-002") {
		t.Fatalf("err = %v, want only B-003", err)
	}
	want := []string{"no benchmark yet", "ok", "FAIL"}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d", len(res), len(want))
	}
	for i, w := range want {
		if res[i].Status() != w {
			t.Errorf("%s: status %q, want %q", res[i].Row.ID, res[i].Status(), w)
		}
	}
	// Only pending rows: the run passes.
	if _, err := Run(context.Background(), &File{Tolerance: DefaultTolerance, Rows: f.Rows[:1]},
		Options{Root: dir, Exec: exec}); err != nil {
		t.Fatalf("pending rows failed the run: %v", err)
	}
}
