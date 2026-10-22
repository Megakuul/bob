// Package store is the only dynamitedb access of the server (R-STORE-106).
//
// Every record kind of SPEC §05.4 is one dynamitedb model: a struct with KeyField fields
// tagged pk/sk, an ETagField and DataField fields (records.go, identity.go, and the record
// types of other server packages, which use the aliases KeyField, ETagField and DataField of
// this package because only internal/store may import dynamitedb). All record reads and
// writes are dynamitedb calls: Get (dynamitedb.Get), Create (dynamitedb.Create, If-None-Match),
// CAS (dynamitedb.Get, then dynamitedb.Put with the read's ETag on a fresh value, ADR 0011 and
// R-STORE-103 rule 4), PutOwned (dynamitedb.Put without ETag) and Delete (dynamitedb.Delete
// with the ETag). The primitives SeqLog, PagedIndex, ShardSet, Counter and Lease are generic
// over the record type of their documents; builder functions supply the record kind.
//
// The package adds only what the library leaves to its user (docs/dynamitedb-lake-usage.md):
// recover around every call (Call, R-STORE-005), the bucket split and credentials, the
// conformance probe, GET If-None-Match for GetCached, and bounded retries around
// dynamitedb.ErrConcurrencyConflict, including the re-read that decides a write whose response
// was lost (R-STORE-103 rule 7).
//
// Every read is a GET of a known key (R-STORE-100); LIST exists only in package sweep.
package store
