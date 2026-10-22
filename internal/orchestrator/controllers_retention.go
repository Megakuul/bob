package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
)

// Retention defaults (SPEC §05.11.1 retention.*).
const (
	DefaultRetainLogs      = 30 * 24 * time.Hour
	DefaultRetainAnalytics = 400 * 24 * time.Hour
	DefaultRetainBuilds    = 365 * 24 * time.Hour
	DefaultRetainCache     = 90 * 24 * time.Hour
)

// slotKeep is how many sequence-log slots stay behind the head (qs) or cursor (wrs).
const slotKeep = 1000

// maxSlotDeletes bounds the slot deletes of one log in one pass; the next pass continues.
const maxSlotDeletes = 10_000

// Retention is the retention controller (hourly, R-STORE-007). Builds, queue and
// registration slots are deleted by known key; log and analytics partitions and cache paths
// are dropped through the hooks of their owners (the lake sweep, R-STORE-101, and the §06
// cache reference rule). Releases are never collected.
type Retention struct {
	Control, Fleet *store.Bucket
	Logs           time.Duration // retention.logs
	Analytics      time.Duration // retention.analytics
	Builds         time.Duration // retention.builds
	Cache          time.Duration // retention.cache
	// Projects returns every project id; nil skips build retention.
	Projects func(ctx context.Context) ([]string, error)
	// DropPartitions drops the lake partitions of class ("log", "run", "job") older than
	// before (the R-STORE-101 lake sweep); nil skips them.
	DropPartitions func(ctx context.Context, class string, before time.Time) (int, error)
	// CacheGC removes cache paths of promo/<day> logs older than before that no release or
	// recent build references (§06); nil skips it.
	CacheGC func(ctx context.Context, before time.Time) (int, error)
	Log     *slog.Logger
}

// retentionState is ctl/retention's persisted state: how far each walk got.
type retentionState struct {
	Queues map[string]uint64 `cbor:"q,omitempty"` // class -> lowest slot not yet deleted
	Reg    uint64            `cbor:"r,omitempty"` // lowest wrs slot not yet deleted
	Pages  map[string]uint64 `cbor:"b,omitempty"` // project -> first bpage page with live entries
}

// RetentionResult counts what one pass removed.
type RetentionResult struct {
	Builds, QueueSlots, RegSlots, Partitions, CachePaths int
}

func (x *Retention) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Pass runs one retention pass.
func (x *Retention) Pass(ctx context.Context, r *Run) error {
	_, err := x.Collect(ctx, r)
	return err
}

// Collect is Pass with its counts (tests).
func (x *Retention) Collect(ctx context.Context, r *Run) (RetentionResult, error) {
	var res RetentionResult
	var st retentionState
	if len(r.State) > 0 {
		_ = cbor.Unmarshal(r.State, &st) // damaged state: the walks restart, deletes are idempotent
	}
	var errs []error
	add := func(n *int, k int, err error) {
		*n += k
		if err != nil {
			errs = append(errs, err)
		}
	}
	if x.DropPartitions != nil {
		k, err := x.DropPartitions(ctx, "log", r.Now.Add(-orDefault(x.Logs, DefaultRetainLogs)))
		add(&res.Partitions, k, err)
		for _, class := range []string{"run", "job"} {
			k, err := x.DropPartitions(ctx, class, r.Now.Add(-orDefault(x.Analytics, DefaultRetainAnalytics)))
			add(&res.Partitions, k, err)
		}
	}
	if x.Control != nil && x.Projects != nil {
		k, err := x.builds(ctx, r.Now.Add(-orDefault(x.Builds, DefaultRetainBuilds)), &st)
		add(&res.Builds, k, err)
	}
	if x.Fleet != nil {
		k, err := x.queues(ctx, &st)
		add(&res.QueueSlots, k, err)
		k, err = x.registrations(ctx, &st)
		add(&res.RegSlots, k, err)
	}
	if x.CacheGC != nil {
		k, err := x.CacheGC(ctx, r.Now.Add(-orDefault(x.Cache, DefaultRetainCache)))
		add(&res.CachePaths, k, err)
	}
	if b, err := cbor.Marshal(st); err == nil {
		r.State = b
	}
	if n := res.Builds + res.QueueSlots + res.RegSlots + res.Partitions + res.CachePaths; n > 0 {
		x.log().Info("retention pass", "builds", res.Builds, "queue_slots", res.QueueSlots, "reg_slots", res.RegSlots,
			"partitions", res.Partitions, "cache_paths", res.CachePaths)
	}
	return res, errors.Join(errs...)
}

// TerminalBuild reports whether a build state is final.
func TerminalBuild(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "superseded", "skipped":
		return true
	}
	return false
}

// buildNodes is the key model of bnodes/<buildID> (only deleted here; the build controller
// owns its fields).
type buildNodes struct {
	ID   store.KeyField  `pk:"bnodes" cbor:"-"`
	ETag store.ETagField `etag:"true" cbor:"-"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *buildNodes) Draft() *buildNodes {
	return &buildNodes{
		ID:   r.ID,
		ETag: r.ETag,
	}
}

// builds deletes terminal builds older than cutoff, oldest page first: bnodes, then the build
// record, then the page entry becomes a tombstone (record before index, R-SRV-101).
func (x *Retention) builds(ctx context.Context, cutoff time.Time, st *retentionState) (int, error) {
	projects, err := x.Projects(ctx)
	if err != nil {
		return 0, fmt.Errorf("retention projects: %w", err)
	}
	if st.Pages == nil {
		st.Pages = map[string]uint64{}
	}
	n := 0
	var errs []error
	for _, p := range projects {
		idx := store.BuildPages(x.Control, p)
		k := st.Pages[p]
	pages:
		for ; ; k++ {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			page, err := idx.Page(ctx, k)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				errs = append(errs, err)
				break
			}
			live := false
			for off, e := range page {
				if e.Build == "" || e.State == BuildEntryTombstone {
					continue
				}
				if !e.Created.Before(cutoff) {
					live = true
					break pages // entries are in creation order: the rest is younger
				}
				gone, err := x.dropBuild(ctx, e.Build, cutoff)
				if err != nil {
					errs = append(errs, err)
					live = true
					continue
				}
				if !gone {
					live = true // not terminal yet (or younger by its own times)
					continue
				}
				if err := idx.Set(ctx, k*uint64(idx.P)+uint64(off), store.BuildEntry{N: e.N, State: BuildEntryTombstone}); err != nil {
					errs = append(errs, err)
					live = true
					continue
				}
				n++
			}
			if live {
				break
			}
			// every entry of a full page is gone: the next pass starts after it
			if slices.ContainsFunc(page, func(e store.BuildEntry) bool { return e.State != BuildEntryTombstone }) {
				break // the last page: entries still to come
			}
			st.Pages[p] = k + 1
		}
	}
	return n, errors.Join(errs...)
}

// dropBuild deletes bnodes/<id> and build/<id> when the build is terminal and older than
// cutoff. gone is true when the record no longer exists.
func (x *Retention) dropBuild(ctx context.Context, id string, cutoff time.Time) (gone bool, err error) {
	rec, err := store.Get(ctx, x.Control, &store.BuildRecord{ID: store.ID(id)})
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !expired(rec, cutoff) {
		return false, nil
	}
	if err := store.Change(ctx, x.Control, &buildNodes{ID: store.ID(id)}, func(*buildNodes) (*buildNodes, error) { return nil, nil }); err != nil {
		return false, err
	}
	left, err := store.CAS(ctx, x.Control, &store.BuildRecord{ID: store.ID(id)}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		if cur != nil && !expired(cur, cutoff) {
			return nil, store.ErrNoChange
		}
		return nil, nil
	})
	return err == nil && left == nil, err
}

func expired(b *store.BuildRecord, cutoff time.Time) bool {
	if !TerminalBuild(b.State.Value()) {
		return false
	}
	t := b.Finished.Value()
	if t.IsZero() {
		t = b.Created.Value()
	}
	return t.Before(cutoff)
}

// queues deletes qs slots below head − 1000 of every class in fleet/cfg.c.
func (x *Retention) queues(ctx context.Context, st *retentionState) (int, error) {
	fc, err := store.Get(ctx, x.Fleet, store.FleetConfigKey())
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if st.Queues == nil {
		st.Queues = map[string]uint64{}
	}
	n := 0
	var errs []error
	for _, c := range fc.Classes.Value() {
		head, err := store.Queue(x.Fleet, c.ID).Head(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if head <= slotKeep+1 {
			continue
		}
		from := max(st.Queues[c.ID], 1)
		k, next, err := deleteSlots(ctx, from, head-slotKeep, func(seq uint64) error {
			err := store.Change(ctx, x.Fleet, store.QueueSlotKey(c.ID, seq), func(*store.QueueSlot) (*store.QueueSlot, error) { return nil, nil })
			return err
		})
		st.Queues[c.ID] = next
		n += k
		if err != nil {
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

// registrations deletes wrs/reg slots below the registrar cursor − 1000.
func (x *Retention) registrations(ctx context.Context, st *retentionState) (int, error) {
	l, err := store.Get(ctx, x.Fleet, &store.ControllerLease{Name: store.ID(CtlRegistrar)})
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cur, _ := strconv.ParseUint(string(l.Cursor.Value()), 10, 64)
	if cur <= slotKeep+1 {
		return 0, nil
	}
	k, next, err := deleteSlots(ctx, max(st.Reg, 1), cur-slotKeep, func(seq uint64) error {
		s, _ := store.Seq20(seq)
		err := store.Change(ctx, x.Fleet, &store.RegSlot{Log: store.ID("reg"), Seq: store.ID(s)}, func(*store.RegSlot) (*store.RegSlot, error) { return nil, nil })
		return err
	})
	st.Reg = next
	return k, err
}

// deleteSlots deletes slots [from, below) (at most maxSlotDeletes) and returns how many it
// handled and the first slot not yet deleted.
func deleteSlots(ctx context.Context, from, below uint64, del func(uint64) error) (int, uint64, error) {
	n := 0
	seq := from
	for ; seq < below && n < maxSlotDeletes; seq++ {
		if ctx.Err() != nil {
			return n, seq, ctx.Err()
		}
		if err := del(seq); err != nil {
			return n, seq, err
		}
		n++
	}
	return n, seq, nil
}

// Controller returns the retention controller (hourly, lease ctl/retention).
func (x *Retention) Controller(owner string, skew time.Duration, now func() time.Time) *Controller {
	return &Controller{ID: CtlRetention, Fleet: x.Fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: now, Persist: true, Pass: x.Pass, Log: x.Log}
}
