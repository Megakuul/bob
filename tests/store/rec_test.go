package store

// rec is the test record of the primitive tests: one pk-only record type with short CBOR
// names and omitempty (R-STORE-103 rule 1). When dynamitedb joins go.mod this is the one
// place that changes to dynamitedb.KeyField / ETagField / DataField.
type rec struct {
	ID   store.KeyField          `pk:"trec" cbor:"-"`
	ETag store.ETagField         `etag:"true" cbor:"-"`
	N    store.DataField[int64]  `cbor:"n,omitempty"`
	Wid  store.DataField[string] `cbor:"w,omitempty"` // write id of the last writer (rule 7)
	Ep   store.DataField[int64]  `cbor:"ep,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *rec) Draft() *rec {
	return &rec{ID: r.ID,
		ETag: r.ETag,
		N:    store.Carry(r.N),
		Wid:  store.Carry(r.Wid),
		Ep:   store.Carry(r.Ep)}
}

// slot is the SeqLog payload.
type slot struct {
	ID   store.KeyField         `pk:"tqs" cbor:"-"`
	ETag store.ETagField        `etag:"true" cbor:"-"`
	By   store.DataField[int64] `cbor:"b,omitempty"`
	Ord  store.DataField[int64] `cbor:"o,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *slot) Draft() *slot {
	return &slot{ID: r.ID,
		ETag: r.ETag,
		By:   store.Carry(r.By),
		Ord:  store.Carry(r.Ord)}
}

// entry is the PagedIndex entry.
type entry struct {
	W int64  `cbor:"w,omitempty"`
	V string `cbor:"v,omitempty"`
}
