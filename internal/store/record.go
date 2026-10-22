package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Errors. All are wrapped with the key, e.g. "store: job/01J…: not found".
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("changed concurrently")
	ErrPanic    = errors.New("dynamitedb panicked")
	ErrNoChange = errors.New("no change") // returned by a CAS func to skip the write
)

// panics is the metric bob_store_panics_total of this process.
var panics atomic.Int64

// Panics is the value of the metric bob_store_panics_total of this process.
func Panics() int64 { return panics.Load() }

// Call runs fn and converts a panic into ErrPanic (R-STORE-005). Every dynamitedb call of the
// package goes through it; it is exported so the harness can feed it each misuse panic.
// The panic is logged with its stack and counted in bob_store_panics_total.
func Call[T any](ctx context.Context, fn func(context.Context) (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			panics.Add(1)
			slog.Error("store: recovered panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("store: %w: %v", ErrPanic, r)
		}
	}()
	return fn(ctx)
}

// Record encoding: dynamitedb's (CBOR, RFC 3339 times), with sorted map keys so that a record
// encodes to the same bytes every time (the lost-response rule compares bytes).
var (
	encMode, _ = cbor.EncOptions{Time: cbor.TimeRFC3339Nano, Sort: cbor.SortCoreDeterministic}.EncMode()
	decMode, _ = cbor.DecOptions{MaxArrayElements: 10000, MaxMapPairs: 5000, MaxNestedLevels: 16}.DecMode()
)

func encode(v any) ([]byte, error) { return encMode.Marshal(v) }

func decode[T any](body []byte) (*T, error) {
	v := new(T)
	if err := decMode.Unmarshal(body, v); err != nil {
		return nil, fmt.Errorf("decode record: %w", err)
	}
	return v, nil
}

// Records are Go structs whose key fields are strings tagged `pk:"<name>"` (and optionally
// `sk:"<name>"`), with an optional string field tagged `etag:"true"` that carries the ETag of
// the last read, and data fields with short CBOR names and omitempty (R-STORE-103 rule 1).
//
//	type JobRecord struct {
//		ID   string `pk:"job" cbor:"-"`
//		ETag string `etag:"true" cbor:"-"`
//		State string `cbor:"st,omitempty"`
//	}
//
// Misusing a record type (nil model, a T that is not a struct, a missing or non-string key
// field) is reported as ErrPanic: these are exactly the cases where dynamitedb panics (S9 §3.6).

type model struct {
	v              reflect.Value // the struct
	pk, sk, etag   int           // field indexes, -1 when absent
	pkName, skName string
}

func misuse(format string, args ...any) error {
	return fmt.Errorf("store: %w: %s", ErrPanic, fmt.Sprintf(format, args...))
}

func modelOf[T any](p *T) (model, error) {
	if p == nil {
		return model{}, misuse("nil model")
	}
	v := reflect.ValueOf(p).Elem()
	if v.Kind() != reflect.Struct {
		return model{}, misuse("model must be a struct, got %s", v.Type())
	}
	m := model{v: v, pk: -1, sk: -1, etag: -1}
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		for _, tag := range []struct {
			name string
			idx  *int
		}{{"pk", &m.pk}, {"sk", &m.sk}, {"etag", &m.etag}} {
			val, ok := f.Tag.Lookup(tag.name)
			if !ok || *tag.idx >= 0 {
				continue
			}
			if tag.name == "etag" {
				if b, _ := strconv.ParseBool(val); !b {
					continue
				}
			}
			if f.Type.Kind() != reflect.String {
				return model{}, misuse("%s field %s of %s must be a string", tag.name, f.Name, t)
			}
			*tag.idx = i
			switch tag.name {
			case "pk":
				m.pkName = val
			case "sk":
				m.skName = val
			}
		}
	}
	if m.pk < 0 {
		return model{}, misuse("%s has no pk field", t)
	}
	return m, nil
}

func (m model) key() (string, error) {
	if m.sk < 0 {
		return Key(m.pkName, m.v.Field(m.pk).String())
	}
	return Key(m.pkName, m.v.Field(m.pk).String(), m.skName, m.v.Field(m.sk).String())
}

// fill sets the key fields from key and the ETag field after a read or write.
func (m model) fill(key, etag string) {
	parts := strings.Split(key, "/")
	m.v.Field(m.pk).SetString(parts[1])
	if m.sk >= 0 && len(parts) == 4 {
		m.v.Field(m.sk).SetString(parts[3])
	}
	m.setETag(etag)
}

func (m model) setETag(etag string) {
	if m.etag >= 0 {
		m.v.Field(m.etag).SetString(etag)
	}
}

func (m model) etagValue() string {
	if m.etag < 0 {
		return ""
	}
	return m.v.Field(m.etag).String()
}

func keyOf[T any](p *T) (model, string, error) {
	m, err := modelOf(p)
	if err != nil {
		return m, "", err
	}
	k, err := m.key()
	return m, k, err
}

func wrap(key string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s: %w", key, err)
}

func checkBucket(b *Bucket) error {
	if b == nil || b.s3 == nil {
		return misuse("nil bucket")
	}
	return nil
}

// read GETs key and decodes it into a fresh T with key fields and ETag set.
func read[T any](ctx context.Context, b *Bucket, key string) (*T, error) {
	o, _, err := b.get(ctx, key, "")
	if err != nil {
		return nil, err
	}
	return decodeAt[T](o, key)
}

func decodeAt[T any](o object, key string) (*T, error) {
	v, err := decode[T](o.body)
	if err != nil {
		return nil, err
	}
	if m, ok := bound(v); ok {
		m.fill(key, o.etag)
	}
	return v, nil
}

// bound returns the record model of v; ok is false for the primitives' own documents, which
// have no key fields (their keys are computed from runtime names).
func bound[T any](v *T) (model, bool) {
	m, err := modelOf(v)
	return m, err == nil
}

// Get reads the record whose key fields are set in key. The result carries the ETag.
func Get[T any](ctx context.Context, b *Bucket, key *T) (*T, error) {
	return Call(ctx, func(ctx context.Context) (*T, error) {
		if err := checkBucket(b); err != nil {
			return nil, err
		}
		_, k, err := keyOf(key)
		if err != nil {
			return nil, err
		}
		v, err := read[T](ctx, b, k)
		return v, wrap(k, err)
	})
}

// Cache keeps the last value and ETag per key for GetCached. Zero value is ready to use.
// Values handed out are shallow copies: callers must not modify slices or maps in them.
type Cache[T any] struct {
	mu sync.Mutex
	m  map[string]cached[T]
}

type cached[T any] struct {
	v    T
	etag string
}

func (c *Cache[T]) lookup(key string) (cached[T], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

func (c *Cache[T]) put(key string, v *T, etag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cached[T]{}
	}
	c.m[key] = cached[T]{v: *v, etag: etag}
}

func (c *Cache[T]) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// getCached is GetCached on a key string; c may be nil (no caching).
func getCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key string) (*T, bool, error) {
	var have cached[T]
	var ok bool
	if c != nil {
		have, ok = c.lookup(key)
	}
	o, notModified, err := b.get(ctx, key, have.etag)
	switch {
	case errors.Is(err, ErrNotFound):
		if c != nil {
			c.drop(key)
		}
		return nil, false, err
	case err != nil:
		return nil, false, err
	case notModified && ok:
		v := have.v
		return &v, false, nil
	case notModified:
		return nil, false, fmt.Errorf("unexpected 304 without a cached value")
	}
	v, err := decodeAt[T](o, key)
	if err != nil {
		return nil, false, err
	}
	if c != nil {
		c.put(key, v, o.etag)
	}
	return v, true, nil
}

// GetCached sends GET If-None-Match with the cached ETag. On 304 it returns the cached value
// and changed=false (1 request). On 200 it reads the record (1 request: the 200 carries the
// body) and updates the cache. On 404 it drops the entry and returns ErrNotFound.
func GetCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (v *T, changed bool, err error) {
	type res struct {
		v       *T
		changed bool
	}
	r, err := Call(ctx, func(ctx context.Context) (res, error) {
		if err := checkBucket(b); err != nil {
			return res{}, err
		}
		_, k, err := keyOf(key)
		if err != nil {
			return res{}, err
		}
		v, changed, err := getCached(ctx, b, c, k)
		return res{v, changed}, wrap(k, err)
	})
	return r.v, r.changed, err
}

// Create writes v only if its key does not exist (PUT If-None-Match: *). On success v's ETag
// field holds the new ETag.
func Create[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, write(ctx, b, v, ifAbsent, "")
	})
	return err
}

// PutOwned writes v unconditionally. Only for records with exactly one legitimate writer
// (orch/<self>, whb/<self>).
func PutOwned[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, write(ctx, b, v, always, "")
	})
	return err
}

func write[T any](ctx context.Context, b *Bucket, v *T, c cond, etag string) error {
	if err := checkBucket(b); err != nil {
		return err
	}
	m, k, err := keyOf(v)
	if err != nil {
		return err
	}
	body, err := encode(v)
	if err != nil {
		return wrap(k, err)
	}
	tag, err := b.put(ctx, k, body, c, etag)
	switch {
	case errors.Is(err, errPrecondition) && c == ifAbsent:
		return wrap(k, ErrExists)
	case errors.Is(err, errPrecondition):
		return wrap(k, ErrConflict)
	case err != nil:
		return wrap(k, err)
	}
	m.setETag(tag)
	return nil
}

// Delete removes the record if its ETag still matches v's ETag (2 requests: a GET that
// checks the ETag, then DELETE If-Match). A missing record is ErrNotFound, a changed one
// ErrConflict.
func Delete[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(b); err != nil {
			return struct{}{}, err
		}
		m, k, err := keyOf(v)
		if err != nil {
			return struct{}{}, err
		}
		etag := m.etagValue()
		if etag == "" {
			return struct{}{}, wrap(k, errors.New("Delete needs the ETag of a read"))
		}
		cur, _, err := b.get(ctx, k, "")
		if err != nil {
			return struct{}{}, wrap(k, err)
		}
		if cur.etag != etag {
			return struct{}{}, wrap(k, ErrConflict)
		}
		if err := b.del(ctx, k, etag); errors.Is(err, errPrecondition) {
			return struct{}{}, wrap(k, ErrConflict)
		} else if err != nil {
			return struct{}{}, wrap(k, err)
		}
		return struct{}{}, nil
	})
	return err
}

// casAttempts is the number of reads CAS makes before it gives up with ErrConflict.
const casAttempts = 8

// CAS reads the record at key, calls fn with it (nil if absent), and writes fn's result with
// If-Match of that read (or If-None-Match when absent). On ErrConflict it re-reads and calls fn
// again, up to 8 times with jittered backoff (10–200 ms). fn returning ErrNoChange ends CAS
// without writing (CAS then returns the current record and nil); fn returning nil, nil deletes
// the record (DELETE If-Match).
func CAS[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) (*T, error) {
	return Call(ctx, func(ctx context.Context) (*T, error) {
		if err := checkBucket(b); err != nil {
			return nil, err
		}
		_, k, err := keyOf(key)
		if err != nil {
			return nil, err
		}
		v, err := cas(ctx, b, k, casAttempts, fn)
		return v, wrap(k, err)
	})
}

// docAttempts is the CAS budget of the primitives' shared documents (index pages, shards):
// many writers meet on one document by design, and each round lets only one of them win.
const docAttempts = 64

// cas is CAS on a key string, for records and for the primitives' own documents.
func cas[T any](ctx context.Context, b *Bucket, key string, attempts int, fn func(cur *T) (*T, error)) (*T, error) {
	for attempt := range attempts {
		if attempt > 0 {
			if err := pause(ctx, 10*time.Millisecond, 200*time.Millisecond); err != nil {
				return nil, err
			}
		}
		v, err := casOnce(ctx, b, key, fn)
		if !errors.Is(err, errPrecondition) {
			return v, err
		}
	}
	return nil, ErrConflict
}

// casOnce is one round of CAS: read, decide, conditional write. It returns errPrecondition
// when the write lost against another writer.
func casOnce[T any](ctx context.Context, b *Bucket, key string, fn func(cur *T) (*T, error)) (*T, error) {
	o, _, err := b.get(ctx, key, "")
	var cur *T
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return nil, err
	default:
		if cur, err = decodeAt[T](o, key); err != nil {
			return nil, err
		}
	}
	next, err := fn(cur)
	if errors.Is(err, ErrNoChange) {
		return cur, nil
	}
	if err != nil {
		return nil, err
	}
	if next == nil {
		if cur == nil {
			return nil, nil
		}
		if err := b.del(ctx, key, o.etag); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, nil
	}
	m, isRecord := bound(next)
	if isRecord {
		if nk, err := m.key(); err != nil || nk != key {
			return nil, fmt.Errorf("CAS func returned a record with another key (%s, %v)", nk, err)
		}
	}
	body, err := encode(next)
	if err != nil {
		return nil, err
	}
	c := ifMatch
	if cur == nil {
		c = ifAbsent
	}
	tag, err := b.put(ctx, key, body, c, o.etag)
	if err != nil {
		return nil, err
	}
	if isRecord {
		m.setETag(tag)
	}
	return next, nil
}
