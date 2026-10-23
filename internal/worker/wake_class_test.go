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

// TestWakeForNewClass: a wake-up for a class the worker does not read yet (the orchestrator
// published it in fleet/cfg with its first job) is claimed at once when the profile satisfies
// the class, and ignored when it does not. Polls are off (1 h), so only the wake-up can claim.
func TestWakeForNewClass(t *testing.T) {
	be := env.Start(t)
	fleet := be.Open(t, "fleet")
	control := be.Open(t, "control")
	if fleet == nil || control == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	mine, other := store.Class{Platform: "aarch64-linux"}, store.Class{Platform: "x86_64-linux"}
	mineID, err := store.ClassID(mine)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := store.ClassID(other)
	if err != nil {
		t.Fatal(err)
	}
	opub, opriv, _ := ed25519.GenerateKey(rand.Reader)
	wpub, wpriv, _ := ed25519.GenerateKey(rand.Reader)
	queue := func(id string) *orchestrator.Queue {
		return &orchestrator.Queue{Fleet: fleet, Control: control, Class: id, Shards: 16, Key: opriv, Kid: "k1",
			WorkerKey: func(string) ed25519.PublicKey { return wpub }}
	}
	if err := store.Create(ctx, fleet, &store.WorkerRecord{Wid: store.ID("w1"), State: store.Set[string]("active"), SigPub: store.Set([]byte(wpub))}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	started := map[string]bool{}
	w, err := worker.New(worker.Config{Fleet: fleet, ID: "w1", Profile: store.WorkerProfile{Platform: "aarch64-linux"},
		Slots: 2, Keys: []ed25519.PublicKey{opub}, SigKey: wpriv,
		PollInterval: time.Hour, Heartbeat: time.Second, ConfigRefresh: time.Hour, RecordRefresh: time.Hour,
		LeaseTTL: time.Minute, Skew: time.Second, JobWatch: time.Second,
		Exec: func(ctx context.Context, c worker.Claim) worker.Outcome {
			mu.Lock()
			started[c.JobID] = true
			mu.Unlock()
			return worker.Outcome{Status: "ok"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(wctx) }()
	defer func() { wcancel(); <-done }()

	// fleet/cfg gains both classes after the worker started (it was configured with none)
	if err := store.Create(ctx, fleet, &store.FleetConfig{Name: store.FleetConfigKey().Name, Gen: store.Set[int64](1),
		Classes: store.Set([]store.FleetClass{{ID: mineID, Platform: mine.Platform}, {ID: otherID, Platform: other.Platform}})}); err != nil {
		t.Fatal(err)
	}
	s1, err := queue(mineID).Enqueue(ctx, orchestrator.Job{ID: "j1", Build: "b1"}, orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := queue(otherID).Enqueue(ctx, orchestrator.Job{ID: "j2", Build: "b1"}, orchestrator.JobFirst)
	if err != nil {
		t.Fatal(err)
	}
	w.Wake(otherID, s2, "j2")
	w.Wake(mineID, s1, "j1")
	isStarted := func(id string) bool { mu.Lock(); defer mu.Unlock(); return started[id] }
	if !env.Wait(20*time.Second, func() bool { return isStarted("j1") }) {
		t.Fatal("the wake-up for a new class the worker satisfies was not claimed")
	}
	time.Sleep(time.Second)
	if isStarted("j2") {
		t.Fatal("the worker claimed a job of a class its profile does not satisfy")
	}
	if got := w.Classes(); len(got) != 1 || got[0] != mineID {
		t.Fatalf("worker classes %v, want only %s", got, mineID)
	}
}
