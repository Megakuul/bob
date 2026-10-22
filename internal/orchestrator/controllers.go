package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
)

// Controller names of SPEC §05.11 (R-SRV-004) plus the hourly staging sweep (R-STORE-105).
const (
	CtlMembership      = "membership"
	CtlRegistrar       = "registrar"
	CtlBuild           = "build"
	CtlJobReconciler   = "job-reconciler"
	CtlPromotion       = "promotion"
	CtlRelease         = "release"
	CtlLakeMaintenance = "lake-maintenance"
	CtlRetention       = "retention"
	CtlForgeSweeper    = "forge-sweeper"
	CtlIndexRepair     = "index-repair"
	CtlKey             = "key"
	CtlStagingSweep    = "staging-sweep"
)

// ControllerNames lists every controller lease name (ctl/<name>), for
// ListOrchestrators.leader_of.
func ControllerNames() []string {
	return []string{CtlMembership, CtlRegistrar, CtlBuild, CtlJobReconciler, CtlPromotion,
		CtlRelease, CtlLakeMaintenance, CtlRetention, CtlForgeSweeper, CtlIndexRepair, CtlKey, CtlStagingSweep}
}

// LeaseTTLFactor is the controller lease TTL in controller intervals. SPEC §05.6.5 says 3, but
// R-SRV-004 also asks that a dead holder's lease changes owner within 3 × interval, which a TTL
// of 3 × interval plus the takeover skew can never meet; 2 × interval leaves interval − skew
// for the takeover (decision D-IB8-1). Correctness never depends on the lease.
const LeaseTTLFactor = 2

// Run is what a controller pass gets from its runner.
type Run struct {
	Epoch uint64 // lease epoch of this leadership
	// State is the controller's persisted state (Controller.Persist): it comes from ctl/<name>
	// and whatever the pass leaves in it is saved there after the pass.
	State []byte
	// Now is the pass time (the orchestrator clock).
	Now time.Time
}

// Controller runs one leader-only controller (SPEC §05.11, R-SRV-004): every orchestrator runs
// it, and its pass runs only while this orchestrator holds the lease ctl/<Name>. It implements
// server.Component.
type Controller struct {
	ID       string        // controller name (lease ctl/<ID>)
	Fleet    *store.Bucket // where ctl/<ID> lives
	Owner    string        // this orchestrator's id
	Interval time.Duration // SPEC interval: lease TTL base and default pass period
	// Every is the pass period when it differs from Interval (membership prunes every 5 s
	// to keep the 40 s removal bound of R-SRV-102).
	Every time.Duration
	Skew  time.Duration // fleet.skew: a lapsed lease is taken over after expires + Skew
	Now   func() time.Time
	// Persist keeps the time of the last pass and Run.State in ctl/<ID>.cu, so that a new
	// leader does not repeat an hourly pass that its predecessor just ran. Controllers that
	// own the cursor themselves (registrar) leave it false.
	Persist bool
	Pass    func(ctx context.Context, r *Run) error
	Wake    <-chan struct{} // optional: run a pass at once (event-driven controllers)
	Log     *slog.Logger

	held    bool
	epoch   uint64
	expires time.Time // of the lease as last written or read
	renewed time.Time
	last    time.Time // last pass (from the cursor when Persist)
	state   []byte
}

// Name implements server.Component.
func (c *Controller) Name() string { return "controller " + c.ID }

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Controller) ttl() time.Duration { return LeaseTTLFactor * c.Interval }

// renewAfter is when a held lease is renewed: half its TTL (one interval) is left as margin
// for a failed or slow renewal.
func (c *Controller) renewAfter() time.Duration { return c.ttl() / 2 }

func (c *Controller) every() time.Duration {
	if c.Every > 0 {
		return c.Every
	}
	return c.Interval
}

func (c *Controller) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// cursorDoc is ctl/<name>.cu of a Persist controller.
type cursorDoc struct {
	Last  time.Time `cbor:"t,omitzero"`
	State []byte    `cbor:"s,omitempty"`
}

func decodeCursor(b []byte) cursorDoc {
	var d cursorDoc
	if len(b) > 0 && cbor.Unmarshal(b, &d) != nil {
		return cursorDoc{} // not ours (or damaged): start over, the passes are idempotent
	}
	return d
}

// Run implements server.Component: acquire, renew, pass, sleep, until ctx ends; then the lease
// is released so another orchestrator takes over at once (§05.6.5 shutdown).
func (c *Controller) Run(ctx context.Context) error {
	if c.Fleet == nil || c.Owner == "" || c.Interval <= 0 || c.Pass == nil {
		return fmt.Errorf("orchestrator: controller %s needs the fleet bucket, an owner, an interval and a pass", c.ID)
	}
	if _, err := store.Key("ctl", c.ID); err != nil {
		return err
	}
	c.held = false
	defer c.release()
	// A fresh leader passes at once: its last pass time is zero, or for a Persist controller the
	// predecessor's, so an hourly pass that just ran is not repeated.
	due := false
	for {
		now := c.now()
		if err := c.acquire(ctx, now); err != nil && ctx.Err() == nil {
			c.log().Warn("controller lease not checked", "controller", c.ID, "err", err.Error())
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(min(c.every(), time.Second)):
				continue
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if c.held && (due || !now.Before(c.last.Add(c.every()))) {
			due = false
			c.pass(ctx, now)
			now = c.now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.Wake:
			due = true
		case <-time.After(c.sleep(now)):
		}
	}
}

// sleep is the time until the next lease check: the next renewal or pass while held; while
// another orchestrator holds the lease, the moment it may be taken over (but at most one pass
// period, so that a renewal by the holder is noticed).
func (c *Controller) sleep(now time.Time) time.Duration {
	var d time.Duration
	if c.held {
		next := c.last.Add(c.every())
		if r := c.renewed.Add(c.renewAfter()); r.Before(next) {
			next = r
		}
		d = next.Sub(now)
	} else {
		d = c.every()
		if t := c.expires.Add(c.Skew).Sub(now) + 50*time.Millisecond; !c.expires.IsZero() && t < d {
			d = t
		}
	}
	d += time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
	return max(d, 100*time.Millisecond)
}

// acquire runs the lease state machine of §05.6.5 with this orchestrator's clock and skew: create
// when absent, renew when owned (after half the TTL), take over when lapsed. It keeps the
// cursor.
func (c *Controller) acquire(ctx context.Context, now time.Time) error {
	if c.held && now.Before(c.renewed.Add(c.renewAfter())) && now.Before(c.expires) {
		return nil
	}
	var other *store.ControllerLease
	got, err := store.CAS(ctx, c.Fleet, &store.ControllerLease{Name: store.ID(c.ID)}, func(cur *store.ControllerLease) (*store.ControllerLease, error) {
		other = nil
		next := &store.ControllerLease{Name: store.ID(c.ID), Owner: store.Set(c.Owner), Epoch: store.Set[uint64](1), Expires: store.Set(now.Add(c.ttl()))}
		switch {
		case cur == nil:
		case cur.Owner.Value() == c.Owner && !cur.Expires.Value().Add(c.Skew).Before(now):
			next.Epoch, next.Cursor = store.Set(cur.Epoch.Value()), store.Set(cur.Cursor.Value())
		case cur.Owner.Value() == "" || cur.Expires.Value().Add(c.Skew).Before(now):
			// A lapsed lease is taken over with a new epoch, even by its last owner: another
			// orchestrator may have acted meanwhile (D-IB1-5).
			next.Epoch, next.Cursor = store.Set(cur.Epoch.Value()+1), store.Set(cur.Cursor.Value())
		default:
			other = cur
			return nil, store.ErrNoChange
		}
		return next, nil
	})
	if err != nil {
		c.held = false
		return err
	}
	if other != nil || got == nil || got.Owner.Value() != c.Owner {
		c.held = false
		if other != nil {
			c.expires = other.Expires.Value()
		}
		return nil
	}
	if !c.held || got.Epoch.Value() != c.epoch {
		c.log().Info("controller leadership acquired", "controller", c.ID, "epoch", got.Epoch.Value(), "orchestrator", c.Owner)
		if c.Persist {
			d := decodeCursor(got.Cursor.Value())
			c.last, c.state = d.Last, d.State
		} else {
			c.last, c.state = time.Time{}, nil
		}
	}
	c.held, c.epoch, c.expires, c.renewed = true, got.Epoch.Value(), got.Expires.Value(), now
	return nil
}

// pass runs one pass; a failure is logged and the pass is retried at the next period. A
// pass that panics is converted to an error by the supervisor; here it would end Run, so the
// pass is guarded.
func (c *Controller) pass(ctx context.Context, now time.Time) {
	r := &Run{Epoch: c.epoch, State: c.state, Now: now}
	err := guard(func() error { return c.Pass(ctx, r) })
	c.last = now
	if err != nil {
		if ctx.Err() == nil {
			c.log().Warn("controller pass failed", "controller", c.ID, "err", err.Error(), "orchestrator", c.Owner)
		}
	} else {
		c.state = r.State
	}
	if !c.Persist || ctx.Err() != nil {
		return
	}
	body, err := cbor.Marshal(cursorDoc{Last: c.last, State: c.state})
	if err != nil {
		c.log().Warn("controller state not saved", "controller", c.ID, "err", err.Error())
		return
	}
	epoch := c.epoch
	_, err = store.CAS(ctx, c.Fleet, &store.ControllerLease{Name: store.ID(c.ID)}, func(cur *store.ControllerLease) (*store.ControllerLease, error) {
		if cur == nil || cur.Owner.Value() != c.Owner || cur.Epoch.Value() != epoch {
			return nil, errLeaseLost // another leader took over: its state wins
		}
		next := *cur
		next.Cursor = store.Set(body)
		return &next, nil
	})
	if errors.Is(err, errLeaseLost) {
		c.held = false
	} else if err != nil && ctx.Err() == nil {
		c.log().Warn("controller state not saved", "controller", c.ID, "err", err.Error())
	}
}

var errLeaseLost = errors.New("controller lease lost")

// release gives the lease up (expires in the past, CAS) when this orchestrator holds it.
func (c *Controller) release() {
	if !c.held {
		return
	}
	c.held = false
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := c.now()
	_, err := store.CAS(ctx, c.Fleet, &store.ControllerLease{Name: store.ID(c.ID)}, func(cur *store.ControllerLease) (*store.ControllerLease, error) {
		if cur == nil || cur.Owner.Value() != c.Owner || cur.Epoch.Value() != c.epoch {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Expires = store.Set(now.Add(-c.Skew - time.Second))
		return &next, nil
	})
	if err != nil {
		c.log().Warn("controller lease not released", "controller", c.ID, "err", err.Error())
	}
}

// guard runs fn and converts a panic into an error (a pass must not end the controller loop).
func guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("orchestrator: pass panicked: %v", r)
		}
	}()
	return fn()
}

// Leaders reads ctl/<name> of every controller and returns, per orchestrator id, the
// controllers whose lease it holds now (owner and not expired). Reads use cache (1 request
// per controller, 304 when unchanged).
func Leaders(ctx context.Context, fleet *store.Bucket, cache *store.Cache[store.ControllerLease], now time.Time) (map[string][]string, error) {
	out := map[string][]string{}
	for _, name := range ControllerNames() {
		l, _, err := store.GetCached(ctx, fleet, cache, &store.ControllerLease{Name: store.ID(name)})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if l.Owner.Value() != "" && now.Before(l.Expires.Value()) {
			out[l.Owner.Value()] = append(out[l.Owner.Value()], name)
		}
	}
	return out, nil
}
