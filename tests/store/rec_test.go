package store

import (
	"github.com/megakuul/dynamitedb"

	"github.com/megakuul/bob/internal/store"
)

// The test record kinds of the primitive tests. Each is a dynamitedb model (R-STORE-103
// rule 1): KeyField keys with their pk/sk names in tags, an ETagField, and DataFields with
// short CBOR names and omitempty. Key names live in the struct tags, so every document kind
// of a primitive is its own type, and the primitives get the kind through builder functions
// (newSeqLog, newPagedIndex, newShardSet below).

// rec is the test record: one pk-only record type.
type rec struct {
	ID   dynamitedb.KeyField          `pk:"trec" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[int64]  `cbor:"n,omitempty"`
	Wid  dynamitedb.DataField[string] `cbor:"w,omitempty"` // write id of the last writer (rule 7)
	Ep   dynamitedb.DataField[int64]  `cbor:"ep,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *rec) Draft() *rec {
	return &rec{ID: r.ID, ETag: r.ETag, N: store.Carry(r.N), Wid: store.Carry(r.Wid), Ep: store.Carry(r.Ep)}
}

// slot is the SeqLog payload: tqs/<name>/s/<seq20>.
type slot struct {
	Log  dynamitedb.KeyField         `pk:"tqs" cbor:"-"`
	Seq  dynamitedb.KeyField         `sk:"s" cbor:"-"`
	ETag dynamitedb.ETagField        `etag:"true" cbor:"-"`
	By   dynamitedb.DataField[int64] `cbor:"b,omitempty"`
	Ord  dynamitedb.DataField[int64] `cbor:"o,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *slot) Draft() *slot {
	return &slot{Log: r.Log, Seq: r.Seq, ETag: r.ETag, By: store.Carry(r.By), Ord: store.Carry(r.Ord)}
}

// tail and head are the hints tqt/<name> and tqh/<name> of the test log.
type tail struct {
	Log  dynamitedb.KeyField          `pk:"tqt" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

type head struct {
	Log  dynamitedb.KeyField          `pk:"tqh" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// newSeqLog is the test log tqs/tqt/tqh of name.
func newSeqLog(b *store.Bucket, name string) *store.SeqLog[slot] {
	return store.NewSeqLog(b, func(seq uint64) *slot {
		s, _ := store.Seq20(seq)
		return &slot{Log: dynamitedb.Key(name), Seq: dynamitedb.Key(s)}
	}, store.NewCounter(b, func(n uint64) *tail {
		return &tail{Log: dynamitedb.Key(name), N: dynamitedb.Set(n)}
	}, func(h *tail) uint64 { return h.N.Value() }), store.NewCounter(b, func(n uint64) *head {
		return &head{Log: dynamitedb.Key(name), N: dynamitedb.Set(n)}
	}, func(h *head) uint64 { return h.N.Value() }))
}

// entry is the PagedIndex entry.
type entry struct {
	W int64  `cbor:"w,omitempty"`
	V string `cbor:"v,omitempty"`
}

// page is one page tbpage/<scope>/p/<num10> of the test index.
type page struct {
	Scope dynamitedb.KeyField           `pk:"tbpage" cbor:"-"`
	Page  dynamitedb.KeyField           `sk:"p" cbor:"-"`
	ETag  dynamitedb.ETagField          `etag:"true" cbor:"-"`
	E     dynamitedb.DataField[[]entry] `cbor:"e,omitempty"`
}

// newPagedIndex is the test index tbpage/<scope> with p entries per page.
func newPagedIndex(b *store.Bucket, scope string, p int) *store.PagedIndex[entry] {
	return store.NewPagedIndex(b, p, func(k uint64, e []entry) *page {
		n, _ := store.Num10(k)
		return &page{Scope: dynamitedb.Key(scope), Page: dynamitedb.Key(n), E: dynamitedb.Set(e)}
	}, func(p *page) []entry { return p.E.Value() })
}

// act is the ShardSet entry.
type act struct {
	B string `cbor:"b,omitempty"`
}

// shard is one shard tjact/<sh2> of the test set.
type shard struct {
	Shard dynamitedb.KeyField                  `pk:"tjact" cbor:"-"`
	ETag  dynamitedb.ETagField                 `etag:"true" cbor:"-"`
	M     dynamitedb.DataField[map[string]act] `cbor:"m,omitempty"`
}

// newShardSet is the test set tjact with n shards.
func newShardSet(b *store.Bucket, n int) *store.ShardSet[act] {
	return store.NewShardSet(b, n, func(sh string, m map[string]act) *shard {
		return &shard{Shard: dynamitedb.Key(sh), M: dynamitedb.Set(m)}
	}, func(s *shard) map[string]act { return s.M.Value() })
}
