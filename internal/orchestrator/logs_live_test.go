package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/logstore"
)

// attachSink is the orchestrator→worker side of one fake Attach stream.
type attachSink struct {
	mu   sync.Mutex
	msgs []*workerv1.AttachResponse
}

func (a *attachSink) send(m *workerv1.AttachResponse) bool {
	a.mu.Lock()
	a.msgs = append(a.msgs, m)
	a.mu.Unlock()
	return true
}

func (a *attachSink) subscribes() []*workerv1.LiveSubscribe {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*workerv1.LiveSubscribe
	for _, m := range a.msgs {
		if s := m.GetLiveSubscribe(); s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (a *attachSink) unsubscribes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, m := range a.msgs {
		if m.GetLiveUnsubscribe() != nil {
			n++
		}
	}
	return n
}

func chunkLines(pkg string, from, to int64, text string) []*workerv1.LiveLine {
	var out []*workerv1.LiveLine
	for s := from; s <= to; s++ {
		out = append(out, &workerv1.LiveLine{Package: pkg, Seq: s, Attempt: 1, Stream: 1, Text: fmt.Sprintf("%s %d", text, s)})
	}
	return out
}

func waitUntil(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLiveLogsHostileChunks is the in-process half of R-LOG-102 with the S17 security cases
// adapted to pull: the orchestrator subscribes only the job's owner, and accepts a LiveChunk only
// on that subscription, from that worker, with the job record's epoch, within the size and rate
// limits. Cross-job, foreign-subscription, stale-epoch, oversize, non-UTF-8 and flood chunks are
// all dropped and the follower's output is exactly the owner's valid lines. A requeue moves the
// subscription to the new owner; the follower leaving ends it.
func TestLiveLogsHostileChunks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A lake nobody answers: the live path alone feeds the follower here.
	dead := s3.New(s3.Options{BaseEndpoint: aws.String("http://127.0.0.1:1"), Region: "us-east-1",
		Credentials: aws.AnonymousCredentials{}, UsePathStyle: true, RetryMaxAttempts: 1})
	var jmu sync.Mutex
	owner, epoch, done := "wA", int64(3), false
	jobs := func(context.Context, string) ([]logstore.JobState, bool, error) {
		jmu.Lock()
		defer jmu.Unlock()
		return []logstore.JobState{{ID: "job1", Owner: owner, Epoch: epoch, Terminal: done}, {ID: "job2", Owner: "wB", Epoch: 7}}, false, nil
	}
	live := NewLiveLogs("orch-0", nil, nil)
	src := logstore.NewSources(logstore.NewReader(dead, "bob-analytics"), live, jobs)
	a, b := &attachSink{}, &attachSink{}
	live.Attached("wA", a.send)
	live.Attached("wB", b.send)

	const build = "01k8a0000000000000000000aa"
	var fmu sync.Mutex
	var got []logstore.Line
	fctx, stopFollow := context.WithCancel(ctx)
	followDone := make(chan error, 1)
	go func() {
		followDone <- logstore.FollowPackage(fctx, src, build, "//p:x", nil, func(bt logstore.Batch) error {
			fmu.Lock()
			got = append(got, bt.Lines...)
			fmu.Unlock()
			return nil
		})
	}()
	waitUntil(t, 5*time.Second, "subscription to the owner", func() bool { return len(a.subscribes()) > 0 })
	subA := a.subscribes()[0]
	if subA.GetJobId() != "job1" || subA.GetExpiresUnixMs() <= time.Now().UnixMilli() {
		t.Fatalf("subscription %+v", subA)
	}
	for _, s := range b.subscribes() {
		if s.GetJobId() == "job1" {
			t.Fatalf("worker B was subscribed to job1, which it does not hold")
		}
	}
	var subB string // B holds job2: it has a subscription of its own
	waitUntil(t, 5*time.Second, "subscription of job2", func() bool {
		for _, s := range b.subscribes() {
			subB = s.GetSubscriptionId()
		}
		return subB != ""
	})

	valid := &workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3, Lines: chunkLines("//p:x", 1, 10, "ok")}
	if err := live.Chunk("wA", valid); err != nil {
		t.Fatalf("valid chunk rejected: %v", err)
	}
	waitUntil(t, 5*time.Second, "valid lines delivered", func() bool {
		fmu.Lock()
		defer fmu.Unlock()
		return len(got) == 10
	})

	forged := func(name string, wid string, c *workerv1.LiveChunk) {
		t.Helper()
		err := live.Chunk(wid, c)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "invalid_argument: log line invalid: ") {
			t.Errorf("%s: error %q lacks the SPEC message", name, err)
		}
	}
	forgedLines := chunkLines("//p:x", 11, 20, "FORGED")
	forged("cross-job (B uses its own subscription for job1)", "wB",
		&workerv1.LiveChunk{SubscriptionId: subB, JobId: "job1", Epoch: 3, Lines: forgedLines})
	forged("foreign subscription (B replays A's id)", "wB",
		&workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3, Lines: forgedLines})
	forged("guessed subscription", "wA",
		&workerv1.LiveChunk{SubscriptionId: "orch-0.000000000000000000000000", JobId: "job1", Epoch: 3, Lines: forgedLines})
	forged("stale epoch", "wA",
		&workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 2, Lines: forgedLines})
	forged("future epoch", "wA",
		&workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 4, Lines: forgedLines})
	forged("5,001 lines", "wA",
		&workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3, Lines: chunkLines("//p:x", 11, 5011, "FORGED")})
	forged("line over 4 KiB", "wA", &workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3,
		Lines: []*workerv1.LiveLine{{Package: "//p:x", Seq: 11, Attempt: 1, Text: "FORGED" + strings.Repeat("A", 5000)}}})
	forged("not UTF-8", "wA", &workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3,
		Lines: []*workerv1.LiveLine{{Package: "//p:x", Seq: 11, Attempt: 1, Text: "FORGED \xff"}}})
	forged("seq 0", "wA", &workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3,
		Lines: []*workerv1.LiveLine{{Package: "//p:x", Seq: 0, Attempt: 1, Text: "FORGED"}}})
	// flood: 4 MiB burst, then 1 MiB/s; chunks of ~2 MiB back to back
	big := chunkLines("//p:x", 11, 11+500, "FORGED"+strings.Repeat("x", 4000))
	floodRejected := 0
	for range 6 {
		if err := live.Chunk("wA", &workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3, Lines: big}); err != nil {
			floodRejected++
		}
	}
	if floodRejected < 3 {
		t.Errorf("flood of 6 × 2 MiB within a moment: %d rejected, want ≥ 3 (1 MiB/s, 4 MiB burst)", floodRejected)
	}
	// The flood chunks that passed the rate limit are valid lines of the owner (seq 11..511):
	// they reach the follower. Forged text must never come from a rejected chunk: check that
	// no line of the forged non-flood chunks (text "FORGED <n>" without padding) arrived.
	time.Sleep(300 * time.Millisecond)
	fmu.Lock()
	for _, l := range got {
		if strings.HasPrefix(l.Text, "FORGED ") && len(l.Text) < 100 {
			t.Errorf("a rejected chunk reached the follower: %q", l.Text)
		}
	}
	fmu.Unlock()

	// requeue: job1 now belongs to B at epoch 4; A's subscription ends, B is subscribed
	jmu.Lock()
	owner, epoch = "wB", 4
	jmu.Unlock()
	waitUntil(t, 5*time.Second, "subscription moved to the new owner", func() bool {
		for _, s := range b.subscribes() {
			if s.GetJobId() == "job1" {
				return true
			}
		}
		return false
	})
	if a.unsubscribes() == 0 {
		t.Errorf("the old owner's subscription was not cancelled")
	}
	forged("old owner after requeue", "wA",
		&workerv1.LiveChunk{SubscriptionId: subA.GetSubscriptionId(), JobId: "job1", Epoch: 3, Lines: chunkLines("//p:x", 600, 601, "FORGED")})

	stopFollow()
	<-followDone
	if live.Stats().Subscriptions != 0 {
		t.Errorf("%d subscriptions left after the last follower left", live.Stats().Subscriptions)
	}
	if b.unsubscribes() == 0 {
		t.Errorf("no LiveUnsubscribe after the last follower left")
	}
}

// TestLiveLogsNotAttached: a job whose owner has no Attach stream here is not live: the feed
// marks the stream delayed (lake fallback) and nothing is sent anywhere.
func TestLiveLogsNotAttached(t *testing.T) {
	live := NewLiveLogs("orch-0", nil, nil)
	f := &logstore.Feed{}
	got := live.Subscribe(context.Background(), f, []logstore.JobState{{ID: "j", Owner: "wX", Epoch: 1}})
	if got["j"] {
		t.Fatalf("a job of a detached worker is reported live")
	}
	a := &attachSink{}
	detach := live.Attached("wX", a.send)
	if got := live.Subscribe(context.Background(), f, []logstore.JobState{{ID: "j", Owner: "wX", Epoch: 1}}); !got["j"] || len(a.subscribes()) != 1 {
		t.Fatalf("attached owner not subscribed: %v %d", got, len(a.subscribes()))
	}
	detach()
	if live.IsAttached("wX") {
		t.Fatalf("detach did not detach")
	}
	if got := live.Subscribe(context.Background(), f, []logstore.JobState{{ID: "j", Owner: "wX", Epoch: 1}}); got["j"] {
		t.Fatalf("detached owner still live")
	}
}
