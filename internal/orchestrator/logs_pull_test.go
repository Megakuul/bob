package orchestrator_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/serverenv/rustfs"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
)

// pullFollower records what one follower received and when.
type pullFollower struct {
	mu      sync.Mutex
	lines   []logstore.Line
	lat     [][2]time.Time // emit, arrival
	arrived []time.Time    // messages with lines or delayed
	last    int64
	errs    []string
}

func (f *pullFollower) emit(b logstore.Batch) error {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range b.Restarts {
		f.lines = slices.DeleteFunc(f.lines, func(l logstore.Line) bool { return l.Seq >= r.FromSeq })
		f.last = r.FromSeq - 1
	}
	for _, l := range b.Lines {
		if l.Seq != f.last+1 {
			f.errs = append(f.errs, "gap or duplicate at seq "+strconv.FormatInt(l.Seq, 10))
		}
		f.last = l.Seq
		f.lines = append(f.lines, l)
		if ns, err := strconv.ParseInt(strings.Fields(l.Text)[0], 10, 64); err == nil {
			f.lat = append(f.lat, [2]time.Time{time.Unix(0, ns), now})
		}
	}
	if len(b.Lines) > 0 || b.Delayed {
		f.arrived = append(f.arrived, now)
	}
	return nil
}

// TestLivePullEndToEnd runs the pull path in process with the real worker log (internal/worker)
// and the lake on RustFS: one job printing 200 lines/s, 10 followers on one orchestrator.
// Line → follower p99 ≤ 1 s (R-LOG-002); cutting the worker's Attach stream stalls the
// followers ≤ 3 s (degraded: delayed + lake); the final streams equal the lake (R-LOG-101).
func TestLivePullEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Stop() }()
	c, err := s3client.New(ctx, s3client.Options{Endpoint: fs.URL, Region: "us-east-1", AccessKey: rustfs.RootAccessKey, SecretKey: rustfs.RootSecretKey})
	if err != nil {
		t.Fatal(err)
	}
	reader := logstore.NewReader(c, "bob-analytics")

	// worker side
	wr := logstore.NewWriter(logstore.WriterConfig{Client: c, Bucket: "bob-analytics", FlushInterval: 2 * time.Second})
	wctx, stopWriter := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	go func() { writerDone <- wr.Run(wctx) }()
	wl := worker.NewLogs(wr, reader, "wA")
	const pkg = "//live:t"
	build := buildIDAt(time.Now())
	jl, err := wl.Start(ctx, build, "job1", 3, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	// orchestrator side
	var jmu sync.Mutex
	finished := false
	jobs := func(context.Context, string) ([]logstore.JobState, bool, error) {
		jmu.Lock()
		defer jmu.Unlock()
		return []logstore.JobState{{ID: "job1", Owner: "wA", Epoch: 3, Terminal: finished}}, finished, nil
	}
	live := orchestrator.NewLiveLogs("orch-0", nil, nil)
	src := logstore.NewSources(reader, live, jobs)

	// the Attach stream: orchestrator → worker messages through a channel, worker → orchestrator
	// chunks straight into LiveLogs.Chunk
	var amu sync.Mutex
	var detach func()
	var stopStream context.CancelFunc
	attach := func() {
		amu.Lock()
		defer amu.Unlock()
		down := make(chan *workerv1.AttachResponse, 64)
		ls := wl.Stream(func(ch *workerv1.LiveChunk) error {
			if err := live.Chunk("wA", ch); err != nil && !strings.Contains(err.Error(), "worker not attached") {
				t.Errorf("worker chunk rejected: %v", err) // "not attached": a chunk racing the cut
			}
			return nil
		})
		sctx, stop := context.WithCancel(ctx)
		stopStream = stop
		detach = live.Attached("wA", func(m *workerv1.AttachResponse) bool {
			select {
			case down <- m:
				return true
			default:
				return false
			}
		})
		go func() { _ = ls.Run(sctx) }()
		go func() {
			for {
				select {
				case <-sctx.Done():
					return
				case m := <-down:
					ls.Handle(m)
				}
			}
		}()
	}
	cut := func() {
		amu.Lock()
		defer amu.Unlock()
		detach()
		stopStream()
	}
	attach()

	// the build: 200 lines/s for 16 s
	produced := make(chan struct{})
	go func() {
		defer close(produced)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for range 160 {
			<-tick.C
			for range 20 {
				jl.Line(pkg, logstore.StreamStdout, strconv.FormatInt(time.Now().UnixNano(), 10)+" x")
			}
		}
	}()

	fols := make([]*pullFollower, 10)
	var fw sync.WaitGroup
	for i := range fols {
		fols[i] = &pullFollower{}
		fw.Add(1)
		go func() {
			defer fw.Done()
			if err := logstore.FollowPackage(ctx, src, build, pkg, nil, fols[i].emit); err != nil {
				t.Errorf("follower %d: %v", i, err)
			}
		}()
	}
	time.Sleep(6 * time.Second)
	cutAt := time.Now()
	cut()
	time.Sleep(5 * time.Second)
	cutEnd := time.Now()
	attach()
	<-produced
	if _, err := jl.End(ctx); err != nil {
		t.Fatal(err)
	}
	jmu.Lock()
	finished = true
	jmu.Unlock()
	fw.Wait()
	stopWriter()
	<-writerDone

	lake, err := reader.Package(ctx, build, pkg, 0, 0)
	if err != nil || len(lake) != 3200 {
		t.Fatalf("lake has %d lines (%v), want 3,200", len(lake), err)
	}
	var lat []time.Duration
	for i, f := range fols {
		for _, e := range f.errs {
			t.Errorf("follower %d: %s", i, e)
		}
		if len(f.lines) != len(lake) {
			t.Fatalf("follower %d has %d lines, the lake %d", i, len(f.lines), len(lake))
		}
		for k := range lake {
			if f.lines[k].Seq != lake[k].Seq || f.lines[k].Text != lake[k].Text {
				t.Fatalf("follower %d line %d differs from the lake", i, k)
			}
		}
		maxGap, prev := time.Duration(0), cutAt
		for _, at := range f.arrived {
			if at.Before(cutAt) {
				continue
			}
			if at.After(cutEnd) {
				break
			}
			maxGap = max(maxGap, at.Sub(prev))
			prev = at
		}
		maxGap = max(maxGap, cutEnd.Sub(prev))
		if maxGap > 3*time.Second {
			t.Errorf("follower %d stalled %v after the Attach stream was cut, want ≤ 3 s", i, maxGap)
		}
		// latency of lines emitted while the live path was up: not in the chunk the cut lost
		// (the last 500 ms before it), not during the cut, nor in the first second after it,
		// when the worker resends its ring
		for _, p := range f.lat {
			if p[0].Before(cutAt.Add(-500*time.Millisecond)) || p[0].After(cutEnd.Add(time.Second)) {
				lat = append(lat, p[1].Sub(p[0]))
			}
		}
	}
	slices.Sort(lat)
	p99 := lat[len(lat)*99/100]
	t.Logf("line → follower over %d deliveries: p50 %v, p99 %v [this host, RustFS local]", len(lat), lat[len(lat)/2], p99)
	if p99 > time.Second {
		t.Errorf("line → follower p99 = %v, want ≤ 1 s", p99)
	}
	st := live.Stats()
	t.Logf("live: %d chunks accepted, %d rejected, %d bytes", st.Accepted, st.Rejected, st.Bytes)
	if st.Rejected > 1 {
		t.Errorf("%d chunks of the honest worker rejected (one may race the cut)", st.Rejected)
	}
}

// buildIDAt is a build id (26-char lower-case Crockford) whose time is t.
func buildIDAt(t time.Time) string {
	const crockford = "0123456789abcdefghjkmnpqrstvwxyz"
	ms := uint64(t.UnixMilli())
	b := []byte("0000000000" + "a0b1c2d3e4f5g6h7")
	for i := 9; i >= 0; i-- {
		b[i] = crockford[ms&31]
		ms >>= 5
	}
	return string(b)
}
