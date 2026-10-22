package logstore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/lake"

	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// startS3 starts a fresh RustFS (the pinned binary, D63) with bob's buckets and returns a root
// client of it.
func startS3(t *testing.T) *s3.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Stop() })
	c, err := s3client.New(ctx, s3client.Options{Endpoint: fs.URL, Region: "us-east-1",
		AccessKey: rustfs.RootAccessKey, SecretKey: rustfs.RootSecretKey})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const analytics = "bob-analytics"

// TestWriteRead: lines of two builds and several packages go through the Writer (size trigger,
// explicit flush) and come back per package, per build in (package, seq) order, deduplicated
// after an ambiguous double commit, with the restart rule applied (R-LOG-001, R-LOG-003).
func TestWriteRead(t *testing.T) {
	c := startS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	b1, b2 := idAt(now, "one"), idAt(now, "two")
	w := NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: time.Hour, CommitLines: 50})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	add := func(build, pkg string, att int32, from, to int64) {
		for s := from; s <= to; s++ {
			if err := w.Add(build, "/nix/store/x.drv", Line{Package: pkg, Seq: s, Attempt: att, Stream: StreamStdout,
				TS: now.UnixNano(), Text: fmt.Sprintf("%s %s a%d s%d", build[20:], pkg, att, s)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(b1, "//a:x", 1, 1, 120) // 120 lines: the size trigger (50) commits without a flush
	add(b1, "//b:y", 1, 1, 30)
	add(b2, "//a:x", 1, 1, 10)
	waitFor(t, 30*time.Second, func() bool { return w.Stats().Commits > 0 })
	if err := w.Flush(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx, b2); err != nil {
		t.Fatal(err)
	}
	// ambiguous commit: the same batch lands twice
	add(b1, "//b:y", 1, 31, 40)
	if err := w.Flush(ctx, b1); err != nil {
		t.Fatal(err)
	}
	add(b1, "//b:y", 1, 31, 40)
	// attempt 2 of //b:y restarts at 36 (attempt 1's 36..40 are void)
	add(b1, "//b:y", 2, 36, 45)
	if err := w.Flush(ctx, b1); err != nil {
		t.Fatal(err)
	}
	stop()
	<-done

	r := NewReader(c, analytics)
	ls, err := r.Package(ctx, b1, "//a:x", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkSeq(t, "b1 //a:x", ls, 1, 120, "//a:x")
	ls, err = r.Package(ctx, b1, "//b:y", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkSeq(t, "b1 //b:y", ls, 1, 45, "//b:y")
	for _, l := range ls {
		if want := int32(1); l.Seq >= 36 && l.Attempt != 2 || l.Seq < 36 && l.Attempt != want {
			t.Fatalf("restart rule: seq %d attempt %d", l.Seq, l.Attempt)
		}
	}
	if ls, err = r.Package(ctx, b1, "//a:x", 100, 5); err != nil || len(ls) != 5 || ls[0].Seq != 101 {
		t.Fatalf("after 100 limit 5: %d lines, first %+v, %v", len(ls), first(ls), err)
	}
	all, err := r.Build(ctx, b1, 0)
	if err != nil || len(all) != 165 || all[0].Package != "//a:x" || all[120].Package != "//b:y" {
		t.Fatalf("Build(b1) = %d lines (%v), want 165 ordered by package", len(all), err)
	}
	if ls, err := r.Build(ctx, b2, 0); err != nil || len(ls) != 10 {
		t.Fatalf("Build(b2) = %d, %v: the other build's lines leak", len(ls), err)
	}
	hits, _, err := r.Search(ctx, b1, "a2 s4", "")
	if err != nil || len(hits) != 6 { // a2 s40..s45 of //b:y; a2 s4 does not exist
		t.Fatalf("Search = %d hits, %v", len(hits), err)
	}
	if _, _, err := r.Search(ctx, b1, "ab", ""); err == nil {
		t.Fatalf("2-byte search term accepted")
	}
	last, err := r.LastSeqs(ctx, b1)
	if err != nil || last["//a:x"].Seq != 120 || last["//b:y"] != (Cursor{Seq: 45, Attempt: 2}) {
		t.Fatalf("LastSeqs = %+v, %v", last, err)
	}
	if ls, err := r.Build(ctx, idAt(now.Add(-72*time.Hour), "none"), 0); err != nil || len(ls) != 0 {
		t.Fatalf("unknown build: %d lines, %v", len(ls), err)
	}
	if p := w.Persisted(b1); p["//a:x"] != 120 || p["//b:y"] != 45 {
		t.Fatalf("Persisted = %+v", p)
	}
}

func first(ls []Line) Line {
	if len(ls) == 0 {
		return Line{}
	}
	return ls[0]
}

func checkSeq(t *testing.T, what string, ls []Line, from, to int64, pkg string) {
	t.Helper()
	if int64(len(ls)) != to-from+1 {
		t.Fatalf("%s: %d lines, want %d", what, len(ls), to-from+1)
	}
	for i, l := range ls {
		if l.Seq != from+int64(i) || l.Package != pkg {
			t.Fatalf("%s: line %d is %s/%d", what, i, l.Package, l.Seq)
		}
	}
}

func waitFor(t *testing.T, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", d)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestCompactionWorkarounds is the R-LOG-100 acceptance: two maintenance passes compact the
// same partition at once (L3) while readers query it (L4): no duplicates and no failed query.
func TestCompactionWorkarounds(t *testing.T) {
	c := startS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	at := time.Now().UTC().Add(-time.Hour)
	build := idAt(at, "compact")
	w := NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: time.Hour})
	const commits, per = 30, 100
	for i := range commits {
		for s := 1; s <= per; s++ {
			seq := int64(i*per + s)
			if err := w.Add(build, "", Line{Package: "//c:x", Seq: seq, Attempt: 1, Text: fmt.Sprintf("line %d", seq)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Flush(ctx, build); err != nil {
			t.Fatal(err)
		}
	}
	part, _ := LogPartition(build, DefaultLogShards)

	r := NewReader(c, analytics)
	var stopRead atomic.Bool
	var readErrs, reads atomic.Int64
	var rw sync.WaitGroup
	for range 3 {
		rw.Add(1)
		go func() {
			defer rw.Done()
			for !stopRead.Load() {
				ls, err := r.Package(ctx, build, "//c:x", 0, 0)
				reads.Add(1)
				if err != nil || len(ls) != commits*per {
					readErrs.Add(1)
					t.Errorf("read during compaction: %d lines, %v", len(ls), err)
				}
			}
		}()
	}
	var cw sync.WaitGroup
	results := make([][]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		cw.Add(1)
		go func() {
			defer cw.Done()
			m := NewMaintenance(c, analytics, nil)
			results[i], errs[i] = m.Pass(ctx)
		}()
	}
	cw.Wait()
	time.Sleep(500 * time.Millisecond)
	stopRead.Store(true)
	rw.Wait()
	compacted := 0
	for i := range 2 {
		for _, p := range results[i] {
			if p == part {
				compacted++
			}
		}
		t.Logf("pass %d: compacted %v, err %v", i, results[i], errs[i])
	}
	if compacted == 0 {
		t.Fatalf("neither maintenance pass compacted %s", part)
	}
	// raw rows (no dedupe) must hold every line exactly once
	b, err := openPartition(ctx, r.client, part)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := scanAll(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != commits*per {
		t.Fatalf("after two concurrent compactions the partition holds %d rows, want %d (duplicates or loss)", len(rows), commits*per)
	}
	t.Logf("%d reads during compaction, %d failed", reads.Load(), readErrs.Load())
}

// TestWriterBudget is the R-LOG-001 load: W workers × 200 lines/s, each with its own Writer, for
// a while; afterwards every line committed before each writer's last commit is readable exactly
// once. BOB_LOG_LOAD=full runs the SPEC size (50 workers, 5 min, flush 30 s).
func TestWriterBudget(t *testing.T) {
	c := startS3(t)
	workers, dur, flush := 50, 40*time.Second, 5*time.Second
	if os.Getenv("BOB_LOG_LOAD") == "full" {
		dur, flush = 5*time.Minute, 30*time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), dur+3*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	builds := make([]string, workers)
	ws := make([]*Writer, workers)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := range workers {
		builds[i] = idAt(now, fmt.Sprintf("w%03d", i))
		ws[i] = NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: flush, ActiveWorkers: func() int { return workers }})
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- ws[i].Run(runCtx) }()
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			seq := int64(0)
			for time.Since(t0) < dur {
				<-tick.C
				for range 20 {
					seq++
					_ = ws[i].Add(builds[i], "", Line{Package: "//load:t", Seq: seq, Attempt: 1, TS: time.Now().UnixNano(),
						Text: fmt.Sprintf("%d worker %d line %d", time.Now().UnixNano(), i, seq)})
				}
			}
			if err := ws[i].Flush(ctx, builds[i]); err != nil {
				t.Errorf("worker %d: final flush: %v", i, err)
			}
			stop()
			<-done
		}()
	}
	wg.Wait()
	elapsed := time.Since(t0)
	r := NewReader(c, analytics)
	var commits, failures int64
	for i := range workers {
		s := ws[i].Stats()
		commits += s.Commits
		failures += s.Failures
		if s.Dropped != 0 {
			t.Errorf("worker %d dropped %d lines", i, s.Dropped)
		}
		last := ws[i].Persisted(builds[i])["//load:t"]
		ls, err := r.Package(ctx, builds[i], "//load:t", 0, MaxLimit)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(ls)) != last {
			t.Errorf("worker %d: %d lines readable, last committed seq %d", i, len(ls), last)
			continue
		}
		for k, l := range ls {
			if l.Seq != int64(k+1) {
				t.Fatalf("worker %d: line %d has seq %d (lost or duplicated)", i, k, l.Seq)
			}
		}
	}
	rate := float64(commits) / elapsed.Seconds()
	t.Logf("%d workers × 200 lines/s for %v [this host]: %d commits (%.1f/s over %d partitions), %d failed attempts (retried)",
		workers, dur, commits, rate, DefaultLogShards, failures)
	if rate > partitionBudget*DefaultLogShards {
		t.Errorf("commit rate %.1f/s exceeds the budget of %d/s per partition", rate, partitionBudget)
	}
}

// scanAll returns every raw row of the log table (no dedupe, no restart rule).
func scanAll(ctx context.Context, b *lake.Bucket) ([]LogLine, error) {
	return lake.Query[LogLine]().Scan(ctx, b)
}

// TestGetLogBudget measures the failing-package view of R-LOG-003 on a 50-package build whose
// lines are still uncommitted-to-compaction (one shard per package commit, the worst case for a
// running build): GetLog(build, package) p99 ≤ 500 ms, and only that package's lines.
func TestGetLogBudget(t *testing.T) {
	c := startS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := idAt(time.Now().UTC(), "budget")
	w := NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: time.Hour})
	for p := range 50 {
		pkg := fmt.Sprintf("//a%d:t", p)
		for s := 1; s <= 20; s++ {
			if err := w.Add(build, "", Line{Package: pkg, Seq: int64(s), Attempt: 1, Text: fmt.Sprintf("pkg%d line %d", p, s)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Flush(ctx, build); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(c, analytics)
	var lat []time.Duration
	for range 100 {
		t0 := time.Now()
		ls, err := r.Package(ctx, build, "//a7:t", 0, 0)
		lat = append(lat, time.Since(t0))
		if err != nil || len(ls) != 20 || ls[0].Text != "pkg7 line 1" {
			t.Fatalf("GetLog(//a7:t) = %d lines, %v", len(ls), err)
		}
	}
	slices.Sort(lat)
	p99 := lat[98]
	t.Logf("GetLog(one package of 50, 50 shards) p50 %v p99 %v [this host, RustFS local]", lat[49], p99)
	if p99 > 500*time.Millisecond {
		t.Errorf("GetLog p99 = %v, want ≤ 500 ms", p99)
	}
}
