// Package chaos is the kill -9 driver of the chaos suite (HARNESS.md §8.2):
// it SIGKILLs or pauses seeded choices of orchestrator and worker processes and
// logs every kill point, so a failing run replays with -chaos-seed.
package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"syscall"
	"time"
)

// seedFlag is the replay seed. 0 means "pick one and log it".
var seedFlag = flag.Uint64("chaos-seed", 0, "seed of the chaos driver (0: random, logged)")

// Seed returns -chaos-seed, or a fresh random seed when it is unset.
func Seed() uint64 {
	if *seedFlag != 0 {
		return *seedFlag
	}
	return rand.Uint64()
}

// Target is one process the driver may hit.
type Target struct {
	Name    string
	Pid     func() int                      // current pid (changes after a restart)
	Group   bool                            // signal the whole process group
	Restart func(ctx context.Context) error // optional: called after a kill
}

// Event is one kill point.
type Event struct {
	Seq    int           `json:"seq"`
	Seed   uint64        `json:"seed"`
	Action string        `json:"action"` // "kill" or "pause"
	Target string        `json:"target"`
	Pid    int           `json:"pid"`
	At     time.Time     `json:"at"`
	Pause  time.Duration `json:"pause,omitempty"`
	Err    string        `json:"err,omitempty"`
}

// Driver chooses and hits targets. It is safe for concurrent use.
type Driver struct {
	seed uint64
	log  io.Writer

	mu     sync.Mutex
	rng    *rand.Rand
	seq    int
	events []Event
}

// New returns a driver; every event is written to log as one JSON line.
func New(seed uint64, log io.Writer) *Driver {
	if log == nil {
		log = io.Discard
	}
	d := &Driver{seed: seed, log: log, rng: rand.New(rand.NewPCG(seed, seed^0xda3e39cb94b95bdb))}
	fmt.Fprintf(log, "{\"chaos_seed\":%d}\n", seed)
	return d
}

// Events returns the events so far.
func (d *Driver) Events() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Event(nil), d.events...)
}

func (d *Driver) record(e Event) Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	e.Seq, e.Seed, e.At = d.seq, d.seed, time.Now()
	d.events = append(d.events, e)
	if b, err := json.Marshal(e); err == nil {
		_, _ = d.log.Write(append(b, '\n'))
	}
	return e
}

// Choose returns n distinct targets, chosen by the seeded sequence.
func (d *Driver) Choose(targets []Target, n int) []Target {
	d.mu.Lock()
	perm := d.rng.Perm(len(targets))
	d.mu.Unlock()
	n = min(n, len(targets))
	out := make([]Target, n)
	for i := range n {
		out[i] = targets[perm[i]]
	}
	return out
}

func signal(t Target, sig syscall.Signal) (int, error) {
	pid := t.Pid()
	if pid <= 0 {
		return pid, fmt.Errorf("chaos: %s has no process", t.Name)
	}
	target := pid
	if t.Group {
		target = -pid
	}
	if err := syscall.Kill(target, sig); err != nil {
		return pid, fmt.Errorf("chaos: signal %v to %s (pid %d): %w", sig, t.Name, pid, err)
	}
	return pid, nil
}

// Kill sends SIGKILL to t, then restarts it if t.Restart is set.
func (d *Driver) Kill(ctx context.Context, t Target) error {
	pid, err := signal(t, syscall.SIGKILL)
	e := Event{Action: "kill", Target: t.Name, Pid: pid}
	if err == nil && t.Restart != nil {
		err = t.Restart(ctx)
	}
	if err != nil {
		e.Err = err.Error()
	}
	d.record(e)
	return err
}

// Pause stops t with SIGSTOP for dur (e.g. 2×TTL+1 s) and resumes it.
func (d *Driver) Pause(ctx context.Context, t Target, dur time.Duration) error {
	pid, err := signal(t, syscall.SIGSTOP)
	if err != nil {
		d.record(Event{Action: "pause", Target: t.Name, Pid: pid, Pause: dur, Err: err.Error()})
		return err
	}
	d.record(Event{Action: "pause", Target: t.Name, Pid: pid, Pause: dur})
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	// Always resume, even when ctx ended, so no stopped process is left behind.
	if _, err := signal(t, syscall.SIGCONT); err != nil {
		return err
	}
	return ctx.Err()
}

// KillFraction kills a seeded fraction of targets (R-FLEET-003: 20 % of the
// workers) and returns those it killed.
func (d *Driver) KillFraction(ctx context.Context, targets []Target, frac float64) ([]Target, error) {
	n := int(float64(len(targets))*frac + 0.5)
	chosen := d.Choose(targets, n)
	var errs []error
	for _, t := range chosen {
		errs = append(errs, d.Kill(ctx, t))
	}
	return chosen, errors.Join(errs...)
}

// Every kills one seeded target from targets() every interval until ctx ends
// (R-SRV-004: a random orchestrator every 20 s). Kill errors are logged as
// events, not returned: a target that died on its own is not a harness failure.
func (d *Driver) Every(ctx context.Context, interval time.Duration, targets func() []Target) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if ts := d.Choose(targets(), 1); len(ts) == 1 {
				_ = d.Kill(ctx, ts[0])
			}
		}
	}
}
