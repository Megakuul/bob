package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// The primitives (SPEC §05.3). Each keeps its state in documents on known keys and reads
// them with GET; none of them lists.

// seqHint is the tail or head hint of a sequence log (`SeqHint`: n).
type seqHint struct {
	N uint64 `cbor:"n,omitempty"`
}

// maxProbe bounds the slot numbers one Append tries after the tail hint. The hint lags the
// real tail by at most the number of concurrent appenders, so this is never reached in a
// healthy log; it turns a broken hint into an error instead of an endless loop.
const maxProbe = 100_000

// SeqLog is a dense append-only log of slots on known keys. Readers find new entries by
// GETting the next slot number; a 404 means "nothing new". It never lists.
type SeqLog[T any] struct {
	Bucket *Bucket
	SlotPK string // e.g. "qs"
	TailPK string // e.g. "qt"
	HeadPK string // e.g. "qh"
	Name   string // e.g. "x86_64-linux.k3j9d0a1b2"

	hints *Cache[seqHint]
}

// NewSeqLog returns the log; it performs no I/O.
//
// Example:
//
//	q := store.NewSeqLog[QueueSlot](fleet, "qs", "qt", "qh", "x86_64-linux.k3j9d0a1b2")
//	seq, err := q.Append(ctx, &QueueSlot{Job: jobID})
func NewSeqLog[T any](b *Bucket, slotPK, tailPK, headPK, name string) *SeqLog[T] {
	return &SeqLog[T]{Bucket: b, SlotPK: slotPK, TailPK: tailPK, HeadPK: headPK, Name: name, hints: &Cache[seqHint]{}}
}

func (l *SeqLog[T]) slotKey(seq uint64) (string, error) {
	if seq == 0 {
		return "", fmt.Errorf("store: %s/%s: slot numbers start at 1", l.SlotPK, l.Name)
	}
	s, _ := Seq20(seq)
	return Key(l.SlotPK, l.Name, "s", s)
}

// Append reads the tail hint t (1 GET), Creates slot t, t+1, … until one succeeds, then raises
// the tail hint to seq+1 with a best-effort CAS (a conflict is ignored). Returns the slot number.
func (l *SeqLog[T]) Append(ctx context.Context, v *T) (seq uint64, err error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) {
		if err := checkBucket(l.Bucket); err != nil {
			return 0, err
		}
		if v == nil {
			return 0, misuse("nil slot value")
		}
		body, err := encode(v)
		if err != nil {
			return 0, fmt.Errorf("store: %s/%s: %w", l.SlotPK, l.Name, err)
		}
		tail, err := l.hint(ctx, l.TailPK, nil)
		if err != nil {
			return 0, err
		}
		seq = max(tail, 1)
		for ; ; seq++ {
			if seq-max(tail, 1) >= maxProbe {
				return 0, fmt.Errorf("store: %s/%s: no free slot within %d of the tail hint %d", l.SlotPK, l.Name, maxProbe, tail)
			}
			k, err := l.slotKey(seq)
			if err != nil {
				return 0, err
			}
			_, err = l.Bucket.put(ctx, k, body, ifAbsent, "")
			if err == nil {
				break
			}
			if !errors.Is(err, errPrecondition) {
				return 0, wrap(k, err)
			}
		}
		// Best effort: a conflict only means another appender moved the hint.
		_ = l.raise(ctx, l.TailPK, seq+1, 1)
		return seq, nil
	})
}

// Read GETs one slot. ErrNotFound means seq is at or past the end of the log.
func (l *SeqLog[T]) Read(ctx context.Context, seq uint64) (*T, error) {
	return Call(ctx, func(ctx context.Context) (*T, error) {
		if err := checkBucket(l.Bucket); err != nil {
			return nil, err
		}
		k, err := l.slotKey(seq)
		if err != nil {
			return nil, err
		}
		v, err := read[T](ctx, l.Bucket, k)
		return v, wrap(k, err)
	})
}

// Tail returns the tail hint (GetCached; 1 request when unchanged). 0 when no hint exists.
func (l *SeqLog[T]) Tail(ctx context.Context) (uint64, error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) { return l.hint(ctx, l.TailPK, l.hints) })
}

// Head returns the head hint (GetCached; 1 request when unchanged). 0 when no hint exists.
func (l *SeqLog[T]) Head(ctx context.Context) (uint64, error) {
	return Call(ctx, func(ctx context.Context) (uint64, error) { return l.hint(ctx, l.HeadPK, l.hints) })
}

// AdvanceHead raises the head hint to n if it is lower (CAS, monotonic; never lowers it).
func (l *SeqLog[T]) AdvanceHead(ctx context.Context, n uint64) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, l.raise(ctx, l.HeadPK, n, casAttempts)
	})
	return err
}

func (l *SeqLog[T]) hint(ctx context.Context, pk string, c *Cache[seqHint]) (uint64, error) {
	if err := checkBucket(l.Bucket); err != nil {
		return 0, err
	}
	k, err := Key(pk, l.Name)
	if err != nil {
		return 0, err
	}
	h, _, err := getCached(ctx, l.Bucket, c, k)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, wrap(k, err)
	}
	return h.N, nil
}

// raise sets the hint at pk to n unless it is already >= n, trying at most attempts times.
func (l *SeqLog[T]) raise(ctx context.Context, pk string, n uint64, attempts int) error {
	if err := checkBucket(l.Bucket); err != nil {
		return err
	}
	k, err := Key(pk, l.Name)
	if err != nil {
		return err
	}
	var last error
	for range attempts {
		_, last = casOnce(ctx, l.Bucket, k, func(cur *seqHint) (*seqHint, error) {
			if cur != nil && cur.N >= n {
				return nil, ErrNoChange
			}
			return &seqHint{N: n}, nil
		})
		if !errors.Is(last, errPrecondition) {
			break
		}
		if err := pause(ctx, 10*time.Millisecond, 200*time.Millisecond); err != nil {
			return err
		}
	}
	if errors.Is(last, errPrecondition) {
		last = ErrConflict
	}
	return wrap(k, last)
}

// PagedIndex is a list of entries numbered 0,1,2,… stored P per page document
// <pagePK>/<scope>/p/<num10>. The entry count is a counter field in a record the caller names
// (e.g. ProjectCounters.Builds). Page k holds entries [k*P, (k+1)*P).
type PagedIndex[E any] struct {
	Bucket *Bucket
	PagePK string // e.g. "bpage"
	Scope  string // e.g. projectID
	P      int    // entries per page, 50 unless stated

	pages *Cache[page[E]]
}

// page is one page document (`e`: the entries, index = offset in the page).
type page[E any] struct {
	E []E `cbor:"e,omitempty"`
}

// maxPage bounds P: a page document must stay a small record.
const maxPage = 1000

// NewPagedIndex performs no I/O.
//
// Example:
//
//	idx := store.NewPagedIndex[BuildEntry](control, "bpage", projectID, 50)
//	page, err := idx.Page(ctx, idx.LastPage(count))
func NewPagedIndex[E any](b *Bucket, pagePK, scope string, p int) *PagedIndex[E] {
	return &PagedIndex[E]{Bucket: b, PagePK: pagePK, Scope: scope, P: p, pages: &Cache[page[E]]{}}
}

func (x *PagedIndex[E]) pageKey(k uint64) (string, error) {
	if x.P <= 0 || x.P > maxPage {
		return "", fmt.Errorf("store: %s/%s: page size %d out of range 1..%d", x.PagePK, x.Scope, x.P, maxPage)
	}
	n, _ := Num10(k)
	return Key(x.PagePK, x.Scope, "p", n)
}

// Set writes entry n (CAS on its page; creates the page on first use).
func (x *PagedIndex[E]) Set(ctx context.Context, n uint64, e E) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(x.Bucket); err != nil {
			return struct{}{}, err
		}
		k, err := x.pageKey(n / uint64(max(x.P, 1)))
		if err != nil {
			return struct{}{}, err
		}
		off := int(n % uint64(x.P))
		_, err = cas(ctx, x.Bucket, k, docAttempts, func(cur *page[E]) (*page[E], error) {
			next := &page[E]{}
			if cur != nil {
				next.E = cur.E
			}
			for len(next.E) <= off {
				var zero E
				next.E = append(next.E, zero)
			}
			next.E[off] = e
			return next, nil
		})
		return struct{}{}, wrap(k, err)
	})
	return err
}

// Page returns page k (GetCached). Missing entries in a page are zero values; the result has
// P entries. A page that was never written is ErrNotFound.
func (x *PagedIndex[E]) Page(ctx context.Context, k uint64) ([]E, error) {
	return Call(ctx, func(ctx context.Context) ([]E, error) {
		if err := checkBucket(x.Bucket); err != nil {
			return nil, err
		}
		key, err := x.pageKey(k)
		if err != nil {
			return nil, err
		}
		p, _, err := getCached(ctx, x.Bucket, x.pages, key)
		if err != nil {
			return nil, wrap(key, err)
		}
		out := make([]E, x.P)
		copy(out, p.E)
		return out, nil
	})
}

// LastPage returns the page number of entry count-1.
func (x *PagedIndex[E]) LastPage(count uint64) uint64 {
	if count == 0 || x.P <= 0 {
		return 0
	}
	return (count - 1) / uint64(x.P)
}

// ShardSet is a set of string ids spread over N shard documents <pk>/<sh2>, each a bounded
// map id -> E (≤ 1,000 entries). Reconcilers read all N shards with GetCached each pass,
// so an unchanged set costs N × 304.
type ShardSet[E any] struct {
	Bucket *Bucket
	PK     string // e.g. "jact"
	N      int    // shard count, from FleetConfig

	shards *Cache[shard[E]]
}

// shard is one shard document (`m`: id -> entry).
type shard[E any] struct {
	M map[string]E `cbor:"m,omitempty"`
}

// MaxShardEntries is the bound of one shard document.
const MaxShardEntries = 1000

// ErrShardFull is returned by Add when the id's shard already holds MaxShardEntries ids; the
// shard count (FleetConfig) must grow.
var ErrShardFull = errors.New("shard full")

// NewShardSet performs no I/O.
//
// Example:
//
//	active := store.NewShardSet[ActiveJob](fleet, "jact", cfg.JobShards)
//	err := active.Add(ctx, jobID, ActiveJob{Build: buildID})
func NewShardSet[E any](b *Bucket, pk string, n int) *ShardSet[E] {
	return &ShardSet[E]{Bucket: b, PK: pk, N: n, shards: &Cache[shard[E]]{}}
}

func (s *ShardSet[E]) shardKey(id string) (string, error) {
	sh, err := Sh2(id, s.N)
	if err != nil {
		return "", err
	}
	return Key(s.PK, sh)
}

// Add adds id (CAS on shard sh2(id)).
func (s *ShardSet[E]) Add(ctx context.Context, id string, e E) error {
	return s.update(ctx, id, func(m map[string]E) (map[string]E, error) {
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
	return s.update(ctx, id, func(m map[string]E) (map[string]E, error) {
		if _, ok := m[id]; !ok {
			return nil, ErrNoChange
		}
		delete(m, id)
		return m, nil
	})
}

func (s *ShardSet[E]) update(ctx context.Context, id string, fn func(map[string]E) (map[string]E, error)) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(s.Bucket); err != nil {
			return struct{}{}, err
		}
		k, err := s.shardKey(id)
		if err != nil {
			return struct{}{}, err
		}
		_, err = cas(ctx, s.Bucket, k, docAttempts, func(cur *shard[E]) (*shard[E], error) {
			var m map[string]E
			if cur != nil {
				m = cur.M
			}
			m, err := fn(m)
			if err != nil {
				return nil, err
			}
			return &shard[E]{M: m}, nil
		})
		return struct{}{}, wrap(k, err)
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
			return nil, fmt.Errorf("store: %s: shard count %d out of range 1..100", s.PK, s.N)
		}
		out := map[string]E{}
		for i := range s.N {
			k, err := Key(s.PK, fmt.Sprintf("%02d", i))
			if err != nil {
				return nil, err
			}
			sh, _, err := getCached(ctx, s.Bucket, s.shards, k)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, wrap(k, err)
			}
			for id, e := range sh.M {
				out[id] = e
			}
		}
		return out, nil
	})
}

// LeaseSkew is the clock skew allowance of a takeover (fleet.skew).
const LeaseSkew = 5 * time.Second

// Lease is a CAS-held lease on one record: owner, epoch (incremented on every takeover),
// expires. Used for controller leadership (ctl/<name>).
type Lease struct {
	Bucket *Bucket
	Key    string        // e.g. "ctl/job-reconciler"
	Owner  string        // this process's orchestrator id
	TTL    time.Duration // 3 × controller interval
}

// leaseDoc is the `ControllerLease` record. Cursor belongs to the controller; the lease
// keeps it as it is.
type leaseDoc struct {
	Owner   string          `cbor:"o,omitempty"`
	Epoch   uint64          `cbor:"ep,omitempty"`
	Expires time.Time       `cbor:"ex,omitempty"`
	Cursor  cbor.RawMessage `cbor:"cu,omitempty"`
}

// NewLease performs no I/O.
//
// Example:
//
//	l := store.NewLease(fleet, "ctl/job-reconciler", self.ID, 30*time.Second)
//	if held, epoch, err := l.TryAcquire(ctx); held { reconcile(ctx, epoch) }
func NewLease(b *Bucket, key, owner string, ttl time.Duration) *Lease {
	return &Lease{Bucket: b, Key: key, Owner: owner, TTL: ttl}
}

// TryAcquire creates the lease if absent, renews it if owned, or takes it over (epoch+1) when
// expires + skew < now. 1 GET + at most 1 PUT. A lease that lapsed is taken over with a new
// epoch even by its last owner, because another orchestrator may have acted on it meanwhile.
// When held is false, epoch is the current holder's.
func (l *Lease) TryAcquire(ctx context.Context) (held bool, epoch uint64, err error) {
	type res struct {
		held  bool
		epoch uint64
	}
	r, err := Call(ctx, func(ctx context.Context) (res, error) {
		if err := checkBucket(l.Bucket); err != nil {
			return res{}, err
		}
		if l.Owner == "" || l.TTL <= 0 {
			return res{}, fmt.Errorf("store: lease %s needs an owner and a positive TTL", l.Key)
		}
		if err := validKey(l.Key); err != nil {
			return res{}, err
		}
		var other res
		got, err := casOnce(ctx, l.Bucket, l.Key, func(cur *leaseDoc) (*leaseDoc, error) {
			now := time.Now()
			next := &leaseDoc{Owner: l.Owner, Epoch: 1, Expires: now.Add(l.TTL)}
			switch {
			case cur == nil:
			case cur.Expires.Add(LeaseSkew).Before(now):
				next.Epoch, next.Cursor = cur.Epoch+1, cur.Cursor
			case cur.Owner == l.Owner:
				next.Epoch, next.Cursor = cur.Epoch, cur.Cursor
			default:
				other = res{false, cur.Epoch}
				return nil, ErrNoChange
			}
			return next, nil
		})
		switch {
		case errors.Is(err, errPrecondition):
			return res{}, nil // somebody else wrote first
		case err != nil:
			return res{}, wrap(l.Key, err)
		case got == nil || got.Owner != l.Owner:
			return other, nil
		}
		return res{true, got.Epoch}, nil
	})
	return r.held, r.epoch, err
}

// validKey checks a full key <pk>/<pkval>[/<sk>/<skval>].
func validKey(key string) error {
	parts := strings.Split(key, "/")
	if len(parts) != 2 && len(parts) != 4 {
		return fmt.Errorf("store: key %q must be <pk>/<pkval> or <pk>/<pkval>/<sk>/<skval>", key)
	}
	_, err := Key(parts[0], parts[1], parts[2:]...)
	return err
}
