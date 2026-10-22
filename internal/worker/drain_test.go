package worker_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/store/env"
)

// TestDrain: a draining worker finishes the job it runs, claims nothing new (neither by poll
// nor by wake-up) and reports no free slot; undrained it picks the queued job up. The worker
// record's state is the truth: "draining" there drains, "active" undrains.
func TestDrain(t *testing.T) {
	be := env.Start(t)
	fleet := be.Open(t, "fleet")
	control := be.Open(t, "control")
	if fleet == nil || control == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	class := store.Class{Platform: "x86_64-linux"}
	cid, err := store.ClassID(class)
	if err != nil {
		t.Fatal(err)
	}
	opub, opriv, _ := ed25519.GenerateKey(rand.Reader)
	wpub, wpriv, _ := ed25519.GenerateKey(rand.Reader)
	q := &orchestrator.Queue{Fleet: fleet, Control: control, Class: cid, Shards: 16, Key: opriv, Kid: "k1",
		WorkerKey: func(string) ed25519.PublicKey { return wpub }}
	if err := store.Create(ctx, fleet, &store.WorkerRecord{Wid: store.ID("w1"), State: store.Set[string]("active"), SigPub: store.Set([]byte(wpub))}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var mu sync.Mutex
	started := map[string]bool{}
	w, err := worker.New(worker.Config{Fleet: fleet, ID: "w1", Profile: store.WorkerProfile{Platform: "x86_64-linux"},
		Classes: []store.Class{class}, Slots: 1, Keys: []ed25519.PublicKey{opub}, SigKey: wpriv,
		PollInterval: 200 * time.Millisecond, Heartbeat: time.Second, ConfigRefresh: time.Hour, RecordRefresh: 300 * time.Millisecond,
		LeaseTTL: time.Minute, Skew: time.Second, JobWatch: time.Second,
		Exec: func(ctx context.Context, c worker.Claim) worker.Outcome {
			mu.Lock()
			started[c.JobID] = true
			mu.Unlock()
			if c.JobID == "j1" {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return worker.Outcome{Status: "ok"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(wctx) }()
	defer func() { wcancel(); <-done }()
	state := func(id string) string {
		v, err := q.Job(ctx, id)
		if err != nil {
			return ""
		}
		return v.State
	}
	isStarted := func(id string) bool { mu.Lock(); defer mu.Unlock(); return started[id] }

	if _, err := q.Enqueue(ctx, orchestrator.Job{ID: "j1", Build: "b1"}, orchestrator.JobFirst); err != nil {
		t.Fatal(err)
	}
	if !env.Wait(30*time.Second, func() bool { return isStarted("j1") }) {
		t.Fatal("j1 was not picked up")
	}
	// Drain through the record (as FleetService.DrainWorker writes it).
	setState := func(st string) {
		t.Helper()
		if err := store.Change(ctx, fleet, &store.WorkerRecord{Wid: store.ID("w1")}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
			next := *cur
			next.State = store.Set(st)
			return &next, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setState("draining")
	if !env.Wait(10*time.Second, w.Draining) {
		t.Fatal("the worker did not see its draining record")
	}
	slot, err := q.Enqueue(ctx, orchestrator.Job{ID: "j2", Build: "b1"}, orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	close(release) // j1 ends while draining: it still finishes
	if !env.Wait(30*time.Second, func() bool { return state("j1") == "succeeded" }) {
		t.Fatalf("the running job did not finish while draining: %s", state("j1"))
	}
	w.Wake(cid, slot, "j2")
	time.Sleep(2 * time.Second) // several polls and the wake-up
	if isStarted("j2") || state("j2") != "queued" || w.FreeSlots() != 0 {
		t.Fatalf("draining worker: j2 started %v, state %s, free slots %d; want not claimed and 0 free", isStarted("j2"), state("j2"), w.FreeSlots())
	}
	setState("active")
	if !env.Wait(30*time.Second, func() bool { return state("j2") == "succeeded" }) {
		t.Fatalf("undrained worker did not run the queued job: %s", state("j2"))
	}
}
