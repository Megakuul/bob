package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"bobspike/s09/internal/fleet"
	"bobspike/s09/internal/model"
	"bobspike/s09/internal/store"
)

var (
	pollWorkers  = flag.Int("pollworkers", 100, "simulated workers")
	pollInterval = flag.Duration("interval", 5*time.Second, "poll interval when the queue was empty")
	pollFor      = flag.Duration("for", 60*time.Second, "duration")
	enqEvery     = flag.Duration("enqevery", time.Second, "enqueue one job every ... (0 = none)")
)

// poll simulates N workers polling one platform queue; measures request rate and enqueue->claim latency.
func poll(ctx context.Context) error {
	base := conn(ctx, "s09-poll", nil, 0, true)
	c := &store.Counter{}
	const plat = "poll"
	enqAt := sync.Map{}
	var mu sync.Mutex
	var lat []time.Duration
	var claims int
	stop := time.Now().Add(*pollFor)
	var wg sync.WaitGroup
	for w := 0; w < *pollWorkers; w++ {
		cn := store.Open(*endpoint, "s09-poll", *ak, *sk, c, 0)
		q := fleet.New(cn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.Int64N(int64(*pollInterval)))) // spread start
			for time.Now().Before(stop) {
				cl, err := q.Claim(ctx, fmt.Sprintf("w%03d", w), plat, nil)
				if err != nil {
					if !errors.Is(err, fleet.ErrNoJob) {
						fmt.Println("claim:", err)
					}
					// jittered interval (0.8..1.2)
					time.Sleep(time.Duration(float64(*pollInterval) * (0.8 + 0.4*rand.Float64())))
					continue
				}
				if t, ok := enqAt.Load(cl.Ref.ID); ok {
					mu.Lock()
					lat = append(lat, time.Since(t.(time.Time)))
					claims++
					mu.Unlock()
				}
				_ = q.Complete(ctx, cl, model.JobSucceeded, "ok")
				// a busy worker polls again immediately after finishing
			}
		}()
	}
	enq := fleet.New(base)
	enqueued := 0
	if *enqEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop.Add(-*pollInterval * 2)) {
				id := model.NewID(time.Now())
				enqAt.Store(id, time.Now())
				if err := enq.Enqueue(ctx, fleet.JobRef{Platform: plat, ID: id, Build: "b"}, nil); err == nil {
					enqueued++
				}
				time.Sleep(*enqEvery)
			}
		}()
	}
	t0 := time.Now()
	wg.Wait()
	el := time.Since(t0).Seconds()
	tot := float64(c.Total())
	// AWS S3 standard us-east-1 list price: LIST/PUT $0.005 per 1k, GET $0.0004 per 1k (DELETE free).
	month := 30 * 86400.0
	cost := (float64(c.List.Load()+c.Put.Load())*0.005/1000 + float64(c.Get.Load())*0.0004/1000) / el * month
	fmt.Printf("[%s] poll: workers=%d interval=%v enqueue every %v for %v: enqueued=%d claimed=%d\n", *backend, *pollWorkers, *pollInterval, *enqEvery, *pollFor, enqueued, claims)
	fmt.Printf("[%s]   requests %.1f/s (%s); AWS-list-price equivalent $%.0f/month\n", *backend, tot/el, c.String(), cost)
	fmt.Printf("[%s]   enqueue->claim latency p50=%v p90=%v p99=%v max=%v\n", *backend, pct(lat, 50), pct(lat, 90), pct(lat, 99), pct(lat, 100))
	return nil
}
