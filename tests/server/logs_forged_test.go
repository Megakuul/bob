package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
	"github.com/megakuul/bob/tests/serverenv/workerclient"
)

// forgedLines are lines a hostile worker claims for package pkg: text "FORGED ..." that no
// follower may ever see.
func forgedLines(pkg string, from, n int, pad int) []*workerv1.LiveLine {
	out := make([]*workerv1.LiveLine, 0, n)
	for i := range n {
		out = append(out, &workerv1.LiveLine{Package: pkg, Seq: int64(from + i), Attempt: 1, Stream: 1,
			TsUnixNano: time.Now().UnixNano(), Text: "FORGED " + strings.Repeat("x", pad)})
	}
	return out
}

// TestRLOG102ForgedChunks is the hostile-worker half of R-LOG-102 (Gate H ruling G20): a second,
// properly registered worker (bwt1 token + PoP, tests/serverenv/workerclient) attaches to the
// follower's orchestrator and sends forged LiveChunks for a job that another worker holds:
// cross-job with a guessed subscription, the job's current epoch, a stale and a future epoch,
// an oversize chunk (5,001 lines), a line over 4 KiB, and a flood above 1 MiB/s. All are
// rejected: the follower's output is unchanged (gap-free, equal to the lake, no forged text),
// the hostile worker is never subscribed to the job, and the orchestrator keeps running.
func TestRLOG102ForgedChunks(t *testing.T) {
	parity.Covers(t, "R-LOG-102")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 1, Workers: 1, Env: fastFleet(nil)})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const label = "//victim:t"
	repo := srvtest.Repo(t, []srvtest.Pkg{{Name: "victim", Cmd: stamped(20)}})
	id, err := e.Submit(repo, label)
	if err != nil {
		t.Fatal(err)
	}
	st := srvtest.NewStream()
	fdone := make(chan error, 1)
	go func() { fdone <- drain(ctx, e.Via(0).Follow(id, label), st, nil) }()

	// the victim job: running, owner and epoch from its record
	info := e.Stack.Info
	t.Setenv("AWS_ACCESS_KEY_ID", info.WorkerAccess)
	t.Setenv("AWS_SECRET_ACCESS_KEY", info.WorkerSecret)
	fleet, err := store.Open(ctx, store.BucketConfig{Endpoint: info.S3ProxyURL, Region: info.Region, Name: "bob-fleet", Role: "fleet"})
	if err != nil {
		t.Fatal(err)
	}
	var victim *store.JobRecord
	if why := srvtest.Eventually(90*time.Second, func() string {
		js, err := e.Jobs(ctx, id)
		if err != nil {
			return err.Error()
		}
		for _, j := range js {
			if j.State == "JOB_STATE_RUNNING" && j.Kind != "JOB_KIND_EVAL" {
				if victim, err = store.Get(ctx, fleet, &store.JobRecord{ID: j.ID}); err != nil {
					return err.Error()
				}
				return ""
			}
		}
		return "no running build job"
	}); why != "" {
		t.Fatalf("the victim build did not start: %s", why)
	}

	// the hostile worker
	hostile, err := workerclient.Register(ctx, fleet, store.WorkerProfile{Platform: "aarch64-linux"})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.AttachFronts) == 0 {
		t.Fatal("harness error: devstack has no attach fronts")
	}
	s, err := hostile.Attach(ctx, info.AttachFronts[0], &workerv1.Hello{Platform: "aarch64-linux", SlotsTotal: 1})
	if err != nil {
		t.Fatalf("hostile worker attach: %v", err)
	}
	defer s.Close()
	sub := "orchestrator-0.000000000000000000000000"
	chunk := func(epoch int64, lines []*workerv1.LiveLine) *workerv1.LiveChunk {
		return &workerv1.LiveChunk{SubscriptionId: sub, JobId: victim.ID, Epoch: epoch, Lines: lines}
	}
	cases := []struct {
		name string
		c    *workerv1.LiveChunk
	}{
		{"cross-job, current epoch", chunk(victim.Epoch, forgedLines(label, 1, 50, 0))},
		{"stale epoch", chunk(victim.Epoch-1, forgedLines(label, 1, 50, 0))},
		{"future epoch", chunk(victim.Epoch+1, forgedLines(label, 1, 50, 0))},
		{"5,001 lines", chunk(victim.Epoch, forgedLines(label, 1, 5001, 0))},
		{"line over 4 KiB", chunk(victim.Epoch, forgedLines(label, 1, 1, 5000))},
		{"restart forging attempt 2", &workerv1.LiveChunk{SubscriptionId: sub, JobId: victim.ID, Epoch: victim.Epoch,
			Restarts: []*workerv1.LiveRestart{{Package: label, Attempt: 2, FromSeq: 1}}}},
	}
	for _, c := range cases {
		if err := s.SendChunk(c.c); err != nil {
			t.Logf("%s: the orchestrator closed the hostile stream: %v", c.name, err)
			break
		}
	}
	flood := chunk(victim.Epoch, forgedLines(label, 1, 900, 4000)) // ~3.6 MiB each
	for range 10 {
		if err := s.SendChunk(flood); err != nil {
			break
		}
	}
	// the hostile worker must never be subscribed to the victim's job
	deadline := time.After(5 * time.Second)
watch:
	for {
		select {
		case m, ok := <-s.In:
			if !ok {
				break watch
			}
			if ls := m.GetLiveSubscribe(); ls != nil && ls.GetJobId() == victim.ID {
				t.Errorf("the hostile worker was subscribed to job %s, which %s holds", victim.ID, victim.Owner)
			}
		case <-deadline:
			break watch
		}
	}

	if err := <-fdone; err != nil {
		t.Fatalf("follower of the victim build: %v", err)
	}
	for _, msg := range st.Errs {
		t.Errorf("follower: %s", msg)
	}
	got := st.Lines[label]
	for _, l := range got {
		if strings.HasPrefix(l.Text, "FORGED") {
			t.Fatalf("a forged line reached the follower: seq %d", l.Seq)
		}
	}
	if state, err := e.WaitBuild(id, 2*time.Minute); err != nil || state != "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("victim build: %q %v", state, err)
	}
	lake, err := e.Log(ctx, id, label, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lake) || len(lake) != 200*20 {
		t.Fatalf("follower has %d lines, the lake %d, the build printed %d", len(got), len(lake), 200*20)
	}
	for i := range lake {
		if got[i].Seq != lake[i].Seq || got[i].Text != lake[i].Text {
			t.Fatalf("follower line %d is %d %q, the lake has %d %q", i, got[i].Seq, got[i].Text, lake[i].Seq, lake[i].Text)
		}
	}
	for _, p := range info.Orchestrators {
		b, _ := os.ReadFile(p.Log)
		if strings.Contains(string(b), "panic:") {
			t.Errorf("%s panicked:\n%.2000s", p.Name, b)
		}
		if !strings.Contains(string(b), "invalid_argument: log line invalid: "+label+"/") {
			t.Errorf("%s did not log the rejected chunks as %q", p.Name, fmt.Sprintf("invalid_argument: log line invalid: %s/<seq>", label))
		}
	}
	if orchs, err := e.Orchestrators(ctx); err != nil || len(orchs) != 1 {
		t.Errorf("orchestrators after the forged chunks: %v, %v", orchs, err)
	}
	e.AssertNoList()
}
