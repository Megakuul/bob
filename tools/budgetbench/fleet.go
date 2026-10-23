package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

// lab is a fleet and a control bucket on a local RustFS behind the counting proxy, with the
// queue of one class: the set-up of the R-FLEET-009/010 acceptance tests (tests/store/fleet),
// without the testing package.
type lab struct {
	fs             *rustfs.Server
	proxy          *s3count.Server
	fleet, control *store.Bucket
	fname, cname   string
	status         *orchestrator.Queue // the same queue read directly from RustFS (not counted)
	classID        string
	priv           ed25519.PrivateKey
	pub            ed25519.PublicKey
	q              *orchestrator.Queue
	kmu            sync.Mutex
	keys           map[string]ed25519.PublicKey
	stops          []func()
}

var (
	benchClass   = store.Class{Platform: "x86_64-linux"}
	benchProfile = store.WorkerProfile{Platform: "x86_64-linux"}
)

func (b *bench) newLab(ctx context.Context) (*lab, error) {
	dir := filepath.Join(b.work, "rustfs")
	_ = os.RemoveAll(dir)
	fs, err := rustfs.Start(ctx, rustfs.Options{Dir: dir, NoSetup: true})
	if err != nil {
		return nil, err
	}
	l := &lab{fs: fs, keys: map[string]ed25519.PublicKey{}}
	l.stops = append(l.stops, func() { _ = fs.Stop() })
	px, err := s3count.New(s3count.Options{Backend: fs.URL, Secrets: map[string]string{fs.Root.AccessKey: fs.Root.SecretKey},
		Roles: map[string]string{fs.Root.AccessKey: "root"}})
	if err != nil {
		l.stop()
		return nil, err
	}
	if l.proxy, err = s3count.Listen(px, 0); err != nil {
		l.stop()
		return nil, err
	}
	l.stops = append(l.stops, func() { _ = l.proxy.Close() })
	for k, v := range map[string]string{"AWS_ACCESS_KEY_ID": fs.Root.AccessKey, "AWS_SECRET_ACCESS_KEY": fs.Root.SecretKey,
		"AWS_REGION": fs.Root.Region} {
		if err := os.Setenv(k, v); err != nil {
			l.stop()
			return nil, err
		}
	}
	var direct [2]*store.Bucket
	open := func(role string, k int) (*store.Bucket, string, error) {
		name := fmt.Sprintf("bench-%s-%d", role, os.Getpid())
		if err := fs.CreateBucket(ctx, name); err != nil {
			return nil, "", err
		}
		bk, err := store.Open(ctx, store.BucketConfig{Endpoint: l.proxy.URL, Region: fs.Root.Region, Name: name, Role: role})
		if err != nil {
			return nil, "", err
		}
		direct[k], err = store.Open(ctx, store.BucketConfig{Endpoint: fs.URL, Region: fs.Root.Region, Name: name, Role: role})
		return bk, name, err
	}
	if l.fleet, l.fname, err = open("fleet", 0); err != nil {
		l.stop()
		return nil, err
	}
	if l.control, l.cname, err = open("control", 1); err != nil {
		l.stop()
		return nil, err
	}
	if l.classID, err = store.ClassID(benchClass); err != nil {
		l.stop()
		return nil, err
	}
	if l.pub, l.priv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		l.stop()
		return nil, err
	}
	l.q = &orchestrator.Queue{Fleet: l.fleet, Control: l.control, Class: l.classID, Shards: 16, Key: l.priv, Kid: "k1",
		WorkerKey: func(wid string) ed25519.PublicKey { l.kmu.Lock(); defer l.kmu.Unlock(); return l.keys[wid] },
		Now:       func() time.Time { return time.Now().UTC() }}
	st := *l.q
	st.Fleet, st.Control = direct[0], direct[1]
	l.status = &st
	return l, nil
}

func (l *lab) stop() {
	for i := len(l.stops) - 1; i >= 0; i-- {
		l.stops[i]()
	}
}

// worker starts a worker with the given loop intervals; exec runs claimed jobs.
func (l *lab) worker(id string, poll, heartbeat, cfg, record, watch time.Duration, seed uint64,
	exec func(context.Context, worker.Claim) worker.Outcome) (*worker.Worker, error) {
	pub, sig, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	l.kmu.Lock()
	l.keys[id] = pub
	l.kmu.Unlock()
	w, err := worker.New(worker.Config{SigKey: sig, Fleet: l.fleet, ID: id, Profile: benchProfile,
		Classes: []store.Class{benchClass}, Slots: 1, Keys: []ed25519.PublicKey{l.pub},
		PollInterval: poll, Heartbeat: heartbeat, ConfigRefresh: cfg, RecordRefresh: record,
		LeaseTTL: 3 * time.Second, Skew: time.Second, JobWatch: watch, Seed: seed,
		Now: func() time.Time { return time.Now().UTC() }, Exec: exec})
	if err != nil {
		return nil, fmt.Errorf("worker.New(%s): %w", id, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	l.stops = append(l.stops, func() {
		cancel()
		w.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return w, nil
}

func (l *lab) reqs(c s3count.Counts) int {
	n := 0
	for _, r := range c.Rows {
		if r.Bucket == l.fname || r.Bucket == l.cname {
			n += r.Count
		}
	}
	return n
}

func (l *lab) lists(c s3count.Counts) int { return c.Lists(l.fname) + c.Lists(l.cname) }

// fleet measures the requests per job (B-057 R-NFR-102, B-088 R-FLEET-009): 300 jobs enqueued
// with wake-ups on 20 workers, claimed, run and finalised; mean requests on control+fleet per
// job, and LIST requests (must be 0). Heartbeats, job watch and idle polls are excluded by
// their own rule, as in the acceptance test: the workers' timers are an hour.
func (b *bench) fleetJobs() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	l, err := b.newLab(ctx)
	if err != nil {
		return err
	}
	defer l.stop()
	const jobs, workers = 300, 20
	var targets []orchestrator.Wake
	for i := range workers {
		w, err := l.worker(fmt.Sprintf("w%02d", i), time.Hour, time.Hour, time.Hour, time.Hour, time.Hour, 1,
			func(context.Context, worker.Claim) worker.Outcome { return worker.Outcome{Status: "ok"} })
		if err != nil {
			return err
		}
		targets = append(targets, w)
	}
	time.Sleep(2 * time.Second) // start-up reads
	l.proxy.Reset()
	d := &orchestrator.Dispatcher{Queue: l.q, Workers: targets, SecondWakeAfter: 2 * time.Second, Seed: 9}
	id := func(i int) string { return fmt.Sprintf("job-%03d", i) }
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for e := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := e; i < jobs; i += 3 {
				if _, _, err := d.Enqueue(ctx, orchestrator.Job{ID: id(i), Build: "b1", Spec: []byte("spec-" + id(i))}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for i := 0; i < jobs; {
		v, err := l.status.Job(ctx, id(i))
		if err == nil && v != nil && v.State == "succeeded" {
			i++
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("job %s did not succeed within 3 min", id(i))
		}
		time.Sleep(200 * time.Millisecond)
	}
	for i := range jobs {
		if ok, err := l.q.Finalise(ctx, id(i)); err != nil || !ok {
			return fmt.Errorf("finalise %s: accepted=%v, %v", id(i), ok, err)
		}
	}
	c := l.proxy.Counts()
	total, lists := l.reqs(c), l.lists(c)
	mean := float64(total) / jobs
	b.note("%d jobs on %d workers with wake-ups: %d requests on control+fleet (mean %.2f per job), %d LIST", jobs, workers, total, mean, lists)
	b.budget("B-057", mean, "count", "value", nil)
	b.budget("B-088", mean, "count", "value", nil)
	if lists > 0 {
		return fmt.Errorf("%d LIST requests on control+fleet (B-057/B-088 allow 0)", lists)
	}
	return nil
}

// fleetIdle measures the idle worker cost (B-058, R-NFR-103): 20 idle workers with the default
// intervals (poll 30 s ± 20 %, heartbeat 15 s, config 30 s, worker record 60 s, job watch 30 s)
// on one class, started over 15 s, 15 s warm-up, then all their requests on control+fleet over
// a 60 s window, per worker and second. The SPEC design rate is exactly 4 heartbeats + 2 polls +
// 2 config reads + 1 record read per minute = 0.15 req/s, the budget itself.
func (b *bench) fleetIdle() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	l, err := b.newLab(ctx)
	if err != nil {
		return err
	}
	defer l.stop()
	const n, warm, window = 20, 15 * time.Second, 60 * time.Second
	for i := range n {
		// Starts spread over one heartbeat period, so the window's edges do not coincide with
		// every worker's timers at once (aligned timers put 0 or 20 extra events at an edge).
		time.Sleep(15 * time.Second / n)
		if _, err := l.worker(fmt.Sprintf("idle%03d", i), 30*time.Second, 15*time.Second, 30*time.Second, 60*time.Second,
			30*time.Second, uint64(i+1), func(context.Context, worker.Claim) worker.Outcome { return worker.Outcome{Status: "ok"} }); err != nil {
			return err
		}
	}
	time.Sleep(warm)
	l.proxy.Reset()
	time.Sleep(window)
	c := l.proxy.Counts()
	rate := float64(l.reqs(c)) / (n * window.Seconds())
	b.note("%d idle workers, %s window: %d requests on control+fleet, %d LIST", n, window, l.reqs(c), l.lists(c))
	kinds := map[string]int{}
	for _, r := range c.Rows {
		if r.Bucket == l.fname || r.Bucket == l.cname {
			kinds[r.Bucket+" "+r.Op+" "+r.Prefix] += r.Count
		}
	}
	for k, v := range kinds {
		b.note("  %s: %d (%.4f per worker and second)", k, v, float64(v)/(n*window.Seconds()))
	}
	b.budget("B-058", rate, "req/s", "value", nil)
	return nil
}
