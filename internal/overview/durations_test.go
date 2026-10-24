package overview

import (
	"testing"
	"time"

	"github.com/megakuul/bob/internal/logstore"
)

// TestDurationRows: per label and day, only runs that built (A298).
func TestDurationRows(t *testing.T) {
	day := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	run := func(label, status string, start time.Time, ms int) logstore.PackageRun {
		return logstore.NewPackageRun(logstore.RunInfo{BuildID: "b-01M5N8WSK5HJ31JASXZEXY8FY4", Project: "p", Package: label,
			Status: status, Start: start, End: start.Add(time.Duration(ms) * time.Millisecond)})
	}
	rows := durationRows([]logstore.PackageRun{
		run("//a:a", "ok", day, 1000), run("//a:a", "failed", day.Add(time.Hour), 3000),
		run("//a:a", "cached", day, 1), run("//a:a", "ok", day.AddDate(0, 0, 1), 500), run("//b:b", "cancelled", day, 9),
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", rows)
	}
	if r := rows[0]; r.Label != "//a:a" || r.Day != "2026-10-23" || r.Count != 2 || r.AvgMs != 2000 {
		t.Errorf("row 0 = %v", r)
	}
	if r := rows[1]; r.Day != "2026-10-24" || r.Count != 1 || r.AvgMs != 500 {
		t.Errorf("row 1 = %v", r)
	}
}
