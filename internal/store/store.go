package store

import (
	"context"
	"errors"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Bucket is one dynamitedb bucket plus a raw S3 client for conditional GETs.
// It is safe for concurrent use.
type Bucket struct{}

// BucketConfig selects one bucket. Credentials come from the AWS SDK default chain.
type BucketConfig struct {
	Endpoint string // e.g. "https://s3.eu-central-1.amazonaws.com" or "http://127.0.0.1:9000"
	Region   string // e.g. "us-east-1"
	Name     string // bucket name, e.g. "bob-fleet"
	Role     string // "control" | "fleet" (metrics label only)
}

// Open connects to the bucket, runs the conformance probe (R-STORE-102) once per process,
// and returns a Bucket. It never lists.
func Open(ctx context.Context, cfg BucketConfig) (*Bucket, error) {
	return nil, diag.NotImplemented("store.Open")
}

// Errors. All are wrapped with the key, e.g. "store: job/01J…: not found".
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("changed concurrently")
	ErrPanic    = errors.New("dynamitedb panicked")
	ErrNoChange = errors.New("no change") // returned by a CAS func to skip the write
)

// Call runs fn and converts a panic into ErrPanic (R-STORE-005). Every dynamitedb call of the
// package goes through it; it is exported so the harness can feed it each misuse panic.
func Call[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	return zero, diag.NotImplemented("store.Call")
}

// Get reads the record whose key fields are set in key. The result carries the ETag.
func Get[T any](ctx context.Context, b *Bucket, key *T) (*T, error) {
	return nil, diag.NotImplemented("store.Get")
}

// Cache keeps the last value and ETag per key for GetCached. Zero value is ready to use.
type Cache[T any] struct{}

// GetCached sends GET If-None-Match with the cached ETag. On 304 it returns the cached value
// and changed=false (1 request). On 200 it reads the record (2 requests in total) and updates
// the cache. On 404 it drops the entry and returns ErrNotFound.
func GetCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (v *T, changed bool, err error) {
	return nil, false, diag.NotImplemented("store.GetCached")
}

// Create writes v only if its key does not exist (PUT If-None-Match: *).
func Create[T any](ctx context.Context, b *Bucket, v *T) error {
	return diag.NotImplemented("store.Create")
}

// CAS reads the record at key, calls fn with it (nil if absent), and writes fn's result with
// If-Match of that read (or If-None-Match when absent). On ErrConflict it re-reads and calls fn
// again, up to 8 times with jittered backoff. fn returning ErrNoChange ends CAS without
// writing; fn returning nil, nil deletes the record.
func CAS[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) (*T, error) {
	return nil, diag.NotImplemented("store.CAS")
}

// PutOwned writes v unconditionally. Only for records with exactly one legitimate writer.
func PutOwned[T any](ctx context.Context, b *Bucket, v *T) error {
	return diag.NotImplemented("store.PutOwned")
}

// Delete removes the record if its ETag still matches v's ETag.
func Delete[T any](ctx context.Context, b *Bucket, v *T) error {
	return diag.NotImplemented("store.Delete")
}

// SeqLog is a dense append-only log of slots on known keys. It never lists.
type SeqLog[T any] struct {
	Bucket *Bucket
	SlotPK string
	TailPK string
	HeadPK string
	Name   string
}

// NewSeqLog returns the log; it performs no I/O.
func NewSeqLog[T any](b *Bucket, slotPK, tailPK, headPK, name string) *SeqLog[T] {
	return &SeqLog[T]{Bucket: b, SlotPK: slotPK, TailPK: tailPK, HeadPK: headPK, Name: name}
}

// Append creates the next free slot and returns its number.
func (l *SeqLog[T]) Append(ctx context.Context, v *T) (seq uint64, err error) {
	return 0, diag.NotImplemented("store.SeqLog.Append")
}

// Read GETs one slot. ErrNotFound means seq is at or past the end of the log.
func (l *SeqLog[T]) Read(ctx context.Context, seq uint64) (*T, error) {
	return nil, diag.NotImplemented("store.SeqLog.Read")
}

// Tail returns the tail hint.
func (l *SeqLog[T]) Tail(ctx context.Context) (uint64, error) {
	return 0, diag.NotImplemented("store.SeqLog.Tail")
}

// Head returns the head hint.
func (l *SeqLog[T]) Head(ctx context.Context) (uint64, error) {
	return 0, diag.NotImplemented("store.SeqLog.Head")
}

// AdvanceHead raises the head hint to n if it is lower (monotonic).
func (l *SeqLog[T]) AdvanceHead(ctx context.Context, n uint64) error {
	return diag.NotImplemented("store.SeqLog.AdvanceHead")
}

// PagedIndex is a list of entries numbered 0,1,2,… stored P per page document.
type PagedIndex[E any] struct {
	Bucket *Bucket
	PagePK string
	Scope  string
	P      int
}

// NewPagedIndex performs no I/O.
func NewPagedIndex[E any](b *Bucket, pagePK, scope string, p int) *PagedIndex[E] {
	return &PagedIndex[E]{Bucket: b, PagePK: pagePK, Scope: scope, P: p}
}

// Set writes entry n (CAS on its page; creates the page on first use).
func (x *PagedIndex[E]) Set(ctx context.Context, n uint64, e E) error {
	return diag.NotImplemented("store.PagedIndex.Set")
}

// Page returns page k. Missing entries in a page are zero values.
func (x *PagedIndex[E]) Page(ctx context.Context, k uint64) ([]E, error) {
	return nil, diag.NotImplemented("store.PagedIndex.Page")
}

// LastPage returns the page number of entry count-1.
func (x *PagedIndex[E]) LastPage(count uint64) uint64 {
	if count == 0 || x.P <= 0 {
		return 0
	}
	return (count - 1) / uint64(x.P)
}

// ShardSet is a set of string ids spread over N shard documents.
type ShardSet[E any] struct {
	Bucket *Bucket
	PK     string
	N      int
}

// NewShardSet performs no I/O.
func NewShardSet[E any](b *Bucket, pk string, n int) *ShardSet[E] {
	return &ShardSet[E]{Bucket: b, PK: pk, N: n}
}

// Add adds id (CAS on its shard).
func (s *ShardSet[E]) Add(ctx context.Context, id string, e E) error {
	return diag.NotImplemented("store.ShardSet.Add")
}

// Remove removes id (CAS; absent is nil).
func (s *ShardSet[E]) Remove(ctx context.Context, id string) error {
	return diag.NotImplemented("store.ShardSet.Remove")
}

// All reads every shard.
func (s *ShardSet[E]) All(ctx context.Context) (map[string]E, error) {
	return nil, diag.NotImplemented("store.ShardSet.All")
}

// Lease is a CAS-held lease on one record: owner, epoch, expires.
type Lease struct {
	Bucket *Bucket
	Key    string
	Owner  string
	TTL    time.Duration
}

// NewLease performs no I/O.
func NewLease(b *Bucket, key, owner string, ttl time.Duration) *Lease {
	return &Lease{Bucket: b, Key: key, Owner: owner, TTL: ttl}
}

// TryAcquire creates, renews or takes over the lease.
func (l *Lease) TryAcquire(ctx context.Context) (held bool, epoch uint64, err error) {
	return false, 0, diag.NotImplemented("store.Lease.TryAcquire")
}
