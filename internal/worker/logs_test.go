package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/logstore"
)

const testBuild = "01k8a0000000000000000000aa"

func newTestLogs(now *time.Time) *Logs {
	l := NewLogs(logstore.NewWriter(logstore.WriterConfig{}), nil, "w1")
	l.Now = func() time.Time { return *now }
	return l
}

func sub(id, job string, exp time.Time, cur map[string]*workerv1.LiveCursor) *workerv1.AttachResponse {
	return &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveSubscribe{LiveSubscribe: &workerv1.LiveSubscribe{
		SubscriptionId: id, JobId: job, Cursors: cur, ExpiresUnixMs: exp.UnixMilli()}}}
}

// TestJobLogSeqAndRestart: seqs per package from 1, long lines split, a retried attempt
// continues after the previous attempt's last persisted seq with a LiveRestart (SPEC §05.10.2).
func TestJobLogSeqAndRestart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newTestLogs(&now)
	j, err := l.Start(context.Background(), testBuild, "job1", 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := j.Writer("//a:x", logstore.StreamStdout)
	_, _ = w.Write([]byte("one\ntwo\r\n" + strings.Repeat("x", 9000) + "\npartial"))
	_ = w.Close()
	if j.next["//a:x"] != 1+2+3+1 { // one, two, 3 pieces of 9000 bytes, partial
		t.Fatalf("next seq %d", j.next["//a:x"])
	}
	if len(j.restarts) != 0 {
		t.Fatalf("attempt 1 has restarts")
	}
	j2, err := l.Start(context.Background(), testBuild, "job1", 2, 2, map[string]int64{"//a:x": 5})
	if err != nil {
		t.Fatal(err)
	}
	j2.Line("//a:x", 1, "again")
	j2.Line("//b:y", 1, "new package")
	if j2.ring[0].Seq != 6 || j2.ring[0].Attempt != 2 || j2.ring[1].Seq != 1 {
		t.Fatalf("attempt 2 lines %+v", j2.ring)
	}
	want := []logstore.Restart{{Package: "//a:x", Attempt: 2, FromSeq: 6}, {Package: "//b:y", Attempt: 2, FromSeq: 1}}
	if len(j2.restarts) != 2 || j2.restarts[0] != want[0] || j2.restarts[1] != want[1] {
		t.Fatalf("restarts %+v, want %+v", j2.restarts, want)
	}
	if _, err := l.Start(context.Background(), "../etc", "x", 1, 1, nil); err == nil {
		t.Fatalf("invalid build id accepted")
	}
}

// TestLiveStream: no subscription, no data; a subscription gets the ring after its cursors,
// then new lines, then the end markers; the byte budget and the 5,000-line cap split big
// backlogs; expiry and LiveUnsubscribe stop the chunks; the ring keeps 20,000 lines at most.
func TestLiveStream(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newTestLogs(&now)
	j, _ := l.Start(context.Background(), testBuild, "job1", 7, 1, nil)
	for i := range 30 {
		j.Line("//a:x", 1, "line "+string(rune('a'+i%26)))
	}
	s := l.Stream(nil)
	if c := s.chunks(); len(c) != 0 {
		t.Fatalf("chunks without a subscription: %d", len(c))
	}
	if !s.Handle(sub("s1", "job1", now.Add(15*time.Second), map[string]*workerv1.LiveCursor{"//a:x": {Seq: 20, Attempt: 1}})) {
		t.Fatalf("LiveSubscribe not handled")
	}
	cs := s.chunks()
	if len(cs) != 1 || len(cs[0].Lines) != 10 || cs[0].Lines[0].Seq != 21 || cs[0].Epoch != 7 || cs[0].SubscriptionId != "s1" || cs[0].JobId != "job1" {
		t.Fatalf("first chunk %+v", cs)
	}
	j.Line("//a:x", 1, "more")
	j.EndPackage("//a:x", nil)
	cs = s.chunks()
	if len(cs) != 1 || len(cs[0].Lines) != 1 || cs[0].Lines[0].Seq != 31 || len(cs[0].Finals) != 1 || cs[0].Finals[0].LastSeq != 31 {
		t.Fatalf("second chunk %+v", cs)
	}
	if cs = s.chunks(); len(cs) != 0 {
		t.Fatalf("nothing new, but %d chunks", len(cs))
	}

	// a flood: 7,000 lines of 1 KiB = 7 MiB (the ring keeps 8 MiB); chunks stay ≤ 5,000 lines
	// and within the budget
	for range 7000 {
		j.Line("//big:x", 1, strings.Repeat("y", 1000))
	}
	total, chunks := 0, 0
	for range 40 {
		for _, c := range s.chunks() {
			if len(c.Lines) > logstore.MaxChunkLines {
				t.Fatalf("chunk of %d lines", len(c.Lines))
			}
			total += len(c.Lines)
			chunks++
		}
		now = now.Add(time.Second)
	}
	if total != 7000 {
		t.Fatalf("flood: %d of 7,000 lines sent", total)
	}
	if chunks < 3 {
		t.Fatalf("7 MiB in %d chunks: the burst / 1 MiB/s budget was not applied", chunks)
	}

	// expiry and unsubscribe
	s.Handle(sub("s2", "job1", now.Add(time.Second), nil))
	now = now.Add(2 * time.Second)
	j.Line("//a:x", 1, "late")
	for _, c := range s.chunks() {
		if c.SubscriptionId == "s2" {
			t.Fatalf("expired subscription still served")
		}
	}
	s.Handle(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveUnsubscribe{LiveUnsubscribe: &workerv1.LiveUnsubscribe{SubscriptionId: "s1"}}})
	j.Line("//a:x", 1, "after unsubscribe")
	if cs := s.chunks(); len(cs) != 0 {
		t.Fatalf("chunks after unsubscribe: %d", len(cs))
	}
	// unknown job: nothing
	s.Handle(sub("s3", "nope", now.Add(10*time.Second), nil))
	if cs := s.chunks(); len(cs) != 0 {
		t.Fatalf("chunks for a job this worker does not run")
	}
	for range 30000 {
		j.Line("//big:x", 1, "z")
	}
	if len(j.ring) != ringLines || j.ringSize > ringBytes {
		t.Fatalf("ring holds %d lines / %d bytes", len(j.ring), j.ringSize)
	}
}
