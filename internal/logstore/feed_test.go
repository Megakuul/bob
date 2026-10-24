package logstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeLive is a live path whose worker is the test: it records the feed it should fill.
type fakeLive struct {
	mu       sync.Mutex
	feed     *Feed
	attached bool
	subs     int
	unsubbed bool
}

func (l *fakeLive) Subscribe(_ context.Context, f *Feed, running []JobState) map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.feed = f
	l.subs++
	out := map[string]bool{}
	for _, j := range running {
		out[j.ID] = l.attached
	}
	return out
}

func (l *fakeLive) Unsubscribe(*Feed) {
	l.mu.Lock()
	l.unsubbed = true
	l.mu.Unlock()
}

func (l *fakeLive) set(attached bool) {
	l.mu.Lock()
	l.attached = attached
	l.mu.Unlock()
}

func (l *fakeLive) push(t *testing.T, lines []Line, rs []Restart) {
	t.Helper()
	l.mu.Lock()
	f := l.feed
	l.mu.Unlock()
	if f == nil {
		t.Fatal("no subscription yet")
	}
	f.AddLive(lines, rs)
}

func lines(pkg string, att int32, from, to int64) []Line {
	var out []Line
	for s := from; s <= to; s++ {
		out = append(out, Line{Package: pkg, Seq: s, Attempt: att, Stream: StreamStdout, Text: fmt.Sprintf("a%d %d", att, s)})
	}
	return out
}

// follower collects what Follow emits.
type follower struct {
	mu      sync.Mutex
	got     []Line
	delayed int
	done    bool
	end     []Final
	errs    []string
	last    map[string]int64
	msgs    int
}

func (f *follower) emit(b Batch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs++
	for _, r := range b.Restarts {
		keep := f.got[:0]
		for _, l := range f.got {
			if l.Package != r.Package || l.Seq < r.FromSeq {
				keep = append(keep, l)
			}
		}
		f.got = keep
		f.last[r.Package] = r.FromSeq - 1
	}
	for _, l := range b.Lines {
		if l.Seq != f.last[l.Package]+1 {
			f.errs = append(f.errs, fmt.Sprintf("%s: got seq %d after %d", l.Package, l.Seq, f.last[l.Package]))
		}
		f.last[l.Package] = l.Seq
		f.got = append(f.got, l)
	}
	if b.Delayed {
		f.delayed++
	}
	if b.Done {
		f.done, f.end = true, b.End
	}
	return nil
}

func (f *follower) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// TestFollowMerge runs one follower through the S17 cases with pull: history from the lake plus
// live lines, the worker detached (degraded: lake every 5 s, delayed), a worker crash with a
// retry that restarts at the last persisted seq, and the end of the build. The final stream
// equals the lake content, with no gap and no duplicate (R-LOG-101).
func TestFollowMerge(t *testing.T) {
	c := startS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := idAt(time.Now().UTC(), "follow")
	const pkg = "//f:x"
	w := NewWriter(WriterConfig{Client: c, Bucket: analytics, FlushInterval: time.Hour})
	persist := func(ls []Line) {
		if err := w.Add(build, "", ls...); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(ctx, build); err != nil {
			t.Fatal(err)
		}
	}
	persist(lines(pkg, 1, 1, 50))

	live := &fakeLive{attached: true}
	var jmu sync.Mutex
	finished := false
	jobs := func(context.Context, string) ([]JobState, bool, error) {
		jmu.Lock()
		defer jmu.Unlock()
		return []JobState{{ID: "j1", Owner: "w1", Epoch: 1, Terminal: finished}}, finished, nil
	}
	src := NewSources(NewReader(c, analytics), live, jobs)
	fl := &follower{last: map[string]int64{}}
	done := make(chan error, 1)
	go func() { done <- FollowPackage(ctx, src, build, pkg, nil, fl.emit) }()

	waitFor(t, 10*time.Second, func() bool { return fl.count() == 50 }) // history from the lake
	live.push(t, lines(pkg, 1, 45, 80), nil)                            // the worker's ring overlaps the lake
	waitFor(t, 5*time.Second, func() bool { return fl.count() == 80 })

	// the worker detaches: degraded, the lake is read every 5 s and the stream says delayed
	live.set(false)
	persist(lines(pkg, 1, 51, 100))
	waitFor(t, 15*time.Second, func() bool { return fl.count() == 100 })
	fl.mu.Lock()
	delayed := fl.delayed
	fl.mu.Unlock()
	if delayed == 0 {
		t.Fatalf("no delayed message while the worker was not attached")
	}

	// the worker crashed after it committed 1..90; attempt 2 continues at 91
	live.set(true)
	live.push(t, lines(pkg, 2, 91, 95), []Restart{{Package: pkg, Attempt: 2, FromSeq: 91}})
	persist(lines(pkg, 2, 91, 95))
	waitFor(t, 10*time.Second, func() bool {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		return len(fl.got) == 95 && fl.got[94].Attempt == 2
	})
	jmu.Lock()
	finished = true
	jmu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("follower not done 15 s after the build ended")
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	for _, e := range fl.errs {
		t.Error(e)
	}
	if !fl.done || len(fl.end) != 1 || fl.end[0] != (Final{Package: pkg, LastSeq: 95, Attempt: 2}) {
		t.Fatalf("end markers %+v (done %v)", fl.end, fl.done)
	}
	lake, err := src.Lake.Package(ctx, build, pkg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lake) != len(fl.got) {
		t.Fatalf("follower has %d lines, the lake %d", len(fl.got), len(lake))
	}
	for i := range lake {
		if lake[i].Seq != fl.got[i].Seq || lake[i].Attempt != fl.got[i].Attempt || lake[i].Text != fl.got[i].Text {
			t.Fatalf("line %d: follower %+v, lake %+v", i, fl.got[i], lake[i])
		}
	}
	if src.Feeds() != 0 || !live.unsubbed {
		t.Fatalf("feed not released after the last follower left (feeds %d, unsubscribed %v)", src.Feeds(), live.unsubbed)
	}
}
