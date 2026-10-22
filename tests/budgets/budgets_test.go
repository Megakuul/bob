// Package budgets checks that the budgets file carries the rows of the
// requirements whose whole acceptance is a budget; the measuring itself is the
// budgets stage (tools/ci) and the slow benchmark command.
package budgets

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/megakuul/bob/tests/parity"
)

type file struct {
	Rows []struct {
		ID  string `json:"id"`
		Req string `json:"req"`
	} `json:"rows"`
}

func rows(t *testing.T, req string) []string {
	t.Helper()
	b, err := os.ReadFile("budgets.json")
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("harness error: budgets.json: %v", err)
	}
	var ids []string
	for _, r := range f.Rows {
		if r.Req == req {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

func TestBudgetRows(t *testing.T) {
	parity.Covers(t, "R-NFR-150", "R-OUT-024", "R-OUT-025", "R-PLAT-008")
	want := map[string]int{"R-NFR-150": 10, "R-OUT-024": 1, "R-OUT-025": 1, "R-PLAT-008": 1}
	for req, n := range want {
		if got := rows(t, req); len(got) != n {
			t.Errorf("%s: budgets.json has %d rows %v, want %d", req, len(got), got, n)
		}
	}
}
