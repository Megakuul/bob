package fleet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/store/env"
)

// clock is the injected time of a test: workers, reconcilers and registrars read it, so
// leases, token lifetimes and registration windows move exactly when the test says.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	real bool
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)} }

// realClock follows the wall clock (chaos runs measure real latencies).
func realClock() *clock { return &clock{real: true} }

func (c *clock) Now() time.Time {
	if c.real {
		return time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// lab is one fleet and control bucket on RustFS with a queue for one class.
type lab struct {
	t       *testing.T
	be      *env.Backend
	fleet   *store.Bucket
	control *store.Bucket
	fname   string // fleet bucket name (for proxy counts)
	cname   string
	class   store.Class
	classID string
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	clk     *clock
	q       *orchestrator.Queue
	kmu     sync.Mutex
	keys    map[string]ed25519.PublicKey // worker id -> result signing key
}

var testClass = store.Class{Platform: "x86_64-linux"}
var testProfile = store.WorkerProfile{Platform: "x86_64-linux"}

// newLab returns nil after failing t as red while store.Open is a stub.
func newLab(t *testing.T) *lab {
	t.Helper()
	be := env.Start(t)
	f := be.Open(t, "fleet")
	if f == nil {
		return nil
	}
	fname := be.Last
	c := be.Open(t, "control")
	if c == nil {
		return nil
	}
	l := &lab{t: t, be: be, fleet: f, control: c, fname: fname, cname: be.Last, class: testClass, clk: newClock(), keys: map[string]ed25519.PublicKey{}}
	var err error
	if l.classID, err = store.ClassID(testClass); err != nil {
		if env.Red(t, err) {
			return nil
		}
		t.Fatal(err)
	}
	if l.pub, l.priv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	l.q = &orchestrator.Queue{Fleet: f, Control: c, Class: l.classID, Shards: 16, Key: l.priv, Kid: "k1",
		WorkerKey: func(wid string) ed25519.PublicKey { l.kmu.Lock(); defer l.kmu.Unlock(); return l.keys[wid] }}
	return l
}

// workerCfg is the test profile of a worker: fast loops, injected clock, no wake-ups unless
// the test delivers them. Exec runs the job.
func (l *lab) workerCfg(id string, exec func(context.Context, worker.Claim) worker.Outcome) worker.Config {
	pub, sig, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		l.t.Fatal(err)
	}
	l.kmu.Lock()
	l.keys[id] = pub
	l.kmu.Unlock()
	return worker.Config{SigKey: sig,
		Fleet: l.fleet, ID: id, Profile: testProfile, Classes: []store.Class{l.class}, Slots: 1,
		Keys:         []ed25519.PublicKey{l.pub},
		PollInterval: 100 * time.Millisecond, Heartbeat: 200 * time.Millisecond,
		ConfigRefresh: time.Second, RecordRefresh: time.Second,
		LeaseTTL: 3 * time.Second, Skew: time.Second, JobWatch: 500 * time.Millisecond,
		Seed: 1, Now: l.clk.Now, Exec: exec,
	}
}

// start runs a worker until the test ends. It returns nil after failing t as red when the
// worker is a stub.
func (l *lab) start(cfg worker.Config) *worker.Worker {
	l.t.Helper()
	w, err := worker.New(cfg)
	if env.Red(l.t, err) {
		return nil
	}
	if err != nil {
		l.t.Fatalf("worker.New(%s): %v", cfg.ID, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := w.Run(ctx); err != nil && ctx.Err() == nil {
			if !env.NotImplemented(err) {
				l.t.Errorf("worker %s: %v", cfg.ID, err)
			}
		}
	}()
	l.t.Cleanup(func() {
		cancel()
		w.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			l.t.Errorf("worker %s did not stop within 10 s (goroutine leak)", cfg.ID)
		}
	})
	return w
}

func (l *lab) job(id string) orchestrator.Job {
	return orchestrator.Job{ID: id, Build: "b1", Spec: []byte("spec-" + id)}
}

func (l *lab) view(id string) *orchestrator.JobView {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := l.q.Job(ctx, id)
	if err != nil {
		return nil
	}
	return v
}

// waitState waits until job id is in one of the states.
func (l *lab) waitState(id string, d time.Duration, states ...string) *orchestrator.JobView {
	l.t.Helper()
	var last *orchestrator.JobView
	env.Wait(d, func() bool {
		last = l.view(id)
		if last == nil {
			return false
		}
		for _, s := range states {
			if last.State == s {
				return true
			}
		}
		return false
	})
	return last
}

// ledger records every Exec call of a test: who ran which job at which epoch.
type ledger struct {
	mu    sync.Mutex
	runs  map[string][]run
	total atomic.Int64
}

type run struct {
	wid   string
	epoch int64
	at    time.Time
}

func newLedger() *ledger { return &ledger{runs: map[string][]run{}} }

func (g *ledger) note(wid string, c worker.Claim) {
	g.mu.Lock()
	g.runs[c.JobID] = append(g.runs[c.JobID], run{wid, c.Epoch, time.Now()})
	g.mu.Unlock()
	g.total.Add(1)
}

func (g *ledger) of(job string) []run {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]run(nil), g.runs[job]...)
}

func (g *ledger) jobs() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.runs)
}

func jobID(n int) string { return fmt.Sprintf("J%05d", n) }
