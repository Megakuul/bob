package store

// rec is the test record of the primitive tests: one pk-only record type with short CBOR
// names and omitempty (R-STORE-103 rule 1). When dynamitedb joins go.mod this is the one
// place that changes to dynamitedb.KeyField / ETagField / DataField.
type rec struct {
	ID   string `pk:"trec"`
	ETag string `etag:"true" cbor:"-"`
	N    int64  `cbor:"n,omitempty"`
	Wid  string `cbor:"w,omitempty"` // write id of the last writer (rule 7)
	Ep   int64  `cbor:"ep,omitempty"`
}

// slot is the SeqLog payload.
type slot struct {
	ID   string `pk:"tqs"`
	ETag string `etag:"true" cbor:"-"`
	By   int64  `cbor:"b,omitempty"`
	Ord  int64  `cbor:"o,omitempty"`
}

// entry is the PagedIndex entry.
type entry struct {
	W int64  `cbor:"w,omitempty"`
	V string `cbor:"v,omitempty"`
}
