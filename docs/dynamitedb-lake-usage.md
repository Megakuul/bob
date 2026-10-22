# dynamitedb and lake: how bob uses them (binding, 2026-10-22)

Owner rule (A111): bob uses the libraries as they are and as documented. No wrappers that re-implement
them, no shims, no "workarounds" for behaviour that the README documents. A suspected library
limitation is proven with a minimal failing program that follows the README exactly, written to
plan/progress/<unit>.md, before anything else is considered. Source: the module cache
(`github.com/megakuul/dynamitedb@v0.6.1`, `github.com/megakuul/lake@v0.5.3`), README.md in each.

## dynamitedb (read README.md first; the whole API is about 25 functions)

A record kind is a struct. Keys are `KeyField` with `pk:"<name>"` / `sk:"<name>"` tags, mutable data
are `DataField[T]`, concurrency is `ETagField`:

```go
type JobRecord struct {
    ID    dynamitedb.KeyField             `pk:"job" cbor:"-"`
    ETag  dynamitedb.ETagField            `etag:"true" cbor:"-"`
    State dynamitedb.DataField[string]    `cbor:"state,omitempty"`
    Epoch dynamitedb.DataField[int]       `cbor:"epoch,omitempty"`
}
```

* Always `omitempty` on DataFields. Key names are in the tags, so there is **one struct type per record
  kind** (the SPEC section 05.4 lists them). Generic code is generic over the record type
  (`func CAS[T any](...)`), and the caller passes a function that builds the key/filter value
  (`func(id string) *JobRecord`). Do not look for runtime key names; do not build struct types at run time.
* **Two kinds of values, never mixed.** *Filter values* (for `Get`, `Query`, `Delete`) use the key fields
  and filter operators (`Eq`, `NotEq`, `In`, `GreaterThan`, `Before`, `CustomFilter` ...). *Update
  values* (for `Create`, `Put`, `Update`) use key fields and update operators (`Set`, `Increment`,
  `Append`, `Emplace`, `CustomUpdate` ...). A filter operator in an update call, or an update operator
  in a filter call, **panics** (R-STORE-005: the store recovers and returns `ErrPanic`).
* **Values returned by `Get`/`Query` are read-only results.** Read them with `.Value()` and `.ETag`.
  Never pass a fetched record to `Put`/`Update`/`Create`: its fields are plain read values, not
  operators, and the library panics ("incorrect DataField usage"). Build a **fresh** value for the write.
* **Compare-and-swap is `Get` then `Put` with the ETag you read** (ADR 0011 decision 2 and SPEC R-STORE-103: `dynamitedb.Update` is NOT used; the protected test TestRSTORE103SourceRules fails if `dynamitedb.Update(` appears under internal/). Build the Put value fresh:

  ```go
  cur, err := dynamitedb.Get(ctx, b, &JobRecord{ID: dynamitedb.Key(id)})            // read (ETag in cur.ETag)
  // decide from cur.State.Value() ...
  err = dynamitedb.Put(ctx, b, &JobRecord{                                           // fresh value, update operators
      ID: dynamitedb.Key(id), ETag: cur.ETag,
      State: dynamitedb.Set("running"), Epoch: dynamitedb.Set(cur.Epoch.Value()+1),
  })
  if errors.Is(err, dynamitedb.ErrConcurrencyConflict) { /* lost the race: re-read, retry (bounded) */ }
  ```
  `Put` writes the whole document with If-Match. Decide **before** calling it whether to write at all.
* Create-only is `Create` (If-None-Match: `ErrAlreadyExists`). Whole-document write is `Put` with an ETag
  for a conditional replace. `Delete` takes a filter value with an ETag for a conditional delete.
* `Get` with only keys is one GET. A filter operator that fails gives `ErrNotFound`. `Query` lists: **no
  `Query` outside `internal/store/sweep` (D60)**.
* Sentinel errors in the library's `errors.go`: `ErrNotFound`, `ErrAlreadyExists`,
  `ErrConcurrencyConflict`. Never match S3 error strings.
* bob wraps nothing except: `recover()` around every library call (it panics on misuse), the bucket
  split/credentials, the conformance probe, and the retry/backoff loops around `ErrConcurrencyConflict`.

## lake (read README.md first)

Use lake's own API for tables, inserts, queries, compaction and partitions as documented in its README
and `test/`. Workarounds (the SPEC "L1..L4" notes) exist only where a minimal program that follows the
README demonstrably fails; each one is a small function with a comment pointing at that program in
`plan/progress/`.

## Guardrails (enforced by `tools/lint`, rule `dbusage`)

* Only `internal/store` (and `internal/store/sweep` for Query) imports `github.com/megakuul/dynamitedb`;
  only `internal/logstore` imports `github.com/megakuul/lake`; the AWS S3 SDK is imported only by
  `internal/store` (probe, sweep, artifacts) and `internal/cache`/`internal/promote` (artifacts bucket
  objects), never to reimplement dynamitedb records.
* A call to `dynamitedb.Create/Put/Update` must take a composite literal (a fresh value) as its
  argument and that literal must not contain filter operators; `Get/Query/Delete` literals must not
  contain update operators.
* `dynamitedb.Query` only in `internal/store/sweep`.
