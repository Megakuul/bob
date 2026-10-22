package orchestrator

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/safe"
)

// Wake is the transport of a wake-up to one attached worker (Attach, SPEC §05.7).
type Wake interface {
	ID() string
	FreeSlots() int
	Wake(class string, slot uint64, job string)
}

// Dispatcher chooses whom to wake after an enqueue (R-FLEET-012).
type Dispatcher struct {
	Queue   *Queue
	Workers []Wake
	// SecondWakeAfter is the delay before a second worker is woken for a job that is still
	// queued (2 s).
	SecondWakeAfter time.Duration
	Seed            uint64

	mu  sync.Mutex
	rng *rand.Rand
}

// candidates returns the idle workers in wake order: the Prefer worker first, then by free
// slots (most first), ties in random order. Workers in skip are left out.
func (d *Dispatcher) candidates(prefer string, skip map[string]bool) []Wake {
	type cand struct {
		w     Wake
		free  int
		pref  bool
		order int
	}
	d.mu.Lock()
	if d.rng == nil {
		d.rng = rand.New(rand.NewPCG(d.Seed, d.Seed^0x9e3779b97f4a7c15))
	}
	var cs []cand
	for _, w := range d.Workers {
		if skip[w.ID()] {
			continue
		}
		if f := w.FreeSlots(); f > 0 {
			cs = append(cs, cand{w: w, free: f, pref: prefer != "" && w.ID() == prefer, order: d.rng.Int()})
		}
	}
	d.mu.Unlock()
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.pref != b.pref {
			return a.pref
		}
		if a.free != b.free {
			return a.free > b.free
		}
		return a.order < b.order
	})
	out := make([]Wake, len(cs))
	for i, c := range cs {
		out[i] = c.w
	}
	return out
}

// Enqueue enqueues j and wakes one idle eligible worker: the Prefer worker first, else the one
// with the most free slots, ties at random; after SecondWakeAfter it wakes a second one if the
// job is still queued.
func (d *Dispatcher) Enqueue(ctx context.Context, j Job) (slot uint64, woken []string, err error) {
	slot, err = d.Queue.Enqueue(ctx, j, JobFirst)
	if err != nil {
		return 0, nil, err
	}
	cs := d.candidates(j.Prefer, nil)
	if len(cs) == 0 {
		return slot, nil, nil // nobody idle: the job waits for a poll (a lost wake-up costs one poll interval)
	}
	first := cs[0]
	first.Wake(d.Queue.Class, slot, j.ID)
	woken = []string{first.ID()}
	if d.SecondWakeAfter > 0 {
		// The second wake-up outlives the request that enqueued the job, but not its deadline.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.SecondWakeAfter+30*time.Second)
		safe.Go(func() error {
			defer cancel()
			return d.secondWake(bg, j, slot, first.ID())
		})
	}
	return slot, woken, nil
}

func (d *Dispatcher) secondWake(ctx context.Context, j Job, slot uint64, first string) error {
	t := time.NewTimer(d.SecondWakeAfter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-t.C:
	}
	v, err := d.Queue.Job(ctx, j.ID)
	if err != nil || v.State != StateQueued {
		return nil // claimed, finished, or gone: one wake-up was enough
	}
	if cs := d.candidates(j.Prefer, map[string]bool{first: true}); len(cs) > 0 {
		cs[0].Wake(d.Queue.Class, slot, j.ID)
	}
	return nil
}
