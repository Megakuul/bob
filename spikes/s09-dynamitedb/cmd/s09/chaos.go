package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	d "github.com/megakuul/dynamitedb"

	"bobspike/s09/internal/fleet"
	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

var (
	nOrch     = flag.Int("orch", 3, "orchestrators (reconcilers)")
	leader    = flag.Bool("leader", false, "reconcile only while holding the ctl/requeue lease")
	nWorkers  = flag.Int("workers", 20, "workers")
	pDie      = flag.Float64("pdie", 0.2, "probability a worker dies/pauses during a job")
	leaseTTL  = flag.Duration("ttl", 3*time.Second, "job lease TTL")
	recEvery  = flag.Duration("reconcile", time.Second, "reconcile interval")
	chaosJobs = flag.Int("chaosjobs", 300, "jobs")
)

type execRec struct {
	worker     string
	epoch      int
	start, end time.Time
}

func chaos(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	base := conn(ctx, "s09-chaos", nil, 0, true)
	enq := fleet.New(base)
	const plat = "chaos"
	build := "b" + model.NewID(time.Now())
	ids := make([]string, *chaosJobs)
	for i := range ids {
		ids[i] = model.NewID(time.Now())
		if err := enq.Enqueue(ctx, fleet.JobRef{Platform: plat, ID: ids[i], Build: build}, nil); err != nil {
			return err
		}
	}
	var (
		mu        sync.Mutex
		execs     = map[string][]execRec{}
		accepted  = map[string]int{}
		staleRej  atomic.Int64
		deaths    atomic.Int64
		pauses    atomic.Int64
		renewLost atomic.Int64
	)
	stop := make(chan struct{})
	// orchestrators
	type ostat struct {
		c                                   *store.Counter
		passes, requeued, finalized, confl int
		led                                 int
	}
	ost := make([]*ostat, *nOrch)
	var owg sync.WaitGroup
	for o := 0; o < *nOrch; o++ {
		ost[o] = &ostat{c: &store.Counter{}}
		cn := store.Open(*endpoint, "s09-chaos", *ak, *sk, ost[o].c, 0)
		q := fleet.New(cn)
		q.LeaseTTL = *leaseTTL
		owg.Add(1)
		go func() {
			defer owg.Done()
			me := fmt.Sprintf("orch%d", o)
			tk := time.NewTicker(*recEvery)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
				}
				if *leader {
					ok, err := q.TryLead(ctx, "requeue", me, 3**recEvery)
					if err != nil || !ok {
						continue
					}
					ost[o].led++
				}
				st, err := q.Reconcile(ctx, plat, 500*time.Millisecond)
				if err != nil {
					fmt.Println(me, "reconcile:", err)
				}
				ost[o].passes++
				ost[o].requeued += st.Requeued
				ost[o].finalized += st.Finalized
				ost[o].confl += st.Conflicts
			}
		}()
	}
	// workers
	var wwg sync.WaitGroup
	wc := &store.Counter{}
	for w := 0; w < *nWorkers; w++ {
		cn := store.Open(*endpoint, "s09-chaos", *ak, *sk, wc, 0)
		q := fleet.New(cn)
		q.LeaseTTL = *leaseTTL
		wwg.Add(1)
		go func() {
			defer wwg.Done()
			gen := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				me := fmt.Sprintf("w%02d-%d", w, gen)
				cl, err := q.Claim(ctx, me, plat, nil)
				if err != nil {
					time.Sleep(200 * time.Millisecond)
					continue
				}
				_ = q.Renew(ctx, cl, model.JobRunning)
				rec := execRec{worker: me, epoch: cl.Epoch, start: time.Now()}
				work := time.Duration(200+rand.IntN(1500)) * time.Millisecond
				die := rand.Float64() < *pDie
				dieAt := time.Duration(rand.Int64N(int64(work)))
				t0 := time.Now()
				lost := false
				for time.Since(t0) < work {
					if die && time.Since(t0) > dieAt {
						break
					}
					time.Sleep(*leaseTTL / 4)
					if err := q.Renew(ctx, cl, ""); errors.Is(err, fleet.ErrLostLease) {
						lost = true
						renewLost.Add(1)
						break
					}
				}
				if die {
					if rand.IntN(2) == 0 {
						deaths.Add(1) // dies for good; a fresh worker identity replaces it
						rec.end = time.Now()
						mu.Lock()
						execs[cl.Ref.ID] = append(execs[cl.Ref.ID], rec)
						mu.Unlock()
						gen++
						continue
					}
					pauses.Add(1) // GC pause / network partition longer than the lease
					time.Sleep(*leaseTTL*2 + time.Second)
				}
				rec.end = time.Now()
				mu.Lock()
				execs[cl.Ref.ID] = append(execs[cl.Ref.ID], rec)
				mu.Unlock()
				if lost {
					continue
				}
				err = q.Complete(ctx, cl, model.JobSucceeded, me)
				if errors.Is(err, fleet.ErrLostLease) {
					staleRej.Add(1)
					continue
				}
				if err != nil {
					fmt.Println("complete:", err)
					continue
				}
				mu.Lock()
				accepted[cl.Ref.ID]++
				mu.Unlock()
			}
		}()
	}
	// wait until all jobs have history and no live job remains
	t0 := time.Now()
	var hist []*model.JobRecord
	for {
		time.Sleep(time.Second)
		var err error
		hist, err = store.Query(ctx, base.Bucket, &model.JobRecord{Build: d.Key(build), ID: d.KeyPrefix("")})
		live, err2 := store.Query(ctx, base.Bucket, &model.LiveJob{Platform: d.Key(plat), ID: d.KeyPrefix("")})
		if err == nil && err2 == nil && len(hist) == len(ids) && len(live) == 0 {
			break
		}
		if time.Since(t0) > 8*time.Minute {
			fmt.Printf("TIMEOUT: history=%d live=%d\n", len(hist), len(live))
			break
		}
	}
	elapsed := time.Since(t0)
	close(stop)
	wwg.Wait()
	owg.Wait()
	// invariants
	lostJobs, doubleAccepted, dupExec, maxAttempts := 0, 0, 0, 0
	have := map[string]bool{}
	for _, h := range hist {
		have[h.ID.Value()] = true
		if a := h.Lease.Value().Attempts; a > maxAttempts {
			maxAttempts = a
		}
	}
	for _, id := range ids {
		if !have[id] {
			lostJobs++
		}
		if accepted[id] > 1 {
			doubleAccepted++
		}
		ex := execs[id]
		for i := range ex {
			for j := i + 1; j < len(ex); j++ {
				if ex[i].start.Before(ex[j].end) && ex[j].start.Before(ex[i].end) {
					dupExec++
				}
			}
		}
	}
	mode := "no leader, every orchestrator reconciles"
	if *leader {
		mode = "leader lease ctl/requeue"
	}
	fmt.Printf("[%s] chaos (%s): orch=%d workers=%d jobs=%d ttl=%v pdie=%.2f -> done in %v\n",
		*backend, mode, *nOrch, *nWorkers, len(ids), *leaseTTL, *pDie, elapsed.Round(time.Millisecond))
	fmt.Printf("[%s]   deaths=%d pauses=%d renewLost=%d staleCompletionsRejected=%d | lostJobs=%d doubleAcceptedCompletions=%d overlappingExecutions=%d maxAttempts=%d\n",
		*backend, deaths.Load(), pauses.Load(), renewLost.Load(), staleRej.Load(), lostJobs, doubleAccepted, dupExec, maxAttempts)
	var rq, fin, cf int
	for o, s := range ost {
		fmt.Printf("[%s]   orch%d passes=%d led=%d requeued=%d finalized=%d conflicts=%d requests: %s (%.1f req/s)\n",
			*backend, o, s.passes, s.led, s.requeued, s.finalized, s.confl, s.c.String(), float64(s.c.Total())/elapsed.Seconds())
		rq += s.requeued
		fin += s.finalized
		cf += s.confl
	}
	fmt.Printf("[%s]   total requeues=%d finalized-by-reconciler=%d reconciler conflicts=%d; worker requests: %s\n", *backend, rq, fin, cf, wc.String())
	return nil
}
