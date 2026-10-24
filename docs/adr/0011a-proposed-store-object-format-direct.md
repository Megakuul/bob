# ADR 0011a — PROPOSED: the store speaks dynamitedb's object format directly

Status: **REJECTED by the owner 2026-10-22 ("of course use the library"); see A111.**
2026-10-22. Raised by unit I-B1 (plan/progress/I-B1.md, D-IB1-1). Not an owner decision yet: the
code exists on branch bob-v1 and can be reverted or adapted either way.

## Problem (measured with RustFS 1.0.3, dynamitedb v0.6.1)
1. The protected tests use plain records with a string `pk` field; `dynamitedb.Create` rejects them
   ("partition key 'trec' is unset"): key names live in struct tags, but SeqLog/PagedIndex/
   ShardSet/Lease take key names at run time.
2. `dynamitedb.Put` of a record returned by `dynamitedb.Get` panics ("incorrect DataField usage"),
   so `CAS(fn(cur))` cannot hand fn's result to `Put`.
3. Generic primitives need runtime key names; the library only offers tag-based keys.

## Option A (implemented by I-B1)
`internal/store` sends the S3 requests itself with the AWS SDK in dynamitedb's object format (same
keys, CBOR, ETag locking). Every other rule of ADR 0011 and R-STORE-103 holds (GET-only hot paths,
conditional writes, recover around every call, no LIST outside sweep). Cost: bob owns ~one file
of request code; compatible with dynamitedb data written by the library. Lost: "wraps the library".

## Option B
Keep the library: adapt the tests' record types (tags) and design the primitives around fixed
struct types per record kind, with generated types for key names; needs a workaround for blocker 2
(copy through a fresh struct). More code per record kind, closer to D8 ("use dynamitedb as it is").

## Option C
Ask upstream (D8 allows "real bugs/missing features"): runtime key names and a CAS that accepts a
new value. Slowest.

Recommendation: A, as the least code and fully testable; if the owner wants the library kept, B.
Until decided, I-B1's package stays and is isolated behind `internal/store` (no other package
imports the SDK), so switching is local.
