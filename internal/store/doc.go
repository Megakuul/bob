// Package store is the only dynamitedb access of the server.
//
// It keeps dynamitedb's object format and rules (keys <pk>/<pkval>[/<sk>/<skval>], one CBOR
// map per record with short field names, ETag optimistic locking: create = PUT
// If-None-Match, change = GET then PUT If-Match of that GET) and issues the S3 requests
// itself through one AWS SDK client per bucket. It does not call the dynamitedb library: its
// model types need compile-time key names (the primitives take them at run time), dynamitedb.Put
// panics on a record it returned itself (so CAS(fn(cur)) cannot hand fn's result back), and
// the SDK retries it relies on hide whether a conditional write landed (R-STORE-103 rule 7).
// See plan/progress/I-B1.md, DECISIONS.
//
// Every read is a GET of a known key (R-STORE-100); LIST exists only in package sweep.
package store
