package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// The primitives (SPEC §05.3). Each keeps its state in records on known keys and reads them
// with dynamitedb.Get; none of them lists. A primitive is generic over the record type of its
// documents; the record kind (its struct type and therefore its pk/sk names) comes in through
// builder functions that return fresh values (store.ID keys, store.Set data) and accessor
// functions that read fetched ones. The key value of a document is its builder's result with
// only the key fields kept.

// Counter is a record kind that holds one number: a sequence-log hint (`SeqHint`: n).
// It performs no I/O until used.
type Counter struct {
	key   func() (string, error)
	read  func(ctx context.Context, cached bool) (uint64, error)
	raise func(ctx context.Context, n uint64, attempts int) error
}

// NewCounter describes a counter record kind: build returns the fresh value of the record
// holding n (key + Set(n)), n reads a fetched one.
//
// Example:
//
//	tail := store.NewCounter(fleet, func(n uint64) *QueueTail {
//		return &QueueTail{Class: store.ID(classID), N: store.Set(n)}
//	}, func(h *QueueTail) uint64 { return h.N.Value() })
func NewCounter[H any](b *Bucket, build func(n uint64) *H, n func(h *H) uint64) *Counter {
	c := &Counter{}
	cache := &Cache[H]{}
	key := func() *H { return keyOnly(build(0)) }
	c.key = func() (string, error) { return objectKey(key()) }
	c.read = func(ctx context.Context, cached bool) (uint64, error) {
		if err := checkBucket(b); err != nil {
			return 0, err
		}
		var cc *Cache[H]
		if cached {
			cc = cache
		}
		h, _, k, err := getCached(ctx, b, cc, key())
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, wrap(k, err)
		}
		return n(h), nil
	}
	c.raise = func(ctx context.Context, to uint64, attempts int) error {
		if err := checkBucket(b); err != nil {
			return err
		}
		k, err := objectKey(key())
		if err != nil {
			return err
		}
		_, _, err = cas(ctx, b, key(), attempts, func(cur *H) (*H, error) {
			if cur != nil && n(cur) >= to {
				return nil, ErrNoChange
			}
			return build(to), nil
		})
		return wrap(k, err)
	}
	return c
}

// maxProbe bounds the slot numbers one Append tries after the tail hint. The hint lags the
// real tail by at most the number of concurrent appenders, so this is never reached in a
// healthy log; it turns a broken hint into an error instead of an endless loop.
const maxProbe = 100_000

// SeqLog is a dense append-only log of slots on known keys. Readers find new entries by
// GETting the next slot number; a 404 means "nothing new". It never lists.
type SeqLog[S any] struct {
	Bucket *Bucket
	slot   func(seq uint64) *S
	tail   *Counter
	head   *Counter // nil: the log has no head hint
}

// NewSeqLog returns the log; it performs no I/O. slot returns the key value of slot seq
// (seq ≥ 1); tail and head are the hint records (head may be nil).
//
// Example:
//
//	q := store.NewSeqLog(fleet, func(seq uint64) *QueueSlot { return QueueSlotKey(classID, seq) }, tail, head)
//	seq, err := q.Append(ctx, &QueueSlot{Job: store.Set(jobID)})
func NewSeqLog[S any](b *Bucket, slot func(seq uint64) *S, tail, head *Counter) *SeqLog[S] {
	return &SeqLog[S]{Bucket: b, slot: slot, tail: tail, head: head}
}

func (l *SeqLog[S]) slotKey(seq uint64) (*S, error) {
	if seq == 0 {
		k, _ := objectKey(l.slot(1))
		return nil, fmt.Errorf("store: %s: slot numbers start at 1", k)
	}
	return l.slot(seq), nil
}

// Append reads the tail hint t (1 GET), Creates slot t, t+1, … until one succeeds, then raises
// the tail hint to seq+1 with a best-effort CAS (a conflict is ignored). Returns the slot
// number. v is a fresh value without keys; Append sets the slot's keys.
func (l *SeqLog[S]) Append(ctx context.Context, v *S) (seq uint64, err error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) {
		if err := checkBucket(l.Bucket); err != nil {
			return 0, err
		}
		if v == nil {
			return 0, fmt.Errorf("store: %w: nil slot value", ErrPanic)
		}
		tail, err := l.tail.read(ctx, false)
		if err != nil {
			return 0, err
		}
		seq = max(tail, 1)
		for ; ; seq++ {
			if seq-max(tail, 1) >= maxProbe {
				return 0, fmt.Errorf("store: no free slot within %d of the tail hint %d", maxProbe, tail)
			}
			k, err := write(ctx, l.Bucket, withKey(v, l.slot(seq)), createOnly, nil)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrExists) {
				return 0, wrap(k, err)
			}
		}
		// Best effort: a conflict only means another appender moved the hint.
		_ = l.tail.raise(ctx, seq+1, 1)
		return seq, nil
	})
}

// Read GETs one slot. ErrNotFound means seq is at or past the end of the log.
func (l *SeqLog[S]) Read(ctx context.Context, seq uint64) (*S, error) {
	return Call(ctx, func(ctx context.Context) (*S, error) {
		if err := checkBucket(l.Bucket); err != nil {
			return nil, err
		}
		key, err := l.slotKey(seq)
		if err != nil {
			return nil, err
		}
		v, k, err := get(ctx, l.Bucket, key)
		return v, wrap(k, err)
	})
}

// Tail returns the tail hint (GetCached; 1 request when unchanged). 0 when no hint exists.
func (l *SeqLog[S]) Tail(ctx context.Context) (uint64, error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) { return l.tail.read(ctx, true) })
}

// Head returns the head hint (GetCached; 1 request when unchanged). 0 when no hint exists.
func (l *SeqLog[S]) Head(ctx context.Context) (uint64, error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) {
		if l.head == nil {
			return 0, errors.New("store: this sequence log has no head hint")
		}
		return l.head.read(ctx, true)
	})
}

// AdvanceHead raises the head hint to n if it is lower (CAS, monotonic; never lowers it).
func (l *SeqLog[S]) AdvanceHead(ctx context.Context, n uint64) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if l.head == nil {
			return struct{}{}, errors.New("store: this sequence log has no head hint")
		}
		return struct{}{}, l.head.raise(ctx, n, casAttempts)
	})
	return err
}

// PagedIndex is a list of entries numbered 0,1,2,… stored P per page record. The entry count
// is a counter field in a record the caller names (e.g. ProjectCounters.Builds). Page k
// holds entries [k*P, (k+1)*P).
type PagedIndex[E any] struct {
	Bucket *Bucket
	P      int // entries per page, 50 unless stated

	set  func(ctx context.Context, k uint64, off int, e E) error
	page func(ctx context.Context, k uint64) ([]E, error)
}

// maxPage bounds P: a page document must stay a small record.
const maxPage = 1000

// NewPagedIndex performs no I/O. build returns the fresh value of page k holding entries e
// (keys + Set(e)); entries reads a fetched page.
//
// Example:
//
//	idx := store.NewPagedIndex(control, 50, func(k uint64, e []BuildEntry) *BuildPage {
//		n, _ := store.Num10(k)
//		return &BuildPage{Project: store.ID(projectID), Page: store.ID(n), Entries: store.Set(e)}
//	}, func(p *BuildPage) []BuildEntry { return p.Entries.Value() })
//	page, err := idx.Page(ctx, idx.LastPage(count))
func NewPagedIndex[P, E any](b *Bucket, p int, build func(k uint64, e []E) *P, entries func(*P) []E) *PagedIndex[E] {
	x := &PagedIndex[E]{Bucket: b, P: p}
	cache := &Cache[P]{}
	x.set = func(ctx context.Context, k uint64, off int, e E) error {
		key := keyOnly(build(k, nil))
		_, _, err := cas(ctx, b, key, docAttempts, func(cur *P) (*P, error) {
			var es []E
			if cur != nil {
				es = slices.Clone(entries(cur))
			}
			for len(es) <= off {
				var zero E
				es = append(es, zero)
			}
			es[off] = e
			return build(k, es), nil
		})
		return err
	}
	x.page = func(ctx context.Context, k uint64) ([]E, error) {
		v, _, key, err := getCached(ctx, b, cache, keyOnly(build(k, nil)))
		if err != nil {
			return nil, wrap(key, err)
		}
		out := make([]E, x.P)
		copy(out, entries(v))
		return out, nil
	}
	return x
}

func (x *PagedIndex[E]) check() error {
	if err := checkBucket(x.Bucket); err != nil {
		return err
	}
	if x.P <= 0 || x.P > maxPage {
		return fmt.Errorf("store: page size %d out of range 1..%d", x.P, maxPage)
	}
	return nil
}

// Set writes entry n (CAS on its page; creates the page on first use).
func (x *PagedIndex[E]) Set(ctx context.Context, n uint64, e E) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := x.check(); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, x.set(ctx, n/uint64(x.P), int(n%uint64(x.P)), e)
	})
	return err
}

// Page returns page k (GetCached). Missing entries in a page are zero values; the result has
// P entries. A page that was never written is ErrNotFound.
func (x *PagedIndex[E]) Page(ctx context.Context, k uint64) ([]E, error) {
	return Call(ctx, func(ctx context.Context) ([]E, error) {
		if err := x.check(); err != nil {
			return nil, err
		}
		return x.page(ctx, k)
	})
}

// LastPage returns the page number of entry count-1.
func (x *PagedIndex[E]) LastPage(count uint64) uint64 {
	if count == 0 || x.P <= 0 {
		return 0
	}
	return (count - 1) / uint64(x.P)
}

// ShardSet is a set of string ids spread over N shard records <pk>/<sh2>, each a bounded
// map id -> E (≤ 1,000 entries). Reconcilers read all N shards with GetCached each pass,
// so an unchanged set costs N × 304.
type ShardSet[E any] struct {
	Bucket *Bucket
	N      int // shard count, from FleetConfig

	update func(ctx context.Context, sh string, fn func(map[string]E) (map[string]E, error)) error
	shard  func(ctx context.Context, sh string) (map[string]E, error)
}

// MaxShardEntries is the bound of one shard document.
const MaxShardEntries = 1000

// ErrShardFull is returned by Add when the id's shard already holds MaxShardEntries ids; the
// shard count (FleetConfig) must grow.
var ErrShardFull = errors.New("shard full")

// NewShardSet performs no I/O. build returns the fresh value of shard sh (its sh2 segment)
// holding m (key + Set(m)); entries reads a fetched shard.
//
// Example:
//
//	active := store.NewShardSet(fleet, cfg.JobShards, func(sh string, m map[string]ActiveJob) *ActiveJobShard {
//		return &ActiveJobShard{Shard: store.ID(sh), Jobs: store.Set(m)}
//	}, func(s *ActiveJobShard) map[string]ActiveJob { return s.Jobs.Value() })
//	err := active.Add(ctx, jobID, ActiveJob{Build: buildID})
func NewShardSet[S, E any](b *Bucket, n int, build func(sh string, m map[string]E) *S, entries func(*S) map[string]E) *ShardSet[E] {
	s := &ShardSet[E]{Bucket: b, N: n}
	cache := &Cache[S]{}
	s.update = func(ctx context.Context, sh string, fn func(map[string]E) (map[string]E, error)) error {
		key := keyOnly(build(sh, nil))
		k, err := objectKey(key)
		if err != nil {
			return err
		}
		_, _, err = cas(ctx, b, key, docAttempts, func(cur *S) (*S, error) {
			var m map[string]E
			if cur != nil {
				m = maps.Clone(entries(cur))
			}
			m, err := fn(m)
			if err != nil {
				return nil, err
			}
			return build(sh, m), nil
		})
		return wrap(k, err)
	}
	s.shard = func(ctx context.Context, sh string) (map[string]E, error) {
		v, _, k, err := getCached(ctx, b, cache, keyOnly(build(sh, nil)))
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(k, err)
		}
		return entries(v), nil
	}
	return s
}

// Add adds id (CAS on shard sh2(id)).
func (s *ShardSet[E]) Add(ctx context.Context, id string, e E) error {
	return s.change(ctx, id, func(m map[string]E) (map[string]E, error) {
		if _, ok := m[id]; !ok && len(m) >= MaxShardEntries {
			return nil, ErrShardFull
		}
		if m == nil {
			m = map[string]E{}
		}
		m[id] = e
		return m, nil
	})
}

// Remove removes id (CAS; absent = nil).
func (s *ShardSet[E]) Remove(ctx context.Context, id string) error {
	return s.change(ctx, id, func(m map[string]E) (map[string]E, error) {
		if _, ok := m[id]; !ok {
			return nil, ErrNoChange
		}
		delete(m, id)
		return m, nil
	})
}

func (s *ShardSet[E]) change(ctx context.Context, id string, fn func(map[string]E) (map[string]E, error)) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(s.Bucket); err != nil {
			return struct{}{}, err
		}
		sh, err := Sh2(id, s.N)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, s.update(ctx, sh, fn)
	})
	return err
}

// All reads every shard (N × GetCached).
func (s *ShardSet[E]) All(ctx context.Context) (map[string]E, error) {
	return Call(ctx, func(ctx context.Context) (map[string]E, error) {
		if err := checkBucket(s.Bucket); err != nil {
			return nil, err
		}
		if s.N < 1 || s.N > 100 {
			return nil, fmt.Errorf("store: shard count %d out of range 1..100", s.N)
		}
		out := map[string]E{}
		for i := range s.N {
			m, err := s.shard(ctx, fmt.Sprintf("%02d", i))
			if err != nil {
				return nil, err
			}
			maps.Copy(out, m)
		}
		return out, nil
	})
}

// LeaseSkew is the clock skew allowance of a takeover (fleet.skew).
const LeaseSkew = 5 * time.Second

// LeaseState is the data of a lease record: owner, epoch (incremented on every takeover),
// expires, and a cursor that belongs to the lease holder (e.g. the registrar's sequence
// number); the lease keeps the cursor as it is.
type LeaseState struct {
	Owner   string
	Epoch   uint64
	Expires time.Time
	Cursor  []byte
}

// Lease is a CAS-held lease on one record. Used for controller leadership (ctl/<name>) and
// the per-job promotion lock (plock/<jobID>).
type Lease struct {
	Bucket *Bucket
	Owner  string        // this process's orchestrator id
	TTL    time.Duration // 3 × controller interval

	key     func() (string, error)
	acquire func(ctx context.Context) (bool, uint64, error)
	release func(ctx context.Context) error
}

// NewLease performs no I/O. build returns the fresh value of the lease record holding s
// (key + Set of s's fields); state reads a fetched one.
//
// Example:
//
//	l := store.NewControllerLease(fleet, "job-reconciler", self.ID, 30*time.Second)
//	if held, epoch, err := l.TryAcquire(ctx); held { reconcile(ctx, epoch) }
func NewLease[L any](b *Bucket, build func(s LeaseState) *L, state func(*L) LeaseState, owner string, ttl time.Duration) *Lease {
	l := &Lease{Bucket: b, Owner: owner, TTL: ttl}
	key := func() *L { return keyOnly(build(LeaseState{})) }
	l.key = func() (string, error) { return objectKey(key()) }
	l.acquire = func(ctx context.Context) (bool, uint64, error) {
		cur, k, err := get(ctx, b, key())
		if err != nil && !errors.Is(err, ErrNotFound) {
			return false, 0, wrap(k, err)
		}
		now := time.Now()
		next := LeaseState{Owner: l.Owner, Epoch: 1, Expires: now.Add(l.TTL)}
		if cur != nil {
			s := state(cur)
			switch {
			case s.Expires.Add(LeaseSkew).Before(now):
				next.Epoch, next.Cursor = s.Epoch+1, s.Cursor
			case s.Owner == l.Owner:
				next.Epoch, next.Cursor = s.Epoch, s.Cursor
			default:
				return false, s.Epoch, nil
			}
		}
		if cur == nil {
			_, err = write(ctx, b, build(next), createOnly, nil)
		} else {
			_, err = write(ctx, b, build(next), ifMatch, etagOf(cur))
		}
		switch {
		case errors.Is(err, ErrExists), errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
			return false, 0, nil // somebody else wrote first
		case err != nil:
			return false, 0, wrap(k, err)
		}
		return true, next.Epoch, nil
	}
	l.release = func(ctx context.Context) error {
		k, err := objectKey(key())
		if err != nil {
			return err
		}
		_, _, err = cas(ctx, b, key(), casAttempts, func(cur *L) (*L, error) {
			if cur == nil {
				return nil, ErrNoChange
			}
			s := state(cur)
			if s.Owner != l.Owner {
				return nil, ErrNoChange
			}
			s.Expires = time.Now().Add(-LeaseSkew)
			return build(s), nil
		})
		return wrap(k, err)
	}
	return l
}

func (l *Lease) check() error {
	if err := checkBucket(l.Bucket); err != nil {
		return err
	}
	k, err := l.key()
	if err != nil {
		return err
	}
	if l.Owner == "" || l.TTL <= 0 {
		return fmt.Errorf("store: lease %s needs an owner and a positive TTL", k)
	}
	return nil
}

// TryAcquire creates the lease if absent, renews it if owned, or takes it over (epoch+1) when
// expires + skew < now. 1 GET + at most 1 PUT. A lease that lapsed is taken over with a new
// epoch even by its last owner, because another orchestrator may have acted on it meanwhile.
// When held is false, epoch is the current holder's (0 when another writer just won).
func (l *Lease) TryAcquire(ctx context.Context) (held bool, epoch uint64, err error) {
	type res struct {
		held  bool
		epoch uint64
	}
	r, err := Call(ctx, func(ctx context.Context) (res, error) {
		if err := l.check(); err != nil {
			return res{}, err
		}
		held, epoch, err := l.acquire(ctx)
		return res{held, epoch}, err
	})
	return r.held, r.epoch, err
}

// Release gives up the lease (expires = now - skew, CAS) so another orchestrator can take it
// at once (§05.6.5 shutdown). Releasing a lease held by somebody else is a no-op.
func (l *Lease) Release(ctx context.Context) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := l.check(); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, l.release(ctx)
	})
	return err
}
