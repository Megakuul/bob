package logstore

import (
	"context"
	"testing"
	"time"
)

// TestPackageRuns: package_run rows come back per project, label and start range (A298).
func TestPackageRuns(t *testing.T) {
	c := startS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	b := idAt(now, "runs")
	w := NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: time.Hour})
	for _, r := range []RunInfo{
		{BuildID: b, Project: "acme", Package: "//a:a", Status: "ok", Start: now.Add(-2 * time.Second), End: now},
		{BuildID: b, Project: "acme", Package: "//b:b", Status: "cached", Start: now.Add(-time.Second), End: now},
		{BuildID: b, Project: "other", Package: "//a:a", Status: "ok", Start: now, End: now},
	} {
		if err := w.AddRun(NewPackageRun(r)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(ctx, b); err != nil {
		t.Fatal(err)
	}
	r := NewReader(c, analytics)
	got, err := r.PackageRuns(ctx, "acme", "", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || len(got) != 2 {
		t.Fatalf("acme runs = %d %v, want 2", len(got), err)
	}
	got, err = r.PackageRuns(ctx, "acme", "//a:a", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || len(got) != 1 || got[0].DurationMs.Data != 2000 {
		t.Fatalf("acme //a:a runs = %+v %v, want one of 2000 ms", got, err)
	}
	if got, err := r.PackageRuns(ctx, "acme", "", now.Add(time.Hour), now.Add(2*time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("runs after now = %d %v, want none", len(got), err)
	}
	if got, err := r.PackageRuns(ctx, "acme", "", now.AddDate(0, -3, 0), now.AddDate(0, -2, 0)); err != nil || len(got) != 0 {
		t.Fatalf("runs of an empty month = %d %v, want none", len(got), err)
	}
}
