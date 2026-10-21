package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"bobspike/s09/internal/fleet"
	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

var (
	nClaimers = flag.Int("claimers", 20, "concurrent claimers")
	nJobs     = flag.Int("jobs", 1000, "jobs for drain phase")
	nRounds   = flag.Int("rounds", 100, "rounds for same-job race")
	casMode   = flag.String("cas", "put", "claim CAS mode: put|update|custom")
	keysMode  = flag.Bool("keys", true, "list hints with raw LIST (no GET per hint)")
	pick      = flag.Int("pick", 50, "claimers pick randomly among the first N hints")
)

func stress(ctx context.Context) error {
	c := &store.Counter{}
	cn := conn(ctx, "s09-stress", c, 0, true)
	claimers := make([]*fleet.Q, *nClaimers)
	counters := make([]*store.Counter, *nClaimers)
	for i := range claimers {
		counters[i] = &store.Counter{}
		cc := store.Open(*endpoint, "s09-stress", *ak, *sk, counters[i], 0)
		claimers[i] = fleet.New(cc)
		claimers[i].CAS, claimers[i].Keys, claimers[i].PickAmong = *casMode, *keysMode, *pick
	}
	enq := fleet.New(cn)
	mode := fmt.Sprintf("cas=%s keys=%v pick=%d", *casMode, *keysMode, *pick)

	// Phase A: one job, all claimers at once.
	hist := map[int]int{}
	var lat []time.Duration
	for r := 0; r < *nRounds; r++ {
		ref := fleet.JobRef{Platform: "race", ID: model.NewID(time.Now()), Build: "b1"}
		if err := enq.Enqueue(ctx, ref, nil); err != nil {
			return err
		}
		var wins atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		var mu sync.Mutex
		for i, q := range claimers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				t0 := time.Now()
				cl, err := q.Claim(ctx, fmt.Sprintf("w%02d", i), "race", nil)
				if err == nil {
					wins.Add(1)
					mu.Lock()
					lat = append(lat, time.Since(t0))
					mu.Unlock()
					_ = q.Complete(ctx, cl, model.JobSucceeded, "ok")
				} else if !errors.Is(err, fleet.ErrNoJob) {
					fmt.Println("claim error:", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		hist[int(wins.Load())]++
	}
	fmt.Printf("[%s] same-job race (%s): %d claimers x %d rounds, histogram(winners->rounds)=%v, winner claim latency p50=%v\n",
		*backend, mode, *nClaimers, *nRounds, hist, pct(lat, 50))

	// Phase B: drain N jobs with all claimers.
	for _, cc := range counters {
		cc.Reset()
	}
	t0 := time.Now()
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := 0; i < *nJobs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_ = enq.Enqueue(ctx, fleet.JobRef{Platform: "drain", ID: model.NewID(time.Now()), Build: "b2"}, nil)
		}()
	}
	wg.Wait()
	enqDur := time.Since(t0)
	claims := map[string][]string{}
	var mu sync.Mutex
	var empty atomic.Int64
	t1 := time.Now()
	for i, q := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			misses := 0
			for misses < 3 {
				cl, err := q.Claim(ctx, fmt.Sprintf("w%02d", i), "drain", nil)
				if err != nil {
					if errors.Is(err, fleet.ErrNoJob) {
						misses++
						empty.Add(1)
						time.Sleep(50 * time.Millisecond)
						continue
					}
					fmt.Println("claim error:", err)
					continue
				}
				misses = 0
				mu.Lock()
				claims[cl.Ref.ID] = append(claims[cl.Ref.ID], cl.Owner)
				mu.Unlock()
				if err := q.Complete(ctx, cl, model.JobSucceeded, "ok"); err != nil {
					fmt.Println("complete error:", err)
				}
			}
		}()
	}
	wg.Wait()
	dur := time.Since(t1)
	doubles := 0
	for _, w := range claims {
		if len(w) > 1 {
			doubles++
		}
	}
	var tot store.Counter
	var conflicts, stale, amb int64
	for i, cc := range counters {
		tot.List.Add(cc.List.Load())
		tot.Get.Add(cc.Get.Load())
		tot.Put.Add(cc.Put.Load())
		tot.Delete.Add(cc.Delete.Load())
		tot.Status412.Add(cc.Status412.Load())
		conflicts += claimers[i].Conflicts
		stale += claimers[i].StaleHints
		amb += claimers[i].AmbiguousWins
	}
	// remaining live jobs?
	left, _ := store.Query(ctx, cn.Bucket, liveDrain())
	fmt.Printf("[%s] drain (%s): jobs=%d claimed=%d double-claims=%d leftLive=%d enqueue=%v (%.0f/s) drain=%v (%.0f claims/s)\n",
		*backend, mode, *nJobs, len(claims), doubles, len(left), enqDur.Round(time.Millisecond), float64(*nJobs)/enqDur.Seconds(),
		dur.Round(time.Millisecond), float64(len(claims))/dur.Seconds())
	fmt.Printf("[%s]   claimer requests (claim+complete, incl. %d empty polls): %s; per job=%.1f; CAS conflicts=%d staleHints=%d ambiguousWins=%d\n",
		*backend, empty.Load(), tot.String(), float64(tot.Total())/float64(len(claims)), conflicts, stale, amb)
	return nil
}

func liveDrain() *model.LiveJob {
	return &model.LiveJob{Platform: dkey("drain"), ID: dprefix("")}
}

func pct(xs []time.Duration, p int) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := len(s) * p / 100
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i].Round(100 * time.Microsecond)
}
