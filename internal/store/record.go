package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/megakuul/dynamitedb"
)

// Errors. All are wrapped with the key, e.g. "store: job/01J…: not found".
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("changed concurrently")
	ErrPanic    = errors.New("dynamitedb panicked")
	ErrNoChange = errors.New("no change") // returned by a CAS func to skip the write
)

// The dynamitedb types and the two value constructors other packages need. R-STORE-106 lets
// only internal/store import dynamitedb, so record types defined elsewhere (forge, project,
// notify, …) name their fields through these aliases and build update values with Set and ID.
// They are the library's own types and functions, not copies.
type (
	// KeyField is dynamitedb.KeyField: a pk/sk key of a record.
	KeyField = dynamitedb.KeyField
	// ETagField is dynamitedb.ETagField: the ETag of a fetched record, used for If-Match.
	ETagField = dynamitedb.ETagField
	// DataField is dynamitedb.DataField: a mutable data field of a record.
	DataField[T any] = dynamitedb.DataField[T]
)

// Set is dynamitedb.Set: the update operator that writes v.
func Set[T any](v T) DataField[T] { return dynamitedb.Set(v) }

// ID is dynamitedb.Key: the key value id (usable in filter and update values alike).
func ID(id string) KeyField { return dynamitedb.Key(id) }

// Carry turns a fetched field into the update operator that writes the same value again, so
// that a fresh value built from a fetched record keeps that field (the records' Draft
// methods). A zero value is not written (the record reads it back as zero: omitempty).
func Carry[T any](f DataField[T]) DataField[T] {
	if f == nil {
		return nil
	}
	v := f.Value()
	if reflect.ValueOf(&v).Elem().IsZero() {
		return nil
	}
	return dynamitedb.Set(v)
}

// ETagOf returns the ETag of a fetched record ("" for a fresh value).
func ETagOf(e ETagField) string {
	if e == nil {
		return ""
	}
	return *e
}

// panics is the metric bob_store_panics_total of this process.
var panics atomic.Int64

// Panics is the value of the metric bob_store_panics_total of this process.
func Panics() int64 { return panics.Load() }

// Call runs fn and converts a panic into ErrPanic (R-STORE-005). Every dynamitedb call of the
// package goes through it (dynamitedb panics on misuse, e.g. a filter operator in an update
// value, a fetched record passed to Put, a nil model, or a backend without ETag headers); it
// is exported so the harness can feed it each misuse panic. The panic is logged with its stack
// and counted in bob_store_panics_total.
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

// Records are dynamitedb models: one struct type per record kind (SPEC §05.4) with KeyField
// fields tagged `pk:"<name>"` (and optionally `sk:"<name>"`), an ETagField tagged
// `etag:"true"`, and DataField fields with short CBOR names and omitempty (R-STORE-103 rule 1):
//
//	type JobRecord struct {
//		ID    dynamitedb.KeyField          `pk:"job" cbor:"-"`
//		ETag  dynamitedb.ETagField         `etag:"true" cbor:"-"`
//		State dynamitedb.DataField[string] `cbor:"st,omitempty"`
//	}
//
// A key value has only its keys set (store.ID). An update value (Create, PutOwned, the result
// of a CAS func) carries update operators (store.Set); a fetched record carries read values
// (.Value()). The helpers below read the struct tags exactly as dynamitedb does; they never
// look at a field's data.

var (
	keyFieldType  = reflect.TypeFor[dynamitedb.KeyField]()
	etagFieldType = reflect.TypeFor[dynamitedb.ETagField]()
)

// objectKey is the S3 key dynamitedb stores v under: <pk>/<pkval>[/<sk>/<skval>]. Every
// segment is checked (R-STORE-104: dynamitedb accepts empty segments, bob does not).
func objectKey[T any](v *T) (string, error) {
	rv := reflect.ValueOf(v).Elem()
	var parts []string
	for _, tag := range []string{"pk", "sk"} {
		for i := range rv.NumField() {
			f := rv.Type().Field(i)
			name, ok := f.Tag.Lookup(tag)
			if !ok || f.Type != keyFieldType {
				continue
			}
			k, _ := rv.Field(i).Interface().(dynamitedb.KeyField)
			if k == nil {
				return "", fmt.Errorf("store: %s key %q of %s is unset", tag, name, rv.Type())
			}
			parts = append(parts, name, k.Value())
			break
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("store: %s has no pk KeyField", rv.Type())
	}
	return Key(parts[0], parts[1], parts[2:]...)
}

// keyOnly returns a new value holding only v's key fields: the filter value of a Get or
// Delete (bob uses no dynamitedb filters, R-STORE-103 rule 2).
func keyOnly[T any](v *T) *T {
	src := reflect.ValueOf(v).Elem()
	out := new(T)
	dst := reflect.ValueOf(out).Elem()
	for i := range src.NumField() {
		if src.Type().Field(i).Type == keyFieldType {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return out
}

// withKey returns a shallow copy of v whose key fields are key's (SeqLog.Append).
func withKey[T any](v, key *T) *T {
	out := *v
	dst, src := reflect.ValueOf(&out).Elem(), reflect.ValueOf(key).Elem()
	for i := range src.NumField() {
		if src.Type().Field(i).Type == keyFieldType {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return &out
}

// withETag returns a shallow copy of v whose ETag field is etag: nil for an unconditional or
// create-only write, the ETag of a read for an If-Match write.
func withETag[T any](v *T, etag dynamitedb.ETagField) *T {
	out := *v
	rv := reflect.ValueOf(&out).Elem()
	for i := range rv.NumField() {
		f := rv.Type().Field(i)
		if ok, _ := strconv.ParseBool(f.Tag.Get("etag")); ok && f.Type == etagFieldType {
			rv.Field(i).Set(reflect.ValueOf(etag))
		}
	}
	return &out
}

// etagOf returns the ETag field of a fetched record.
func etagOf[T any](v *T) dynamitedb.ETagField {
	rv := reflect.ValueOf(v).Elem()
	for i := range rv.NumField() {
		f := rv.Type().Field(i)
		if ok, _ := strconv.ParseBool(f.Tag.Get("etag")); ok && f.Type == etagFieldType {
			e, _ := rv.Field(i).Interface().(dynamitedb.ETagField)
			return e
		}
	}
	return nil
}

// drafter is implemented by every record type: Draft builds the fresh update value that
// writes a fetched record again (keys, ETag, and store.Carry of every data field).
type drafter[T any] interface{ Draft() *T }

// writeValue turns the result of a CAS func into the fresh update value dynamitedb.Put needs.
// A CAS func may return a fresh value, or a fetched record (or a copy of one) with some fields
// replaced by update operators. Data fields that still hold fetched read values (from this
// read or an earlier one) cannot go into a write (dynamitedb panics: "incorrect DataField
// usage"); they are written again with the record type's Draft (store.Carry of the read
// value). A field is a read value when its dynamic type is that of the same field of orig,
// the record CAS just read (dynamitedb fills every field of a fetched record).
func writeValue[T any](orig, next *T) (*T, error) {
	if orig == nil {
		return next, nil
	}
	nv, ov := reflect.ValueOf(next).Elem(), reflect.ValueOf(orig).Elem()
	out := *next
	wv := reflect.ValueOf(&out).Elem()
	// reads is next with every field that is not a read value replaced by orig's read value,
	// so that its Draft carries exactly the read values of next.
	reads := *next
	rv := reflect.ValueOf(&reads).Elem()
	var fields []int
	for i := range nv.NumField() {
		f := nv.Type().Field(i)
		if !f.IsExported() || f.Type.Kind() != reflect.Interface || f.Type == keyFieldType {
			continue
		}
		n, o := nv.Field(i), ov.Field(i)
		if n.IsNil() || o.IsNil() || n.Elem().Type() != o.Elem().Type() {
			rv.Field(i).Set(o) // an update operator (or a cleared field): written as it is
			continue
		}
		fields = append(fields, i)
	}
	if len(fields) == 0 {
		return &out, nil
	}
	d, ok := any(&reads).(drafter[T])
	if !ok {
		return nil, fmt.Errorf("store: %T has no Draft method; a CAS func must return a fresh value", orig)
	}
	draft := reflect.ValueOf(d.Draft()).Elem()
	for _, i := range fields {
		wv.Field(i).Set(draft.Field(i))
	}
	return &out, nil
}

func wrap(key string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s: %w", key, err)
}

func checkBucket(b *Bucket) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("store: %w: nil bucket", ErrPanic)
	}
	return nil
}

// get is dynamitedb.Get of the record whose keys are set in key.
func get[T any](ctx context.Context, b *Bucket, key *T) (*T, string, error) {
	k, err := objectKey(key)
	if err != nil {
		return nil, "", err
	}
	v, err := dynamitedb.Get(ctx, b.db, keyOnly(key))
	switch {
	case err == nil:
		return v, k, nil
	case ctx.Err() != nil: // R-STORE-103 rule 6 (S9 B11): the library hides the context error
		return nil, k, ctx.Err()
	case errors.Is(err, dynamitedb.ErrNotFound):
		return nil, k, ErrNotFound
	}
	return nil, k, err
}

// Get reads the record whose key fields are set in key (one GET, dynamitedb.Get). The result
// carries the ETag.
func Get[T any](ctx context.Context, b *Bucket, key *T) (*T, error) {
	return Call(ctx, func(ctx context.Context) (*T, error) {
		if err := checkBucket(b); err != nil {
			return nil, err
		}
		v, k, err := get(ctx, b, key)
		return v, wrap(k, err)
	})
}

// Cache keeps the last fetched value and ETag per key for GetCached. Zero value is ready to
// use. Values handed out are shared: callers must not modify them.
type Cache[T any] struct {
	mu sync.Mutex
	m  map[string]cached[T]
}

type cached[T any] struct {
	v    *T
	etag string
}

func (c *Cache[T]) lookup(key string) (cached[T], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

func (c *Cache[T]) put(key string, v *T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cached[T]{}
	}
	c.m[key] = cached[T]{v: v, etag: ETagOf(etagOf(v))}
}

func (c *Cache[T]) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// getCached is GetCached; c may be nil (no caching).
func getCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (*T, bool, string, error) {
	k, err := objectKey(key)
	if err != nil {
		return nil, false, "", err
	}
	var have cached[T]
	var ok bool
	if c != nil {
		have, ok = c.lookup(k)
	}
	if ok && have.etag != "" {
		switch notModified, err := b.unchanged(ctx, k, have.etag); {
		case errors.Is(err, ErrNotFound):
			c.drop(k)
			return nil, false, k, err
		case err != nil:
			return nil, false, k, err
		case notModified:
			return have.v, false, k, nil
		}
	}
	v, _, err := get(ctx, b, key)
	if errors.Is(err, ErrNotFound) && c != nil {
		c.drop(k)
	}
	if err != nil {
		return nil, false, k, err
	}
	if c != nil {
		c.put(k, v)
	}
	return v, true, k, nil
}

// GetCached sends GET If-None-Match with the cached ETag. On 304 it returns the cached value
// and changed=false (1 request). On 200 it reads the record with dynamitedb.Get (2 requests
// in total) and updates the cache; without a cached ETag it reads with dynamitedb.Get only.
// On 404 it drops the entry and returns ErrNotFound.
func GetCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (v *T, changed bool, err error) {
	type res struct {
		v       *T
		changed bool
	}
	r, err := Call(ctx, func(ctx context.Context) (res, error) {
		if err := checkBucket(b); err != nil {
			return res{}, err
		}
		v, changed, k, err := getCached(ctx, b, c, key)
		return res{v, changed}, wrap(k, err)
	})
	return r.v, r.changed, err
}

// mode is the precondition of a write.
type mode int

const (
	createOnly mode = iota // dynamitedb.Create: PUT If-None-Match: *
	ifMatch                // dynamitedb.Put with the ETag of a read: PUT If-Match
	owned                  // dynamitedb.Put without ETag: unconditional
)

// write sends v with dynamitedb.Create or dynamitedb.Put. It resolves a write whose outcome
// the response did not tell (R-STORE-103 rule 7): when an earlier attempt of the request got
// no answer (a lost response the SDK retried, or no response at all), dynamitedb reports the
// retry's 412 as ErrAlreadyExists/ErrConcurrencyConflict; write then re-reads the key, and when
// it holds exactly the bytes this call sent, the write was ours and succeeded.
func write[T any](ctx context.Context, b *Bucket, v *T, m mode, etag dynamitedb.ETagField) (string, error) {
	k, err := objectKey(v)
	if err != nil {
		return "", err
	}
	if m == ifMatch && etag == nil {
		return k, errors.New("store: an If-Match write needs the ETag of a read")
	}
	for attempt := 0; ; attempt++ {
		wctx, w := observe(ctx)
		switch m {
		case createOnly:
			err = dynamitedb.Create(wctx, b.db, withETag(v, nil))
		case ifMatch:
			err = dynamitedb.Put(wctx, b.db, withETag(v, etag))
		case owned:
			err = dynamitedb.Put(wctx, b.db, withETag(v, nil))
		}
		if err == nil {
			return k, nil
		}
		if ctx.Err() != nil {
			return k, ctx.Err()
		}
		lost := w.retried(opPut) || w.unanswered(opPut)
		switch {
		case errors.Is(err, dynamitedb.ErrAlreadyExists), errors.Is(err, dynamitedb.ErrConcurrencyConflict):
			if lost {
				if mine, _, rerr := b.holds(ctx, k, w.sent()); rerr != nil {
					return k, rerr
				} else if mine {
					return k, nil
				}
			}
			if m == createOnly {
				return k, ErrExists
			}
			return k, ErrConflict
		case w.status(opPut) == 404 && m == ifMatch:
			return k, ErrNotFound // PUT If-Match on a deleted key (S9 B13)
		case w.status(opPut) == 409 && m != owned:
			// AWS answers a conditional write that races another one on the same key with 409.
			if m == createOnly {
				return k, ErrExists
			}
			return k, ErrConflict
		case lost && attempt < 2:
			mine, found, rerr := b.holds(ctx, k, w.sent())
			switch {
			case rerr != nil:
				return k, rerr
			case mine:
				return k, nil
			case m == owned, m == createOnly && !found:
				if perr := pause(ctx, 20*time.Millisecond, 100*time.Millisecond); perr != nil {
					return k, perr
				}
				continue // the write did not land: send it again
			case m == createOnly:
				return k, ErrExists
			}
			return k, ErrConflict // the CAS loop re-reads
		}
		return k, err
	}
}

// Create writes v only if its key does not exist (dynamitedb.Create: PUT If-None-Match: *).
// v is a fresh value (store.Set operators).
func Create[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(b); err != nil {
			return struct{}{}, err
		}
		k, err := write(ctx, b, v, createOnly, nil)
		return struct{}{}, wrap(k, err)
	})
	return err
}

// PutOwned writes v unconditionally (dynamitedb.Put without ETag). Only for records with
// exactly one legitimate writer (orch/<self>, whb/<self>). v is a fresh value.
func PutOwned[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(b); err != nil {
			return struct{}{}, err
		}
		k, err := write(ctx, b, v, owned, nil)
		return struct{}{}, wrap(k, err)
	})
	return err
}

// del is dynamitedb.Delete of key with the ETag etag (a GET If-Match, then DELETE If-Match).
func del[T any](ctx context.Context, b *Bucket, key *T, etag dynamitedb.ETagField) (string, error) {
	k, err := objectKey(key)
	if err != nil {
		return "", err
	}
	if etag == nil {
		return k, errors.New("store: Delete needs the ETag of a read")
	}
	wctx, w := observe(ctx)
	err = dynamitedb.Delete(wctx, b.db, withETag(keyOnly(key), etag))
	if ctx.Err() != nil && err != nil {
		return k, ctx.Err()
	}
	switch {
	case err == nil && w.status(opGet) == 404:
		return k, ErrNotFound // dynamitedb.Delete treats a missing key as done
	case err == nil:
		return k, nil
	case w.retried(opDelete) || w.unanswered(opDelete):
		// The DELETE's answer was lost: gone means done.
		if _, found, rerr := b.holds(ctx, k, nil); rerr != nil {
			return k, rerr
		} else if !found {
			return k, nil
		}
		return k, ErrConflict
	case errors.Is(err, dynamitedb.ErrConcurrencyConflict):
		// GET If-Match on a missing key is answered 412 by some backends.
		if _, found, rerr := b.holds(ctx, k, nil); rerr == nil && !found {
			return k, ErrNotFound
		}
		return k, ErrConflict
	}
	return k, err
}

// Delete removes the record if its ETag still matches v's ETag (2 requests: dynamitedb.Delete
// sends GET If-Match, then DELETE If-Match). A missing record is ErrNotFound, a changed one
// ErrConflict.
func Delete[T any](ctx context.Context, b *Bucket, v *T) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(b); err != nil {
			return struct{}{}, err
		}
		k, err := del(ctx, b, v, etagOf(v))
		return struct{}{}, wrap(k, err)
	})
	return err
}

// casAttempts is the number of reads CAS makes before it gives up with ErrConflict.
const casAttempts = 8

// docAttempts is the CAS budget of the primitives' shared documents (index pages, shards):
// many writers meet on one document by design, and each round lets only one of them win.
const docAttempts = 64

// CAS reads the record at key, calls fn with it (nil if absent), and writes fn's result with
// If-Match of that read (dynamitedb.Put with the read's ETag), or with If-None-Match when
// absent (dynamitedb.Create). fn returns a fresh value (store.Set operators, e.g. from the
// record's Draft) or the fetched record with some fields replaced by store.Set operators; the
// fields it left alone are written as they were read. On ErrConflict it re-reads and calls fn
// again, up to 8 times with jittered backoff (10–200 ms). fn returning ErrNoChange ends CAS
// without writing (CAS then returns the current record and nil); fn returning nil, nil deletes
// the record (DELETE If-Match). After a write, CAS returns the record as stored (one GET).
func CAS[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) (*T, error) {
	return Call(ctx, func(ctx context.Context) (*T, error) {
		if err := checkBucket(b); err != nil {
			return nil, err
		}
		k, err := objectKey(key)
		if err != nil {
			return nil, err
		}
		cur, wrote, err := cas(ctx, b, key, casAttempts, fn)
		if err != nil || !wrote {
			return cur, wrap(k, err)
		}
		v, _, err := get(ctx, b, key)
		if errors.Is(err, ErrNotFound) {
			return nil, nil // deleted by somebody else right after our write
		}
		return v, wrap(k, err)
	})
}

// Change is CAS for callers that do not need the stored record: the same reads, decision and
// conditional write, without the read-back GET (the hot paths of R-FLEET-009). It returns
// fn's error, ErrConflict after 8 lost rounds, and nil after a write, a delete or ErrNoChange.
func Change[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) error {
	_, err := Call(ctx, func(ctx context.Context) (struct{}, error) {
		if err := checkBucket(b); err != nil {
			return struct{}{}, err
		}
		k, err := objectKey(key)
		if err != nil {
			return struct{}{}, err
		}
		_, _, err = cas(ctx, b, key, casAttempts, fn)
		return struct{}{}, wrap(k, err)
	})
	return err
}

// cas is CAS without the read-back: it returns the current record when fn chose ErrNoChange,
// nil after a delete, and wrote=true after a write.
func cas[T any](ctx context.Context, b *Bucket, key *T, attempts int, fn func(cur *T) (*T, error)) (*T, bool, error) {
	k, err := objectKey(key)
	if err != nil {
		return nil, false, err
	}
	for attempt := range attempts {
		if attempt > 0 {
			if err := pause(ctx, 10*time.Millisecond, 200*time.Millisecond); err != nil {
				return nil, false, err
			}
		}
		cur, _, err := get(ctx, b, key)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		var orig *T
		if cur != nil {
			o := *cur
			orig = &o
		}
		next, err := fn(cur)
		if errors.Is(err, ErrNoChange) {
			return cur, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if next == nil {
			if cur == nil {
				return nil, false, nil
			}
			_, err := del(ctx, b, key, etagOf(orig))
			switch {
			case err == nil, errors.Is(err, ErrNotFound):
				return nil, false, nil
			case errors.Is(err, ErrConflict):
				continue
			}
			return nil, false, err
		}
		if nk, err := objectKey(next); err != nil || nk != k {
			return nil, false, fmt.Errorf("CAS func returned a record with another key (%s, %v)", nk, err)
		}
		if cur == nil {
			_, err = write(ctx, b, next, createOnly, nil)
			if errors.Is(err, ErrExists) {
				continue
			}
		} else {
			w, werr := writeValue(orig, next)
			if werr != nil {
				return nil, false, werr
			}
			_, err = write(ctx, b, w, ifMatch, etagOf(orig))
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				continue
			}
		}
		if err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	return nil, false, ErrConflict
}

// setNZ is dynamitedb.Set for a value that is written only when it is not zero (omitempty:
// the record reads a missing field back as zero).
func setNZ[T any](v T) dynamitedb.DataField[T] {
	if reflect.ValueOf(&v).Elem().IsZero() {
		return nil
	}
	return dynamitedb.Set(v)
}
