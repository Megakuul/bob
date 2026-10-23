# §05 Server data: buckets, keys, state machines, fleet, logs

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Scope: the four buckets and their policies, the dynamitedb key schema (GET-only, D60), the
`internal/store` primitives, the state machines for build, job, worker and release, leases and
epochs, polling plus wake-up (D54), scheduling and batch splitting (D53), worker registration
and authentication (D57), worker isolation (D56), logs in lake with live pull (D55), and
orchestrator HA without a coordinator.

ADRs: 0011, 0012, 0013, 0015, 0016, 0019 (all `Accepted (locked at Gate S)` with owner amendments).
Evidence: spikes S9, S10, S11, S13, S14, S17 and the §05 verification runs in
`/root/scratch-spec05/RESULTS.txt` (V1–V4 below).

## Interfaces

§05 provides:
- `internal/store`: `store.Bucket`, `store.Get`, `store.GetCached`, `store.Create`, `store.CAS`,
  `store.PutOwned`, `store.Delete`, `store.SeqLog`, `store.PagedIndex`, `store.ShardSet`,
  `store.Lease` (§05.3). Every other server package reaches S3 only through it (lake excepted).
- Record types: `JobRecord`, `JobSpec`, `BuildRecord`, `BuildPage`, `ReleaseRecord`,
  `WorkerRecord`, `FleetConfig`, `ProjectRecord`, `UserRecord`, `RoleRecord`, `TokenRecord`,
  `ServiceAccountRecord`, `ForgeRecord`, `SettingRecord` with their keys (§05.4). §06 owns the
  fields of the identity/forge/release records that the API exposes; §05 owns keys and access ops.
- State machines: build, job, worker, release, controller lease (§05.6).
- `proto/bob/worker/v1/worker.proto` (complete, §05.9): `WorkerControlService` (Attach,
  GetCheckoutCredential). Worker-token RPCs are outside user RBAC (§05.9.3).
- Log tables `log_line`, `package_run`, partition naming, `logstore.Reader` for §06's
  `LogService` (GetLog/FollowLog/SearchLog) (§05.10).
- Artifacts bucket key prefixes (§05.2.4); the promotion protocol itself is §06's.
- Budgets R-NFR-100…109 (§05.12).

§05 expects:
- §06: `LogService.FollowLog` server stream that calls `logstore.Follow` (§05.10.4);
  `LogLine`, `Cursor`, `Restart`, `Final` in `bob/log/v1/log.proto` with the fields of
  §05.9.1's `LiveLine`/`LiveRestart`/`LiveFinal`; promotion (`staging/` → `nix/`, `oci/`) per job
  with a create-only narinfo; the visibility document format (D58): **one JSON document per
  store path / OCI object** (`nix/vis/<storehash>.json`, §06 §6.6.2), so a check is one GET
  (resolved at integration); forge adapter `IssueCheckout`/`RevokeCheckout` (D62, §06 §6.9.1)
  used by `GetCheckoutCredential`; RBAC permission ids for the user RPCs that read these records.
- §01: graph IR (fd 3) and derivation generation run on the worker; `bob-builder` contract;
  embedded static Nix with a root-mode config (§05.8).
- §02: `bob build --remote` creates builds through §06's `BuildService`; error templates use
  the §02 taxonomy (`✗ …` / `hint:`).

---

## 05.1 Rules for all S3 access

#### R-STORE-100 GET-only hot paths
- **Statement:** bob-server never issues `ListObjects`/`ListObjectsV2` on the control or fleet
  bucket, and never calls `dynamitedb.Query`. Every read is a GET of a key computed from known
  data (ids, counters, sequence numbers, index documents); every write is a PUT/DELETE on a known
  key, conditional where the record is shared.
- **Acceptance:**
  - Given the harness S3 counting proxy in front of RustFS, when the full server e2e suite runs
    (registration, 300-job drain, chaos, every UI list page, retention), then the proxy counts
    **0** LIST requests on `bob-control` and `bob-fleet`.
  - `go run ./tools/ci` fails if any package other than `internal/store/sweep` references
    `ListObjectsV2`, `ListObjects`, `NewListObjectsV2Paginator` or `dynamitedb.Query`.
- **Source:** D60, R-STORE-004, ADR 0011 (amended).

#### R-STORE-101 Cold sweeps are the only LIST
- **Statement:** LIST is allowed only in `internal/store/sweep`, only on the analytics and artifacts
  buckets, and only from two controllers that run at most once per hour: lake partition drop
  (§05.10.6) and the staging sweep (§05.2.4). `bob-server store fsck` (manual) may also LIST.
- **Acceptance:** the counting proxy attributes every LIST to the `User-Agent` suffix
  `bob-sweep/<controller>`; over a 2 h soak run, there are ≤ 2 sweep runs per controller and 0
  LISTs from other code.
- **Source:** D60 (dynamitedb only), R-STORE-007. **[open]** If the owner wants zero LIST
  anywhere, lake needs a `DropTable`/delete API (S10 F2) and staging needs a working S3
  lifecycle rule. Recommendation: keep the sweep as an interim.

#### R-STORE-102 Supported backends and start-up conformance probe
- **Statement:** Only AWS S3 and fully AWS-compatible servers with conditional writes are
  supported (AWS S3, RustFS ≥ 1.0.3 as the reference). SeaweedFS and Garage are not supported.
  At start, each bob-server process probes every configured bucket under
  `_bob/probe/<random>` and refuses to start when a check fails.
- **Acceptance:** Given RustFS 1.0.3, when bob-server starts, all checks pass:
  1. PUT `If-None-Match: *` twice → second is 412;
  2. PUT `If-Match: <stale>` → 412;
  3. GET `If-None-Match: <current ETag>` → 304;
  4. GET missing key → 404 `NoSuchKey`;
  5. DELETE `If-Match: <stale>` → 412;
  6. GET of `_bob/probe/<r>/` after deleting `_bob/probe/<r>/x` → 404;
  7. every GET returns an `ETag` header.

  Given a fake backend that ignores `If-None-Match`, the process exits 78 with
  `✗ storage backend <endpoint> is not supported: conditional create (If-None-Match) is ignored`
  and `hint: bob needs an AWS-S3-compatible server with conditional writes, e.g. RustFS ≥ 1.0.3 or AWS S3`.
- **Source:** D63, D64, R-STORE-006, S9 §3.1, V1 (§05.13).

#### R-STORE-005 Panic-safe store calls (kept)
- **Statement:** every dynamitedb call runs inside `store.call`, which recovers panics into
  `store.ErrPanic` (logged with the stack, metric `bob_store_panics_total`).
- **Acceptance:** unit tests feed each misuse panic from S9 §3.6 (filter operator in an update,
  nil model, missing `ETag` header from a fake backend). Each returns `ErrPanic` and the process
  keeps running.
- **Source:** R-STORE-005, STATUS (panics occur only on non-compliant backends or misuse).

#### R-STORE-103 dynamitedb usage rules
- **Statement:** bob uses dynamitedb HEAD (D46) unchanged (D64) with these rules:
  1. All model fields are `DataField` with short CBOR names and `omitempty`; there are no static
     fields (S9 B8).
  2. No dynamitedb filters: decisions are made in Go on the fetched value (B5–B7).
  3. No `WithExpires` (B10); expiry is a field that a controller acts on.
  4. A shared record is changed only by `store.CAS`: `dynamitedb.Get`, then decide, then
     `dynamitedb.Put` of a fresh update value (store.Set operators; fields left as read are
     written through the record type's `Draft`) with the Get's `ETagField` (one `PUT If-Match`).
     A new record is written with `dynamitedb.Create`. Only a record with exactly one legitimate
     writer may use `store.PutOwned` (unconditional `Put`). A fetched record is never passed
     to a write (dynamitedb panics).
  5. `dynamitedb.Update` is not used.
  6. After a failed call, `ctx.Err()` is checked first (B11). A 404 from `Put` with an ETag
     maps to `store.ErrNotFound` (B13).
  7. On `ErrConcurrencyConflict`/`ErrAlreadyExists` after an attempt whose answer was lost (the
     SDK retried, or no response came), the store re-reads the key and treats the write as
     successful when the stored object holds exactly the bytes this call sent; the records'
     write ids (`wid` + epoch, or request id `rq`) keep the values of different writers apart.
- **Acceptance:** a table-driven test per record type covers create, CAS win, CAS loss, lost
  response (fault proxy drops the response after the server applied the write → caller reports
  success), and decode of the stored CBOR.
- **Source:** ADR 0011, S9 §3.6/§5.5, STATUS.

#### R-STORE-104 Key segment rules
- **Statement:** keys are `<pk>/<pkval>` or `<pk>/<pkval>/<sk>/<skval>`. Every value matches
  `^[A-Za-z0-9@._-]{1,100}$` and is never empty. A pk name is used either by pk-only types or by
  pk+sk types, never both. Encodings:
  - `num10` = `%010d`;
  - `seq20` = `%020d`;
  - `enc(s)` = `b` + lowercase base32 without padding of UTF-8 `s` when `len(s) ≤ 60`, else `h`
    + the first 40 hex chars of SHA-256(s);
  - `hash64(s)` = hex SHA-256 (64 chars);
  - `sh2(id)` = `%02d` of `fnv32a(id) mod shards`.

  Ids: `ULID` = 26-char Crockford ULID (48-bit ms time + 80 random bits). Build ids are
  `b-<ULID>` with the ULID in upper case (`b-01JB7Q8V3K…`); older lower-case ids still parse.
  Keys that join an id and a name in one segment write `<id>.<enc(name)>` (e.g.
  `bbr/<projectID.enc(branch)>`); when that exceeds 100 characters the name part is
  `h` + the first 40 hex chars of SHA-256(name), so the 2- or 4-segment rule always holds.
- **Acceptance:** fuzz test: no input to the key builders yields an invalid or colliding
  segment (10⁶ random strings, including `/`, empty, 1 KiB). `sh2` is total over non-empty strings;
  ids are validated at the API boundary, not in the key builders. An empty segment returns
  `store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}`.
- **Source:** S9 §5.1, B12.

---

## 05.2 Buckets and policies

### 05.2.1 Buckets

| Bucket (default name) | Engine | Orchestrator | Worker | Contents |
|---|---|---|---|---|
| `bob-control` | dynamitedb | read/write | **none** (Deny) | identity, projects, builds, releases, settings, orchestrator signing keys |
| `bob-fleet` | dynamitedb | read/write | read all; write only the prefixes in §05.2.3 | fleet config, workers, queues, jobs, leases, heartbeats; ids only (R-SEC-003) |
| `bob-analytics` | lake | read/write | read/write (D38) | log and run tables in partitions (§05.10) |
| `bob-artifacts` | plain S3 | read/write | read all; write `staging/` | Nix cache, OCI, release assets |

#### R-STORE-002 Bucket split (kept, refined)
- **Statement:** as in the table above. All four bucket names, the endpoint and the region are
  configuration (`storage.*`, §05.11.1). Credentials come from the AWS SDK default chain (env,
  shared file, IMDSv2, web identity). How a host gets them is a deployment concern (D57).
- **Acceptance:** Given the shipped policies on RustFS, when the harness runs the S11 policy
  matrix with the new prefixes (§05.2.3), then every allowed cell succeeds and every denied cell
  returns 403 (≥ 40 operations, including a worker GET, PUT and DELETE on `bob-control`).
- **Source:** R-STORE-002, R-SEC-001/002, D13, D38, D57, ADR 0013.

### 05.2.2 Credentials

#### R-SEC-100 bob never mints S3 credentials
- **Statement:** bob has no STS, AssumeRole or credential-vending code path. Orchestrator and
  worker processes use the credentials their host provides. Every further secret a worker gets
  (worker token, forge checkout credential) is obtained **through S3 first** (sealed token in
  `worker/<wid>`), then through RPCs authenticated with that token. Never the other way round.
- **Acceptance:** `go run ./tools/ci` fails if a server package imports
  `github.com/aws/aws-sdk-go-v2/service/sts` or `service/iam`. A protected test checks that an
  RPC without a worker token cannot return any S3 credential or worker token.
- **Source:** D57; ADR 0013 decision 8 struck.

#### R-SEC-101 Compromised worker host runbook
- **Statement:** the operator cuts off a compromised worker host in two steps:
  1. revoke its worker id (`bob-server worker revoke <wid>` or the UI). The token and PoP are then
     refused within 10 s;
  2. rotate the shared worker S3 credential in the deployment.

  bob documents this in `docs/operations/worker-compromise.md`.
- **Acceptance (manual):** the runbook exists and its commands work in the harness's local
  deployment.
- **Source:** D57, OD-S11-1 a.

### 05.2.3 Shipped policies

#### R-SEC-002 Policies (kept, prefixes updated)
- **Statement:** bob ships `deploy/policies/{worker,orchestrator}.json` (identity policies) and
  `deploy/policies/control-bucket.json` (bucket policy that denies everyone except the
  orchestrator principals). The worker policy grants:

| Bucket | Actions | Resources |
|---|---|---|
| control | explicit Deny `s3:*` | bucket and `/*` |
| fleet | `s3:ListBucket` (needed only so that missing keys return 404, not 403; bob never lists) | bucket |
| fleet | `s3:GetObject` | `/*` |
| fleet | `s3:PutObject` | `wreg/*`, `wrs/*`, `wrt/*`, `whb/*`, `job/*`, `qs/*`, `qh/*` |
| fleet | `s3:DeleteObject` | none |
| analytics | Get/Put/Delete object, multipart abort/list parts; ListBucket | bucket, `/*` |
| artifacts | `s3:GetObject` | `/*` |
| artifacts | `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | `staging/*` |

  Orchestrator: `s3:*` on all four buckets. Workers cannot write `worker/`, `fleet/`, `orch/`,
  `ctl/`, `qt/`, `jact/` or `widx/`.
- **Acceptance:** extends S11's 27-operation matrix. Each new prefix gets one PUT allowed and
  one denied: a worker PUT on `worker/x`, `fleet/cfg`, `qt/x` and `jact/00` → 403. A worker
  PUT on `job/x` → 200.
- **Source:** R-SEC-002, S11 §5.4.

#### R-SEC-102 Start-up policy self-check
- **Statement:** when `storage.worker_probe_credential` is configured, the orchestrator checks
  at start that this credential gets 403 on `HeadObject bob-control/_bob/probe` and on
  `PutObject bob-fleet/worker/_probe`. It also checks that `GetBucketPolicy bob-control`
  contains the deny statement. If a check fails, the orchestrator refuses to start
  (`✗ control bucket is readable with the worker credential`).
- **Acceptance:** the harness starts an orchestrator against a control bucket without the
  bucket policy → exit 78 with that text.
- **Source:** S11 §5.4, T3.

### 05.2.4 Artifacts bucket prefixes

| Prefix | Writer | Notes |
|---|---|---|
| `staging/<buildID>/<jobID>/<attempt>/…` | worker | NARs, unsigned narinfo, OCI blobs, `COMPLETE.json` written last (S12 §5.1, scoped per job, S14) |
| `nix/nar/<storehash>.nar.zst`, `nix/ls/<storehash>.ls`, `nix/narinfo/<storehash>.narinfo` | orchestrator | narinfo create-only, written last |
| `nix/vis/<storehash>.json`, `oci/vis/sha256/<hex>.json` | orchestrator (C `If-None-Match: *`, CAS `If-Match`) | visibility document, one per served object: `{"v":1,"projects":[…],"public":[<release id>…]}` (format §06 §6.6.2); a check is 1 GET (cached) |
| `src/<projectID>/<sha256hex>.tar.zst` | orchestrator (upload endpoint, §06 §6.9.7) | `--remote` source snapshots, create-only; lifecycle 7 days |
| `oci/…` | orchestrator | S12 layout |
| `rel/<projectID>/<num10>/<asset>` | orchestrator | release assets, immutable. Release id (API, visibility documents) = `<projectID>.<num10>` |

#### R-STORE-105 Staging cleanup without hot-path LIST
- **Statement:**
  - After a job's outputs are promoted, the promoting orchestrator deletes the staged objects
    named in `COMPLETE.json` (known keys).
  - Staging of failed or abandoned attempts is removed by the S3 lifecycle rule
    `staging/` → expire after 7 days, when the backend supports it.
  - Otherwise, the hourly `staging-sweep` (R-STORE-101) lists `staging/` and deletes objects
    older than 7 days.
- **Acceptance:** after a 300-job drain with 20 % killed workers, the `staging/` objects of
  successful attempts are 0 within 1 promotion pass. After the sweep with the clock advanced 7 d,
  `staging/` is empty.
- **Source:** S12 §5.1, R-STORE-007.

---

## 05.3 `internal/store` primitives

All signatures below are exact. Doc comments are part of the contract. Record kinds are
dynamitedb models (one struct type per kind, key names in `pk`/`sk` struct tags, §05.4), so the
primitives are generic over the record type of their documents and take the record kind as
builder functions; they never take key names at run time (I-B15, A111).

```go
package store // import "github.com/megakuul/bob/internal/store"

// Bucket is one dynamitedb bucket plus a raw S3 client for conditional GETs.
// It is safe for concurrent use.
type Bucket struct{ /* unexported */ }

// BucketConfig selects one bucket. Credentials come from the AWS SDK default chain.
type BucketConfig struct {
	Endpoint string // e.g. "https://s3.eu-central-1.amazonaws.com" or "http://127.0.0.1:9000"
	Region   string // e.g. "us-east-1"
	Name     string // bucket name, e.g. "bob-fleet"
	Role     string // "control" | "fleet" (metrics label only)
}

// Open connects to the bucket, runs the conformance probe (R-STORE-102) once per process,
// and returns a Bucket. It never lists.
func Open(ctx context.Context, cfg BucketConfig) (*Bucket, error)

// Errors. All are wrapped with the key, e.g. "store: job/01J…: not found".
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("changed concurrently")
	ErrPanic    = errors.New("dynamitedb panicked")
	ErrNoChange = errors.New("no change") // returned by a CAS func to skip the write
)

// The library's types and value constructors for packages that may not import dynamitedb
// (R-STORE-106): aliases, not copies.
type (
	KeyField         = dynamitedb.KeyField
	ETagField        = dynamitedb.ETagField
	DataField[T any] = dynamitedb.DataField[T]
)
func Set[T any](v T) DataField[T]              // dynamitedb.Set: update operator
func ID(id string) KeyField                    // dynamitedb.Key: key value
func Carry[T any](f DataField[T]) DataField[T] // Set(f.Value()) of a fetched field (nil for zero)
func ETagOf(e ETagField) string                // "" for a fresh value

// Get reads the record whose key fields are set in key (dynamitedb.Get, one GET). The result
// carries the ETag. Fetched records are read-only results: read them with .Value().
func Get[T any](ctx context.Context, b *Bucket, key *T) (*T, error)

// Cache keeps the last value and ETag per key for GetCached. Zero value is ready to use.
type Cache[T any] struct{ /* unexported */ }

// GetCached sends GET If-None-Match with the cached ETag. On 304 it returns the cached value
// and changed=false (1 request). On 200 it reads the record with dynamitedb.Get
// (2 requests in total) and updates the cache. On 404 it drops the entry and returns ErrNotFound.
func GetCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (v *T, changed bool, err error)

// Create writes v only if its key does not exist (dynamitedb.Create, PUT If-None-Match: *).
// v is a fresh value (store.Set operators).
func Create[T any](ctx context.Context, b *Bucket, v *T) error

// CAS reads the record at key, calls fn with it (nil if absent), and writes fn's result with
// If-Match of that read (dynamitedb.Put with the read's ETag) or with If-None-Match when absent
// (dynamitedb.Create). fn returns a fresh value, or the fetched record (or a copy) with some
// fields replaced by store.Set operators: fields still holding read values are written through
// the record type's Draft. On ErrConflict it re-reads and calls fn again, up to 8 times with
// jittered backoff (10–200 ms). fn returning ErrNoChange ends CAS without writing (CAS then
// returns the current record); fn returning nil, nil deletes the record (DELETE If-Match).
// After a write CAS returns the record as stored (one more GET).
func CAS[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) (*T, error)

// Change is CAS without the read-back GET, for callers that do not use the stored record
// (the per-job hot paths of R-FLEET-009).
func Change[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) error

// PutOwned writes v unconditionally (dynamitedb.Put without ETag). Only for records with
// exactly one legitimate writer (orch/<self>, whb/<self>).
func PutOwned[T any](ctx context.Context, b *Bucket, v *T) error

// Delete removes the record if its ETag still matches v's ETag (2 requests, dynamitedb.Delete).
func Delete[T any](ctx context.Context, b *Bucket, v *T) error
```

Every record type has a `Draft() *T` method: the fresh update value that writes a fetched
record again (keys, ETag, `store.Carry` of every data field). A field set with `store.Set`
cannot be read back (dynamitedb panics on `Value()` of an operator), so code that changes a
field in several steps edits a plain local value and sets it once.

### 05.3.1 Sequence log (queues, registration inbox, promotion log)

A **sequence log** is a dense, append-only series of slots `<pk>/<name>/s/<seq20>` with
`seq ≥ 1`, plus two hint records: tail `<tailpk>/<name>` and head `<headpk>/<name>`. Hints only
make things faster. The truth is which slots exist (`If-None-Match` makes each slot number
unique).

```go
// Counter is a record kind that holds one number: a sequence-log hint (`SeqHint`: n).
type Counter struct{ /* unexported */ }

// NewCounter describes a counter record kind: build returns the fresh value of the record
// holding n (key + Set(n)), n reads a fetched one. It performs no I/O.
func NewCounter[H any](b *Bucket, build func(n uint64) *H, n func(h *H) uint64) *Counter

// SeqLog is a dense append-only log of slots on known keys. Readers find new entries by
// GETting the next slot number; a 404 means "nothing new". It never lists.
type SeqLog[S any] struct {
	Bucket *Bucket
	// unexported: slot key builder, tail and head counters
}

// NewSeqLog returns the log; it performs no I/O. slot returns the key value of slot seq;
// head may be nil (no head hint).
//
// Example (records.go):
//
//	q := store.Queue(fleet, "x86_64-linux.k3j9d0a1b2") // qs/<class>/s/<seq20>, qt/, qh/
//	seq, err := q.Append(ctx, &QueueSlot{Job: store.Set(jobID)})
func NewSeqLog[S any](b *Bucket, slot func(seq uint64) *S, tail, head *Counter) *SeqLog[S]

// Append reads the tail hint t (1 GET), Creates slot t, t+1, … until one succeeds, then raises
// the tail hint to seq+1 with a best-effort CAS (a conflict is ignored). Returns the slot
// number. v is a fresh value; Append sets its keys.
func (l *SeqLog[S]) Append(ctx context.Context, v *S) (seq uint64, err error)

// Read GETs one slot. ErrNotFound means seq is at or past the end of the log.
func (l *SeqLog[S]) Read(ctx context.Context, seq uint64) (*S, error)

// Tail and Head return the hints (GetCached; 1 request when unchanged).
func (l *SeqLog[S]) Tail(ctx context.Context) (uint64, error)
func (l *SeqLog[S]) Head(ctx context.Context) (uint64, error)

// AdvanceHead raises the head hint to n if it is lower (CAS, monotonic; never lowers it).
func (l *SeqLog[S]) AdvanceHead(ctx context.Context, n uint64) error
```

Invariants:
- Slots are never deleted while `seq ≥ head − 1000`. GC deletes older slots by known key.
- A writer never skips a number unless that slot exists, so the log has no gaps (V1: 20
  appenders × 50 → 1,000 dense unique slots, 0 lost writes).

### 05.3.2 Paged index (newest-first lists without LIST)

```go
// PagedIndex is a list of entries numbered 0,1,2,… stored P per page record
// <pagePK>/<scope>/p/<num10>. The entry count is a counter field in a record the caller names
// (e.g. ProjectCounters.Builds). Page k holds entries [k*P, (k+1)*P).
type PagedIndex[E any] struct {
	Bucket *Bucket
	P      int // entries per page, 50 unless stated
	// unexported: page builder and accessor
}

// NewPagedIndex performs no I/O. build returns the fresh value of page k holding entries e;
// entries reads a fetched page.
//
// Example (records.go):
//
//	idx := store.NewPagedIndex(control, 50, func(k uint64, e []BuildEntry) *BuildPage {
//		n, _ := store.Num10(k)
//		return &BuildPage{Project: store.ID(projectID), Page: store.ID(n), Entries: store.Set(e)}
//	}, func(p *BuildPage) []BuildEntry { return p.Entries.Value() })
//	page, err := idx.Page(ctx, idx.LastPage(count))
func NewPagedIndex[P, E any](b *Bucket, p int, build func(k uint64, e []E) *P, entries func(*P) []E) *PagedIndex[E]

// Set writes entry n (CAS on its page; creates the page on first use).
func (x *PagedIndex[E]) Set(ctx context.Context, n uint64, e E) error

// Page returns page k (GetCached). Missing entries in a page are zero values.
func (x *PagedIndex[E]) Page(ctx context.Context, k uint64) ([]E, error)

// LastPage returns the page number of entry count-1.
func (x *PagedIndex[E]) LastPage(count uint64) uint64
```

### 05.3.3 Sharded set (membership docs for reconcilers)

```go
// ShardSet is a set of string ids spread over N shard records <pk>/<sh2>, each a bounded
// map id -> E (≤ 1,000 entries). Reconcilers read all N shards with GetCached each pass,
// so an unchanged set costs N × 304.
type ShardSet[E any] struct {
	Bucket *Bucket
	N      int // shard count, from FleetConfig
	// unexported: shard builder and accessor
}

// NewShardSet performs no I/O. build returns the fresh value of shard sh (its sh2 segment)
// holding m; entries reads a fetched shard.
//
// Example (records.go):
//
//	active := store.ActiveJobs(fleet, cfg.JobShards) // jact/<sh2>
//	err := active.Add(ctx, jobID, ActiveJob{Build: buildID})
func NewShardSet[S, E any](b *Bucket, n int, build func(sh string, m map[string]E) *S, entries func(*S) map[string]E) *ShardSet[E]

func (s *ShardSet[E]) Add(ctx context.Context, id string, e E) error    // CAS on shard sh2(id)
func (s *ShardSet[E]) Remove(ctx context.Context, id string) error      // CAS; absent = nil
func (s *ShardSet[E]) All(ctx context.Context) (map[string]E, error)    // N × GetCached
```

Shard count changes go through a new `FleetConfig.Gen`. Entries are rehashed by the
`index-repair` controller, which reads the old and the new shards.

### 05.3.4 Lease

```go
// LeaseState is the data of a lease record: owner, epoch (incremented on every takeover),
// expires, and a cursor the holder owns (kept as it is).
type LeaseState struct {
	Owner   string
	Epoch   uint64
	Expires time.Time
	Cursor  []byte
}

// Lease is a CAS-held lease on one record. Used for controller leadership (ctl/<name>) and the
// promotion lock (plock/<jobID>).
type Lease struct {
	Bucket *Bucket
	Owner  string        // this process's orchestrator id
	TTL    time.Duration // 2 × controller interval
	// unexported: record builder and accessor
}

// NewLease performs no I/O. build returns the fresh value of the lease record holding s;
// state reads a fetched one.
func NewLease[L any](b *Bucket, build func(s LeaseState) *L, state func(*L) LeaseState, owner string, ttl time.Duration) *Lease

// NewControllerLease is the lease ctl/<name> of a controller.
//
// Example:
//
//	l := store.NewControllerLease(fleet, "job-reconciler", self.ID, 20*time.Second)
//	if held, epoch, err := l.TryAcquire(ctx); held { reconcile(ctx, epoch) }
func NewControllerLease(fleet *Bucket, name, owner string, ttl time.Duration) *Lease

// TryAcquire creates the lease if absent, renews it if owned, or takes it over (epoch+1) when
// expires + skew < now. 1 GET + at most 1 PUT.
func (l *Lease) TryAcquire(ctx context.Context) (held bool, epoch uint64, err error)
```

#### R-STORE-106 Primitives are the only data access
- **Statement:** server packages use only the §05.3 API. No package other than `internal/store`
  imports `github.com/megakuul/dynamitedb` or `service/s3`. The exceptions are
  `internal/logstore` (lake) and `internal/artifacts` (plain S3 on the artifacts bucket).
- **Acceptance:** layering check in `go run ./tools/ci`. Unit tests for each primitive run
  against RustFS: SeqLog with 20 concurrent appenders × 50 → 1,000 dense unique slots;
  PagedIndex with 20 writers on one page → no lost entry; Lease with 3 contenders → exactly one
  holder per epoch.
- **Source:** D60, V1.

---

## 05.4 Key schemas

Access op legend: **G** GET, **GC** conditional GET (`GetCached`), **C** Create, **CAS**
Get+Put(If-Match), **P** PutOwned, **D** Delete(If-Match). "Hot" marks paths that run
periodically or per request.

### 05.4.1 Fleet bucket (`bob-fleet`)

| Key | Record / value fields (CBOR name) | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `fleet/cfg` | `FleetConfig`: `g` gen; `m` members [{`o` orchID, `u` endpoint, `fp` SPKI pin, `s` since}]; `c` classes [{`id`, `pl` platform, `cp` caps, `lb` labels}]; `k` orchestrator public keys [{`kid`, `pub`, `na` notAfter}]; `pr` params {poll, heartbeat, lease TTL, flush, shard counts} | orchestrators (CAS) | workers, orchestrators (GC every 30 s) | yes |
| `orch/<orchID>` | `OrchRecord`: `u`, `fp`, `h` heartbeat, `st` started, `v` version | that orchestrator (P, every 10 s) | membership controller (G) | yes |
| `ctl/<controller>` | `ControllerLease`: `o` owner, `ep` epoch, `ex` expires, `cu` cursor (controller-specific, e.g. registration seq) | orchestrators (CAS) | orchestrators (G) | yes |
| `wreg/<wid>` | `WorkerReg`: `b` canonical CBOR RegBody, `s` Ed25519 sig (S11 §5.2) | worker (C) | registrar (G) | no |
| `wrs/reg/s/<seq20>` | `RegSlot`: `w` wid, `at` time | worker (SeqLog Append) | registrar (G next seq) | yes |
| `wrt/reg` | `SeqHint`: `n` | worker (best-effort CAS) | worker (G) | no |
| `worker/<wid>` | `WorkerRecord`: `st` state, `g` gen, `n` serial, `sp` sigpub, `su` suite, `kp` kempub, `pl`, `cp`, `lb`, `sl` slots, `te` token exp, `ct` sealed token (S11 §5.2), `rq` request id | registrar, admin RPC (CAS) | worker (GC 60 s; G 1 s while waiting), orchestrators (GC ≤ 10 s cache) | yes |
| `whb/<wid>` | `Heartbeat`: `at`, `fr` free slots, `j` [{job, epoch}], `s` Ed25519 sig over (wid, at, j) | worker (P, every 15 s) | job reconciler, registrar (G) | yes |
| `widx/<sh2>` | `WorkerIndexShard`: map wid → {`pl`, `st`, `hb` last heartbeat seen, `fr`, `nj`} | registrar (CAS) | UI fleet page, wake selection (GC) | yes |
| `qt/<classID>` | `SeqHint` tail | orchestrators (CAS best effort) | orchestrators, workers (G) | yes |
| `qh/<classID>` | `SeqHint` head | workers, orchestrators (CAS monotonic) | workers (G) | yes |
| `qs/<classID>/s/<seq20>` | `QueueSlot`: `j` jobID, `at` enqueued, `tk` taken-by (wid \| `stale` \| `orphan`) | orchestrators (C); workers/orchestrators (CAS `tk`) | workers (G) | yes |
| `job/<jobID>` | `JobRecord` (§05.4.3) | build controller (C, CAS cancel/requeue); worker (CAS claim/start/result) | workers, reconciler (G) | yes |
| `jact/<sh2>` | `ActiveJobShard`: map jobID → {`b` buildID, `c` class, `at` created} | build controller (CAS) | job reconciler (GC every pass) | yes |

Class id = `<platform>.<first 10 base32 chars of SHA-256(canonical caps ‖ labels)>`, e.g.
`aarch64-linux.k3j9d0a1b2`. `FleetConfig.c` holds at most 256 classes.

### 05.4.2 Control bucket (`bob-control`)

| Key | Value | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `okey/<kid>` | Ed25519 seed, created, notAfter | key controller (C) | orchestrators (G at start + on unknown kid) | no |
| `proj/<projectID>` | `ProjectRecord` (name, repo, forge id, settings, visibility, `lb` last build summary) | ProjectService (CAS) | API, controllers (GC) | yes |
| `pname/<enc(name)>` | projectID | ProjectService (C: uniqueness) | API (G) | yes |
| `pctr/<projectID>` | `ProjectCounters`: `b` builds, `r` releases | build/release controller (CAS) | API (GC) | yes |
| `pidx/all/p/<num10>` + `pidxm/all` | projects paged index (P = 200): {id, name, last build state, time}; `pidxm` holds the count | ProjectService (CAS) | UI projects page (GC) | yes |
| `build/<buildID>` | `BuildRecord` (§05.4.4) | build controller (C, CAS) | API, controllers (G/GC) | yes |
| `bpage/<projectID>/p/<num10>` | `BuildPage`: 50 × `BuildEntry` {n, buildID, state, branch, commit[:12], trigger, created, dur} | build controller (CAS) | UI/API build list (GC) | yes |
| `bbr/<projectID.enc(branch)>/p/<num10>` + `bbrm/<projectID.enc(branch)>` | per-branch paged index of build numbers (P = 50), count in `bbrm` | build controller (CAS) | branch filter, supersede rule (GC) | yes |
| `bidx/running` | map buildID → {project, n, started} (≤ 2,000; over that, `bidx/running.<sh2>` with N in settings) | build controller (CAS) | UI overview (GC) | yes |
| `bidx/failed` | ring of the last 200 failed builds | build controller (CAS) | UI overview (GC) | yes |
| `bnodes/<buildID>` | `BuildNodes`: drv count, bitmaps assigned/promoted/failed, job → drv index ranges (S14 §5.2) | build controller (CAS) | build controller, UI graph (G) | yes |
| `rel/<projectID>/n/<num10>` | `ReleaseRecord` (§05.6.4) | release controller (C, CAS) | API, cache auth (GC) | yes |
| `relv/<projectID.enc(name)>/v/<enc(version)>` | release number (uniqueness of name + version) | release controller (C: uniqueness) | API, `bob install` (G) | yes |
| `rpage/<projectID>/p/<num10>` | 50 release entries | release controller (CAS) | UI (GC) | yes |
| `ridx/all/p/<num10>` + `ridxm/all` | registry-wide release index (P = 50) | release controller (CAS) | UI registry (GC) | yes |
| `user/<userID>` | `UserRecord`: name, email, kind, pw hash, `ro` role bindings [{role, project \| `*`, source manual\|oidc}] (truth; §06's `RoleBinding`), `tk` token ids (≤ 100), `ss` session ids (≤ 100), disabled | AccessService (CAS) | auth (GC ≤ 10 s) | yes |
| `uname/<enc(name)>`, `oidcsub/<hash64(iss‖0‖sub)>` | userID | AccessService, OIDC login (C) | login (G) | yes |
| `uidx/all/p/<num10>` + `uidxm/all` | users paged index (P = 200) | AccessService (CAS) | UI access (GC) | no |
| `role/<roleID>` | `RoleRecord`: name, perms (≤ 2,000), builtin | AccessService (CAS) | auth (GC ≤ 10 s) | yes |
| `rname/<enc(name)>` | roleID | AccessService (C) | API (G) | no |
| `roles/all` | role index (≤ 1,000) | AccessService (CAS) | UI (GC) | no |
| `rmem/<roleID>` | derived member list (pending/ok per user) | AccessService (CAS), index-repair | UI "users of role" (GC) | no |
| `oidcmap/all` | OIDC claim → role mappings (≤ 500) | AccessService (CAS) | OIDC login (GC) | yes |
| `sa/<saID>`, `saidx/all` | `ServiceAccountRecord` (incl. `ro` role bindings as in `UserRecord`, `tk` token ids); index (≤ 1,000) | AccessService (CAS) | auth, UI (GC) | yes |
| `tok/<hash64(token)>` | `TokenRecord`: owner, kind, scopes, exp, created | AccessService (C, D) | every API call (G, cache 10 s) | yes |
| `forge/<forgeID>`, `forges/all` | `ForgeRecord` (forge integration; secrets sealed with the orchestrator data key); index | ForgeService (CAS) | pipeline controller (GC) | yes |
| `ftok/<sh2(jobID)>` | `CheckoutRecord`s: outstanding per-job forge tokens {jobID → epoch, forge, handle (token id), expiresAt, forgeExpires}; never the token value (D62) | `GetCheckoutCredential`, revoke at job end, sweeper (CAS) | same + sweeper (GC) | yes |
| `set/<name>` | `SettingRecord`: setting value | SettingsService (CAS) | all (GC 30 s) | yes |
| `bkey/<hash64(delivery key)>` | `BuildKey`: `b` build id, `at` created: the build one forge delivery created (idempotent build creation from a webhook event; delivery key = the `fdel` key value) | webhook intake through build creation (C) | same, when the create conflicts (G) | no |
| `plock/<jobID>` | promotion lock, a `Lease` (§05.3.4: `o`, `ep`, `ex`, `cu`; TTL 1 min); deleted after the job's promotion; correctness does not depend on it, it makes concurrent promotions of one job deterministic | promoting orchestrator (C, CAS, D) | same (G) | yes |
| `promo/<yyyymmdd>/s/<seq20>` + `promot/<yyyymmdd>` | promotion log: {storehash, project, build, at} (SeqLog without head) | promoting orchestrator (Append, **before** the narinfo write) | cache GC (G, walks days older than retention) | no |

### 05.4.3 `JobRecord` and `JobSpec`

```go
// JobRecord is the fleet-side truth of one job while it is not finalised.
type JobRecord struct {
	ID   dynamitedb.KeyField  `pk:"job" cbor:"-"`
	ETag dynamitedb.ETagField `etag:"true" cbor:"-"`

	Spec    dynamitedb.DataField[[]byte] `cbor:"sp,omitempty"` // canonical CBOR JobSpec, ≤ 64 KiB
	SpecSig dynamitedb.DataField[[]byte] `cbor:"ss,omitempty"` // Ed25519("bob-jobspec-v1"‖0‖LV(sp)) by FleetConfig key kid
	Kid     dynamitedb.DataField[string] `cbor:"kid,omitempty"`

	State    dynamitedb.DataField[string]    `cbor:"st,omitempty"` // queued|claimed|running|succeeded|failed|cancelled
	Owner    dynamitedb.DataField[string]    `cbor:"o,omitempty"`  // wid while claimed/running
	Epoch    dynamitedb.DataField[int64]     `cbor:"ep,omitempty"` // +1 on every claim
	Attempt  dynamitedb.DataField[int64]     `cbor:"at,omitempty"` // 1 on first claim, +1 on each requeue-claim
	Claimed  dynamitedb.DataField[time.Time] `cbor:"ca,omitempty"`
	Changed  dynamitedb.DataField[time.Time] `cbor:"ch,omitempty"`
	Queued   dynamitedb.DataField[time.Time] `cbor:"qa,omitempty"` // last time it became queued
	Cancel   dynamitedb.DataField[bool]      `cbor:"cx,omitempty"` // cancel requested while running

	Result    dynamitedb.DataField[[]byte] `cbor:"rs,omitempty"` // canonical CBOR JobResult
	ResultSig dynamitedb.DataField[[]byte] `cbor:"rg,omitempty"` // Ed25519 by Owner's sigpub over ("bob-jobres-v1"‖jobID‖epoch‖rs)

	// Promotion of a succeeded job (§06 §6.6.4), written by the promoting orchestrator (CAS):
	// "" (pending) | "done" | "blocked: staging incomplete" | "failed: artifact verification".
	Promotion       dynamitedb.DataField[string]   `cbor:"pm,omitempty"`
	NonReproducible dynamitedb.DataField[[]string] `cbor:"nr,omitempty"` // store paths (R-NFR-006)
}

// JobSpec is written once by the build controller and signed; workers refuse unsigned or
// mis-signed specs (a worker can overwrite another job's record, ADR 0013 T10).
type JobSpec struct {
	V        int      `cbor:"1,keyasint"` // 1
	JobID    string   `cbor:"2,keyasint"`
	BuildID  string   `cbor:"3,keyasint"`
	Project  string   `cbor:"4,keyasint"` // projectID only, no names (R-SEC-003)
	Kind     string   `cbor:"5,keyasint"` // evalbuild | eval | batch
	Targets  []string `cbor:"6,keyasint"` // ≤ 64; labels for evalbuild, drv paths for batch
	Class    string   `cbor:"7,keyasint"`
	Repo     string   `cbor:"8,keyasint"` // forge id + repo id (no URL credentials)
	Commit   string   `cbor:"9,keyasint"`
	Staging  string   `cbor:"10,keyasint"` // staging/<buildID>/<jobID>/<attempt>/ (§05.2.4)
	EstSlotS float64  `cbor:"11,keyasint"`
	Prefer   string   `cbor:"12,keyasint"` // affinity hint (wake order only)
	Created  int64    `cbor:"13,keyasint"`
}

// Targets of an eval/evalbuild job carry the build's selection as selector entries next to
// the plain labels; selectors start with "@", labels never do: @event:push:<branch>,
// @event:tag:<name>, @event:pull_request, @pipeline:<name>, @test, @variant:<name>,
// @upload:<sha256 hex>, and @version:<v>, the release version the worker builds the build's
// releases with (BuildRecord.vr, or the tag without "v"; §06 §6.8, A146).

// JobResult is written by the worker that holds the lease.
type JobResult struct {
	V        int      `cbor:"1,keyasint"`
	Status   string   `cbor:"2,keyasint"` // ok | failed | infra
	Complete string   `cbor:"3,keyasint"` // key of COMPLETE.json, "" on failure
	CompSHA  string   `cbor:"4,keyasint"` // hex SHA-256 of COMPLETE.json
	Failed   []string `cbor:"5,keyasint"` // failing packages (labels)
	Error    string   `cbor:"6,keyasint"` // translated error (§02 taxonomy), ≤ 8 KiB
	LogLast  map[string]int64 `cbor:"7,keyasint"` // package → last persisted seq
	Finished int64    `cbor:"8,keyasint"`
}
```

### 05.4.4 `BuildRecord`

Fields: `pr` projectID, `n` number, `st` state, `ph` phase (`evaluating|building|promoting`),
`tr` trigger {kind push|pr|api|remote|retry, by userID, forge event id}, `br` branch, `cm` commit,
`au` commit author (control only), `cr` created, `sa` started, `fi` finished, `pl` platforms,
`sp` superseded-by build number, `rt` retry-of build number, `src` source {git {ref, sha,
fetch_ref} | upload sha256}, `tg` targets, `va` variants, `pi` pipeline runs [{name, state,
release id}], `rp` reported forge statuses {context → last state sent} (status controller,
§06 §6.9.4), `jb` jobs [≤ 1,024 × {id, kind,
class, st, at attempts, ep, worker, dur, e error}], `rs` result {failed packages, error}, `lg` log
partition id (§05.10.1), `rq` last request id, `vr` release version of the build
(`CreateBuildRequest.version`; `""` = none; tag builds derive it from the ref instead, §06 §6.8,
A146). A job row's `e` is the first line of the job's signed `JobResult.error`, URL credentials
redacted, ≤ 300 bytes, written when the row becomes terminal and never cleared; the job record is
deleted after the job ends, so `e` is where §06 `Job.error` comes from afterwards (A180).

### 05.4.5 Control-bucket records for §06 (added at integration)

Names as §06 uses them. Same legend as §05.4.2; all keys are known from the request, so every
access is a GET or a conditional write (D60). `hash64` = lowercase hex SHA-256.

| Key | Record / value | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `repo/<forgeID.enc(forge repo id)>` | `RepoLinkRecord`: projectID | ProjectService (C, D) | webhook intake (GC) | yes |
| `sess/<hash64(token)>` | `SessionRecord`: user, kind `cli`\|`browser`, user agent, created, last-used day, idle/max expiry (refresh token `bob_rt_` or cookie `bob_st_`) | AuthService / OAuth token endpoint (C, CAS, D) | every cookie request, refresh (G, cache 10 s) | yes |
| `oac/<hash64(code)>` | `OAuthCodeRecord`: user, client, redirect URI, PKCE challenge, expiry (60 s) | `/oauth/authorize` (C) | `/oauth/token` (G, D If-Match = single use) | no |
| `devc/<hash64(device_code)>` + `devu/<user_code>` | `DeviceCodeRecord`: user code, state pending\|approved\|denied, user, expiry (600 s), last poll; `devu` → device-code hash | `/oauth/device/code` (C), `/device` approval (CAS) | token polling (G) | no |
| `oidcst/<hash64(state)>` | OIDC login state: provider, nonce, PKCE verifier, next, expiry (10 min) | `/auth/oidc/start` (C) | callback (G, D) | no |
| `oidcp/<providerID>`, `oidcps/all` | `OidcProviderRecord` (issuer, client id, sealed secret, `require_mapping`); index (≤ 50) | OidcService (CAS) | login (GC) | yes |
| `oidcmap/all` | `OidcMappingRecord` list (§05.4.2) | OidcService (CAS) | OIDC login (GC) | yes |
| `adminidx` | `AdminIndex`: count of `admin@*` bindings of enabled principals | AccessService (CAS, together with the binding write) | same | no |
| `fdel/<forgeID.h(delivery id)>` (`h` = first 40 hex chars of SHA-256) | `ForgeDeliveryRecord`: received, event, result, build id (dedupe) | webhook intake (C: create-only) | webhook intake (G) | yes |
| `fdelr/<forgeID>` | ring of the last 200 deliveries (for `ListForgeDeliveries`) | webhook intake (CAS best effort) | UI (GC) | no |
| `skey/<keyname>`, `skeys/all` | `SigningKeyRecord`: Ed25519 seed (sealed), created; `skeys/all` holds every key's name, public key and state `ACTIVE`\|`VERIFY_ONLY`\|`RETIRED` (a rotation is one write); names `<server_name>-<n>` (default `bob-<n>`) | SettingsService (C, CAS) | promotion, `GetServerInfo` (GC) | yes |
| `ocihmac/<kid>`, `ocihmacs/all` | `OciHmacKey`: 32-byte key (sealed), created, notAfter | key controller (C) | OCI token endpoint + `/v2/*` (GC) | yes |
| `nhook/<id>`, `nhooks/all` | `NotifyWebhookRecord` (URL, sealed secret, events, project filter); index | NotifyService (CAS) | notifier (GC) | yes |
| `nout/<sh2(delivery id)>` | notify outbox shard (4 shards): delivery id → {hook, event, body (fixed at publish), attempts, created}; an entry is removed after delivery or the last attempt (§06 §6.9.8 schedule) | publishing orchestrator (CAS), notify controller (CAS) | notify controller (G every pass) | yes |

Mappings of other §06 names: `OrchestratorRecord` = `OrchRecord` (`orch/<orchID>`, its public
keys are in `FleetConfig.k`); `ForgeIntegrationRecord` = `ForgeRecord`; `SettingsRecord` =
`SettingRecord`; `CheckoutRecord` = one entry of `ftok/<sh2(jobID)>`; `ReleaseIndex` = `relv` +
`rpage` + `ridx`; `RoleBindingRecord` = the `ro` entries of `UserRecord`/`ServiceAccountRecord`;
`RefHeadRecord` is not used (the concurrency rule reads `bbr`, §05.6.2). Signing keys (`skey`,
`okey`) and every sealed secret live only in the control bucket, which workers cannot read.

#### R-STORE-004 Lists without scans (kept, made exact)
- **Statement:** every UI and API list reads index documents or paged indexes on known keys.
  The access plan per page is the table in §05.5.2. No list needs more than
  `1 + ceil(page size / P)` index GETs plus detail GETs for at most the visible rows.
- **Acceptance:** for each row of §05.5.2, a test counts the requests through the proxy and
  checks them against the column "requests (page 1)". Only rows reachable through the §06 API are
  listed in the acceptance; "Build by number" and "Release by version" join it when the by-number
  and by-version lookups exist (Gate H, G19; follow-up in `plan/m1.8-ledger.md`).
- **Source:** R-STORE-004, D60.

---

## 05.5 Hot paths

### 05.5.1 Fleet hot paths (per occurrence)

| Path | Who, how often | Requests | Ops |
|---|---|---|---|
| Idle poll | worker, per eligible class, every `fleet.poll_interval` (30 s ± 20 %) | **1** | G `qs/<cls>/s/<cursor>` → 404 |
| Config refresh | worker and orchestrator, every 30 s | 1 (304) | GC `fleet/cfg` |
| Worker record refresh | worker, every 60 s | 1 (304) | GC `worker/<wid>` |
| Heartbeat | worker, every `fleet.heartbeat` (15 s) | 1 | P `whb/<wid>` |
| Enqueue job | build controller | 5–7 | CAS `jact/<sh2>` (2), C `job/<id>`, G `qt`, C `qs/…` (+1 per collision), CAS `qt` best effort (+2) |
| Claim (targeted by wake-up) | worker | 4 (+2 when advancing head) | G slot, G job, CAS-PUT job (If-Match from that GET), CAS-PUT slot `tk` |
| Claim (polling, backlog) | worker | 5 | G `qt` for the spread window, then as above |
| Job watch | worker, per held job, every 30 s | 1 | G `job/<id>` (sees cancel, requeue) |
| Start / result | worker | 2 each | G + PUT If-Match `job/<id>` |
| Finalise | build controller | 7 | G job, CAS build (2), D job (2); `jact` removal CAS (2) batched per pass |
| Requeue | job reconciler | 4 | CAS job (2), Append slot (2) |
| Reconcile pass | job-reconciler leader, every 10 s | N_shards (304) + busy workers / 15 s | GC `jact/*`, G `whb/<owner>` once per owner per 15 s |
| Registration | worker once, registrar | worker 3–4, registrar 4 | C `wreg`, Append `wrs`; registrar G `wrs` next, G `wreg`, C `worker`, CAS `widx` |

#### R-FLEET-009 Request budget per job
- **Statement:** a job that is enqueued, claimed by a woken worker, run and finalised costs
  ≤ 26 S3 requests on control+fleet (enqueue 7 + claim 6 + start 2 + result 2 + finalise 7, plus
  2 spare for collisions), excluding heartbeats (shared per worker) and job watch (1 per 30 s).
- **Acceptance:** 300-job drain on RustFS with wake-ups on, counted by the proxy (no idle polls
  counted): mean ≤ 26 requests per job, 0 LIST. **[proposal]** [8c/15G].
- **Source:** S9 §3.2 (12 requests with LIST), V2 (§05.13), D60.

#### R-FLEET-010 Idle fleet cost
- **Statement:** an idle worker eligible for one class makes ≤ 0.15 S3 requests/s on average.
  100 idle workers cost ≤ 15 req/s, all GET except heartbeats (100/15 s = 6.7 PUT/s).
- **Acceptance:** a 10 min run of 100 in-process idle workers: total ≤ 15 req/s, PUT ≤ 7/s,
  LIST 0. **[proposal]** [8c/15G].
- **Source:** S9 §3.5 (27 req/s with LIST at 5 s); D54 (slow polling). At AWS list prices:
  GET ≈ 8.3/s ≈ $8.6/month, PUT ≈ 6.7/s ≈ $87/month. Raising `fleet.heartbeat` to 60 s
  cuts PUT to $22/month; a dead worker's jobs then requeue later (R-FLEET-003).

### 05.5.2 UI and API read plan

| Page / RPC (§06) | Requests (page 1) | Keys |
|---|---|---|
| Overview | 3 + classes | GC `bidx/running`, GC `bidx/failed`, GC `fleet/cfg`; queue depth per class = G `qt` + G `qh` (approximate: tail − head) |
| Projects | 1 + pages | GC `pidxm/all`, GC last `pidx/all/p/*` page |
| Builds of a project (newest first, 50) | 2–3 | GC `pctr/<p>`, GC `bpage/<p>/p/<last>` (+ previous page when the last is partial) |
| Builds of a branch | 2–3 | GC `bbrm/…`, GC `bbr/…/p/<last>` (+1) |
| Build detail | 2 + live jobs | G `build/<id>`, G `bnodes/<id>`, G `job/<id>` per non-terminal job (≤ visible rows) |
| Build by number `/p/<name>/b/<n>` | 3 | G `pname`, GC `bpage/<p>/p/<n/50>`, G `build/<id>` |
| Releases of a project | 2–3 | GC `pctr`, GC `rpage/<p>/p/<last>` |
| Release by version | 2 | G `relv/…`, G `rel/…` |
| Registry | 2 | GC `ridxm/all`, GC `ridx/all/p/<last>` |
| Fleet: workers | N_widx (default 4) | GC `widx/*` |
| Fleet: orchestrators | 1 + members | GC `fleet/cfg`, G `orch/<id>` per member |
| Access: users / roles / SAs / OIDC | 2 / 1 / 1 / 1 | `uidxm`+`uidx` page; `roles/all`; `saidx/all`; `oidcmap/all` |
| Users of a role | 1 | GC `rmem/<role>` |
| Auth of an API call (cache miss) | 2–3 | G `tok/<hash>`, GC `user/<id>` or `sa/<id>`, GC `role/<id>` per role (cached 10 s) |

Filters (R-UI-006) work client-side on the loaded pages. State and branch filters use the
dedicated indexes (`bidx/*`, `bbr/*`).

---

## 05.6 State machines

Notation: every "→" is one `store.CAS` on the named record unless stated. "skew" =
`fleet.skew` (5 s **[proposal]**). Terminal states are absorbing.

### 05.6.1 Job (`job/<jobID>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | enqueue (build controller) | build running, job id in `bnodes` | CAS `jact` add (intent first); C `job` {queued, `qa`=now, signed spec}; Append slot `qs/<cls>` → `j` | queued |
| queued | claim (worker W) | spec signature valid; W eligible for class; `st`=queued | `o`=W, `ep`+1, `at`+1, `ca`=now; then CAS slot `tk`=W | claimed |
| queued | slot reaches worker but `st`≠queued | — | CAS slot `tk`=`stale` | (unchanged) |
| queued | re-slot (reconciler) | `qa` + `fleet.reslot_after` (60 s) < now | Append new slot; `qa`=now | queued |
| claimed | start (W) | `o`=W ∧ `ep`=mine | `ch`=now | running |
| claimed, running | result (W) | `o`=W ∧ `ep`=mine ∧ result signed by W | `rs`, `rg` set | succeeded \| failed |
| claimed, running | lease expired (reconciler) | `o`=W ∧ max(`ca`, `whb/W.at`) + `fleet.lease_ttl` + skew < now, or `worker/W.st` ∈ {revoked, gone} | `o`="", `qa`=now; then Append slot | queued |
| queued | cancel (controller) | — | — | cancelled |
| claimed, running | cancel (controller) | — | `cx`=true; Wake/CancelJob to W | (unchanged) |
| claimed, running | cancel ack (W) or `cx` ∧ lease expired | `o`=W ∧ `ep`=mine (ack) | — | cancelled |
| succeeded, failed, cancelled | finalise (build controller) | build record holds the job's final row | copy summary into `build.jb`; D `job` (If-Match); CAS `jact` remove | (deleted) |

Requeue keeps `ep` (the next claim increments it). A paused worker that writes after a requeue
fails the If-Match or the `ep` guard and gets `fleet: lease lost (job requeued or taken by
another worker)`.

#### R-FLEET-003 Claims, leases, requeue (kept, made exact)
- **Statement:** as in the table. Liveness of a claimed job comes from the owner's heartbeat
  `whb/<wid>`, not from per-job renewals. A claim gives `fleet.lease_ttl` (60 s) of grace before
  the first heartbeat counts.
- **Acceptance:** chaos run on RustFS with 3 orchestrators (no leader and with leader), 20
  workers, 300 jobs; 20 % of workers die (SIGKILL) or pause for 2 × TTL + 1 s and then try to
  complete. Result: 0 double successful acceptances, 0 lost jobs (every job reaches a terminal
  state), 0 accepted stale results, 0 LIST. Dead-worker jobs are queued again within
  `lease_ttl + reconcile interval + skew` (75 s at defaults; a test profile uses 3 s TTL).
- **Source:** R-FLEET-003, S9 §3.3, ADR 0011 (amended: heartbeat-based liveness).

#### R-FLEET-011 Queue liveness
- **Statement:**
  - A queued job is always reachable through at least one open slot at or after the head hint,
    or it is re-slotted within `reslot_after`.
  - The build controller writes `job` **before** its slot (job-first order).
  - A worker that finds a slot whose job is missing marks it `orphan`. A worker that finds a
    slot whose job is not queued marks it `stale`.
  - Workers never skip an open slot of a queued job except after losing the claim CAS.
- **Acceptance:** 300-job drain with 20 claimers and 3 enqueuers: 300/300 claimed, 0 doubles.
  The protected regression test inverts the order (slot first) and must fail by losing a job,
  as V2 run 1 did.
- **Source:** V2.

### 05.6.2 Build (`build/<buildID>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | trigger (forge event, API, remote, retry) | project exists; dedupe key (event id) not seen | CAS `pctr.b`+1 → n; C `build` {pending}; `bpage`/`bbr` Set n (intent first); concurrency rule | pending |
| pending | plan (build controller) | not superseded | create the eval or evalbuild job (§05.7) | queued |
| queued, running | evaluation result: no pipeline matched the event (§06 §6.9.4) | forge-triggered build | `fi`; forge status cleared | skipped |
| queued | first job claimed | — | `sa`=now; `bidx/running` add | running |
| running | job finished | other jobs remain or new batches are ready | update `jb`, `bnodes`; enqueue ready batches | running |
| running | all requested targets promoted | — | `fi`; `bidx/running` remove; forge status | succeeded |
| running | a job failed (not infra) | — | cancel queued jobs; let running jobs finish; then `fi`; `bidx/failed` add | failed |
| running | job infra failure | attempts < `fleet.max_attempts` (3) | requeue is the job machine's | running |
| running | job infra failure | attempts ≥ 3 | as failed, error `build failed: job <id> failed 3 times on workers (infrastructure)` | failed |
| pending, queued, running | cancel (user) | permission | cancel all jobs | cancelled |
| pending, queued, running | newer build on the same branch with `concurrency=supersede` | project setting | cancel jobs; `sp`=n′ | superseded |
| terminal | retry (user) | — | new build with `rt`=n (never reopens) | — |

Concurrency rule (R-PIPE-005): per project, setting `concurrency` ∈ `queue` (default for
the default branch and for tags) | `supersede` (default for other branches and PRs) |
`parallel`. The rule is evaluated on `bbr/<branch>` (last two entries; tags use the ref name).
A build that runs a release pipeline is never superseded. "Waits in queue" = stays `pending`
until the older build is terminal. (§06 §6.9.5 uses this rule; aligned at integration.)

### 05.6.3 Worker (`worker/<wid>.st`; no record = registering)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | registrar reads `wrs/reg/s/<cursor>` | `wreg/<wid>` valid (S11 §5.2 checks); ≤ 50 approvals per pass; pending ≤ 1,000 | C `worker/<wid>` {active, g 0, n 1, sealed token}; `widx` add; `ctl/registrar.cu`+1 | active |
| — | invalid registration | — | `ctl/registrar.cu`+1, log `worker registration rejected: <reason>` | — |
| active | token half-life | `te` − now < TTL/2 ∧ heartbeat age < 2 × heartbeat | `n`+1, new sealed token | active |
| active | heartbeat stale | `whb.at` + `fleet.gone_after` (5 min) < now | — (jobs requeue by their own rule) | stale |
| stale | heartbeat fresh | — | — | active |
| active, stale | drain (admin) | — | Drain over Attach | draining |
| draining | undrain (admin) | — | — | active |
| any | revoke (admin) | — | `g`+1, clear `ct` | revoked |
| stale, revoked | GC | stale > 24 h, or revoked > 7 d | D `worker`, `wreg`, `whb`; `widx` remove | (deleted) |

Worker-side: `START → REGISTERING (C wreg, Append wrs) → WAITING (G worker/<wid> every 1 s, up
to 5 min, then re-register with new keys) → ACTIVE`. A worker exits when its record says
`revoked`; a new process means new keys and a new wid (S11 §5.1).

#### R-FLEET-001 Zero-setup registration (kept)
- **Acceptance:** a worker started with only fleet/analytics/artifacts S3 credentials becomes
  `active` and holds a valid token within **5 s** p99 (registrar poll 2 s) **[proposal]**
  [8c/15G]. No admin step, 0 LIST.
- **Source:** R-FLEET-001, D13′, D57, S11.

### 05.6.4 Release (`rel/<projectID>/n/<num10>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | create (API or pipeline step) | build succeeded; name + version unique (C `relv`) | CAS `pctr.r`+1; C `rel` {pending, artifacts from build}; `rpage` Set | pending |
| pending | promote (release controller) | all artifact store paths promoted | copy assets to `rel/…` prefix (artifacts bucket), set visibility (§06) | publishing |
| publishing | assets written | every asset object exists (G) | `ridx` Set if public | published |
| pending, publishing | error | — | error text | failed |
| failed | retry | — | — | pending |
| published | visibility change (admin) | — | update markers | published |
| published | delete (explicit) | permission `ReleaseService.DeleteRelease` | remove markers and assets; `relv` kept as a tombstone | deleted |

Published releases are immutable apart from visibility. Retention never selects a release
(R-STORE-007).

### 05.6.5 Controller lease (`ctl/<name>`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| free (absent) | TryAcquire | — | C {o=me, ep=1, ex=now+TTL} | held(me) |
| held(me) | TryAcquire | o=me | ex=now+TTL | held(me) |
| held(X) | TryAcquire | ex + skew < now | o=me, ep+1 | held(me) |
| held(me) | shutdown | — | ex=now (CAS) | free |

TTL = 2 × interval (a takeover also waits out the clock skew, so 3 × could never meet R-SRV-004's
"new owner within 3 × interval"). Correctness never depends on the lease: every action of a controller is a
CAS that is safe if two leaders overlap (S9 §3.3 measured no-leader runs as correct).

---

## 05.7 Scheduling (D53) and dispatch (D54)

#### R-FLEET-005 Scheduling unit (resolved)
- **Statement:** a job realises a set of ≤ 64 derivations on one worker. By default each build has
  one `evalbuild` closure job. The build controller splits the build into an `eval` job plus
  ready-set `batch` jobs only when the S14 planner predicts a gain of ≥ max(10 s, 30 % of the closure-job time) with the
  idle eligible workers (`split = I ≥ 2 and T1 − Tsplit ≥ max(10 s, 0.3 × T1)`; amended 2026-10-22,
  ADR 0016). Promotion is per job; jobs depend on each other only through promoted
  paths, never through `staging/`.
- **Acceptance:**
  - Planner unit tests on the S14 graphs: fixture → no split; go200, rust129 and wide2000 at
    W = 100 with 2 slots → split (fixture: 14.7 s vs 19.4 s, no split). Boundary rows: a predicted
    gain of exactly 10 s and exactly 30 % splits; just below either bound does not; just above
    both does.
  - The planner's split decision equals the S14 simulation's on each S14 graph (amended
    2026-10-22, test-corrections item 14: the formula's predicted gain differs from the
    simulated one by more than 25 % on go200 (24.9 s vs 13.6 s) and wide2000 (323.4 s vs
    207.0 s), so closeness of times or gains is not an acceptance criterion).
  - A real-run test on 4 local workers: a split build of the 73-drv layer graph is faster than
    the closure job (S14 quiet: closure 20.6 s; per-drv 9.3 s; a batch build must be ≤ 15 s)
    **[proposal]** [8c/15G].
- **Source:** D53, ADR 0016, S14 §5.1, `measure/quiet/s14-sched.log`.

Planner, batching and fair share: exactly S14 §5.1 and §5.4 rules 2–4. `ready` drvs come from
`bnodes`; `idle eligible workers` = Σ free slots in `widx` (refreshed every 15 s) / median
slots, counting only workers whose heartbeat is fresh.

#### R-FLEET-012 Dispatch: slow polling plus wake-up
- **Statement:**
  - Workers poll their class queues every `fleet.poll_interval` (30 s ± 20 %), and again at once
    after finishing a job.
  - After an enqueue, the enqueuing orchestrator sends `Wake{class, slot, job}` over Attach to
    one idle eligible worker attached to it: the `Prefer` worker first, otherwise the worker
    with the most free slots, ties broken at random.
  - If the job is still queued after 2 s, it wakes a second worker.
  - A woken worker GETs exactly that slot (no herd).
  - Claims stay CAS in the bucket. A lost wake-up costs at most one poll interval.
  - A polling worker with a backlog picks a random slot in `[cursor, tail)` (spread) before
    falling back to its cursor.
- **Acceptance:**
  - With wake-ups: enqueue → claim p99 ≤ 1 s with 20 idle workers (V2 spread run: p50 106 ms,
    p99 182 ms) **[proposal]** [8c/15G].
  - Wake-ups disabled: p99 ≤ poll_interval × 1.2 + 1 s.
  - Duplicate wake-ups never cause a double claim (the chaos run includes them).
- **Source:** D54, S9 O-S9-1, V2.

#### R-FLEET-004 Platform, capabilities, labels (kept)
- **Acceptance:** a job of class `x86_64-linux.<h>` is never claimed by an aarch64 worker, or
  by a worker that lacks a required cap or label (property test over 1,000 random cap/label
  sets). Workers only read slots of classes they satisfy.

---

## 05.8 Worker isolation (D56)

#### R-FLEET-008 / R-SEC-000 Worker isolation (kept, made exact)
- **Statement:**
  - **Root worker.** `bob-server worker` runs as uid 0 and refuses to start otherwise
    (`✗ the worker must run as root; builds themselves never run as root` /
    `hint: run it as a systemd system service or a privileged container`).
  - **Job container.** Each job runs in a bob-built job container (no OCI runtime, no daemon).
    The container has:
    - its own cgroup v2 leaf `bob-worker.slice/job-<jobID>`, with `memory.max`, `pids.max` and
      `cpu.max` from the slot config;
    - new mount, pid, ipc, uts and cgroup namespaces;
    - a private `/tmp`;
    - `/nix` bound from the worker's persistent store `/var/lib/bob/worker/nix`;
    - no worker credentials: no env, no files, and the S3 credential file is not mounted.
  - **Nix as root inside the container.** bob's embedded static Nix runs with:
    `sandbox = true`, `sandbox-fallback = false`, `build-users-group =` (empty),
    `auto-allocate-uids = true`, `use-cgroups = true`, `allowed-users = root`,
    `trusted-users = root`, `substituters = <bob cache> https://cache.nixos.org`,
    `require-sigs = true`. **Every build runs as an auto-allocated non-root uid** in Nix's
    sandbox (own net ns: no host network, no IMDS). The store is root-owned, so a build cannot
    rewrite input paths. The per-job overlay `/nix` from S13 is not used.
  - **Evaluation.** `bob.go` evaluation runs in evalbox (ADR 0015 decision 1) inside the job
    container. Its userns maps uid 1000 to a dedicated unprivileged host uid
    `fleet.eval_uid_base + slot` (default 2,000,000,000 + slot), never to 0 or to the worker uid.
  - **Fetches.** Workers never build user-influenced FODs. bob fetches and runs `nix store add`
    (ADR 0015 decision 3).
  - **Refusal.** The worker refuses to start without cgroup v2, Landlock ABI ≥ 4, or Nix
    `auto-allocate-uids` support (`✗ worker cannot isolate builds: <reason>`).
- **Acceptance:**
  - The S13 hostile suite (15 evaluator cases) passes on a root worker.
  - Additional cases:
    - a build that tries `chmod`/write on an input store path gets EPERM/EROFS, and the
      persistent store is unchanged (`nix store verify --check-contents` = 0 errors);
    - `id -u` inside a build ≠ 0;
    - a build cannot read `/proc/<worker pid>/environ` or the S3 credential file;
    - a build cannot connect to 169.254.169.254 or to the host loopback;
    - after the job, 0 processes remain in the job cgroup.
- **Source:** D56, ADR 0015 (amended), S13 §5.1/§5.4.

#### R-FLEET-013 Concurrent jobs per worker
- **Statement:** a worker runs up to `worker.slots` jobs (default `max(1, nproc / 2)`), each in
  its own job container, against one shared persistent store (Nix store locking). The worker
  keeps ≥ `worker.min_free` (15 %) of the store disk free and runs `nix store gc` with the
  newest 24 h of outputs as GC roots.
- **Acceptance:** 2 slots, 2 jobs that build overlapping closures at the same time: both
  succeed, and the shared paths are built once.
- **Source:** S14 (slots), S13.

---

## 05.9 Worker protocol and authentication (D57)

### 05.9.1 `proto/bob/worker/v1/worker.proto`

```proto
syntax = "proto3";

package bob.worker.v1;

import "bob/authz/v1/authz.proto";

option go_package = "github.com/megakuul/bob/internal/gen/bob/worker/v1;workerv1";

// WorkerControlService is served by every orchestrator and called only by workers.
// Authentication: header "Authorization: Bob-Worker <bwt1 token>" and "Bob-PoP" (§05.9.2).
// The token is obtained only through the fleet bucket (worker/<wid>, sealed). User tokens
// are refused with PERMISSION_DENIED. Transport: HTTP/2 + TLS; the worker pins the
// orchestrator's SPKI hash from FleetConfig.
service WorkerControlService {
  // Attach is the long-lived control channel between one worker and one orchestrator.
  // A worker keeps one Attach per member in FleetConfig. The first worker message is Hello.
  // The worker reopens the stream before its token expires (new token, new PoP).
  rpc Attach(stream AttachRequest) returns (stream AttachResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_WORKER};
  }

  // GetCheckoutCredential returns the job's source credentials: a forge credential scoped to the
  // job's repository (or the key of an uploaded source snapshot) and a download token, valid
  // only while the caller holds the job (owner + epoch checked on job/<id>). Flow: §06 §6.9.6.
  rpc GetCheckoutCredential(GetCheckoutCredentialRequest) returns (GetCheckoutCredentialResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_WORKER};
  }
}

message AttachRequest {
  oneof msg {
    Hello hello = 1;
    SlotState slots = 2;
    JobEvent job_event = 3;
    LiveChunk live_chunk = 4;
    Pong pong = 5;
  }
}

message AttachResponse {
  oneof msg {
    Welcome welcome = 1;
    Wake wake = 2;
    LiveSubscribe live_subscribe = 3;
    LiveUnsubscribe live_unsubscribe = 4;
    CancelJob cancel_job = 5;
    Drain drain = 6;
    Ping ping = 7;
  }
}

message Hello {
  string worker_id = 1;
  string platform = 2;              // "x86_64-linux" | "aarch64-linux"
  repeated string class_ids = 3;    // classes this worker satisfies (from FleetConfig)
  int32 slots_total = 4;
  int32 slots_free = 5;
  string version = 6;               // bob-server version
}

message Welcome {
  string orchestrator_id = 1;
  int64 fleet_config_gen = 2;       // worker re-reads fleet/cfg when it is older
}

message SlotState {
  int32 slots_free = 1;
  repeated string running_job_ids = 2;
}

enum JobEventKind {
  JOB_EVENT_KIND_UNSPECIFIED = 0;
  JOB_EVENT_KIND_CLAIMED = 1;
  JOB_EVENT_KIND_RUNNING = 2;
  JOB_EVENT_KIND_FINISHED = 3;      // result written to job/<id>
  JOB_EVENT_KIND_ABANDONED = 4;     // worker gave up (lease lost, cancel acknowledged)
}

// JobEvent is a hint so controllers react without waiting for their next pass.
// The job record stays the truth; orchestrators re-read it before acting.
message JobEvent {
  string job_id = 1;
  int64 epoch = 2;
  JobEventKind kind = 3;
}

// Wake asks the worker to try one slot now (D54).
message Wake {
  string class_id = 1;
  uint64 slot = 2;
  string job_id = 3;
}

// LiveSubscribe asks the worker for its in-memory log of a job (D55): first everything after
// the cursors, then new lines every 250 ms, until expires_unix_ms (renewed by repeating the
// message) or LiveUnsubscribe.
message LiveSubscribe {
  string subscription_id = 1;
  string job_id = 2;
  map<string, LiveCursor> cursors = 3; // package label -> last line the orchestrator has
  int64 expires_unix_ms = 4;           // now + 15 s
}

message LiveUnsubscribe {
  string subscription_id = 1;
}

message LiveCursor {
  int64 seq = 1;
  int32 attempt = 2;
}

message LiveChunk {
  string subscription_id = 1;
  string job_id = 2;
  int64 epoch = 3;
  repeated LiveLine lines = 4;         // ≤ 5,000 lines and ≤ 4 MiB per chunk
  repeated LiveRestart restarts = 5;
  repeated LiveFinal finals = 6;
}

message LiveLine {
  string package = 1;
  int64 seq = 2;
  int32 attempt = 3;
  int32 stream = 4;                    // 1 stdout, 2 stderr, 3 bob
  int64 ts_unix_nano = 5;
  string text = 6;                     // UTF-8, ≤ 4 KiB
}

message LiveRestart {
  string package = 1;
  int32 attempt = 2;
  int64 from_seq = 3;
}

message LiveFinal {
  string package = 1;
  int64 last_seq = 2;
  int32 attempt = 3;
}

message CancelJob {
  string job_id = 1;
}

message Drain {
  bool drain = 1;                      // true: finish running jobs, claim nothing new
}

message Ping {
  int64 unix_ms = 1;
}

message Pong {
  int64 unix_ms = 1;
}

message GetCheckoutCredentialRequest {
  string job_id = 1;
  int64 epoch = 2;
}

message GetCheckoutCredentialResponse {
  // HPKE (ADR 0013 suite) to the worker KEM key, info = "bob-wco-v1"‖0‖LV(wid)‖LV(job_id)‖LV(epoch).
  // Plaintext: canonical CBOR {1: clone_url (https, no credentials), 2: username,
  // 3: token (short-lived, repository-scoped; D62 for Forgejo), 4: expires_unix, 5: fetch_ref, 6: sha}.
  // Empty for upload sources.
  bytes sealed_checkout = 1;
  string source_key = 2;               // artifacts key src/<projectID>/<hex>.tar.zst, else ""
  string download_token = 3;           // bob_dt_, download scope, the job's project only, deleted at job end
  int64 expires_unix = 4;
}
```

### 05.9.2 Tokens and proof of possession

Exactly S11 §5.3 (HPKE MLKEM768-X25519, `bwt1` token, Ed25519 PoP, 1 h TTL, rotation at half
life, record cache ≤ 10 s). STS (S11 §3.6, ADR 0013 decision 8) is removed (D57).
Orchestrator signing keys: the seeds are in control `okey/<kid>`, the public keys in
`fleet/cfg.k`. Job specs (`ss`) and tokens are signed with them.

#### R-FLEET-002 No impersonation (kept)
- **Acceptance:** S11's 31 registration checks, plus:
  - a worker's token used with another worker's key → `unauthenticated: proof of possession invalid`;
  - a job result signed by a non-owner → ignored by the controller (metric
    `bob_fleet_forged_results_total`);
  - a `JobSpec` with a bad signature → the worker marks the slot `stale` and does not run it;
  - worker A's Attach cannot subscribe to or cancel worker B's jobs.
- **Source:** R-FLEET-002, S11, ADR 0013.

#### R-FLEET-006 Checkout credentials only for the held job (kept)
- **Statement:** Workers get a checkout credential only for the job they hold (owner + epoch on
  `job/<id>`, state claimed or running), for one repository, revoked at job end. The flow and the
  forge-side token handling are §06 §6.9.6; the record is a `CheckoutRecord` in `ftok/<sh2>`.
- **Acceptance:**
  - `GetCheckoutCredential` with a job the caller does not own, or a stale epoch →
    `permission_denied: worker holds no lease on job <id>`.
  - Live Forgejo: worker A with the lease gets a credential that clones repo R and is denied on
    repo R2 the bot can read (the forge decides the status for a repository outside the token's
    scope: 403 or 404, never 2xx; S16 measured 403, the harness's Forgejo answers 404; amended
    2026-10-22, A104); after the job ends, the token returns 401 within 30 s.
  - Orchestrator killed between mint and revoke: the `forge-sweeper` (60 s, over `ftok/*`)
    revokes the token within 60 s + skew after the job is terminal; a token without a record is
    removed by the Forgejo name-timestamp sweep (§06 §6.9.6).
  - Fork PR: fetches `refs/pull/N/head` from the base repo with the base-repo credential.
- **Source:** R-FLEET-006, D62, ADR 0018, S16 §3.3; merged with §06 §6.9.6 at integration.

### 05.9.3 RBAC for worker RPCs

| RPC | Principal | Permission | Check |
|---|---|---|---|
| `bob.worker.v1.WorkerControlService/Attach` | worker token + PoP | `worker` (built-in, not assignable to roles) | worker record active, gen/serial |
| `bob.worker.v1.WorkerControlService/GetCheckoutCredential` | worker token + PoP | `worker` | as above + job owner + epoch |

The RBAC endpoint test (R-AUTH-006) lists both as `principal=worker` endpoints. There is no
`bob.peer.v1` service (D55).

#### R-SRV-100 Worker reachability
- **Statement:** workers open outbound connections to every orchestrator endpoint in
  `fleet/cfg.m`. Orchestrators never connect to workers. Without any Attach stream a worker
  still works fully (poll, claim, result through S3); only wake-ups, live logs and checkout
  credentials need an orchestrator.
- **Acceptance:** with all Attach streams blocked by the test proxy, a 50-job drain completes.
  The pickup latency is then bounded by the poll interval, and a job that needs checkout fails
  with an infrastructure error after 3 attempts.
- **Source:** D54, D55, D57.

---

## 05.10 Logs (D55)

### 05.10.1 Lake partitions

lake is used at HEAD (`045e301`, L2 fixed) through the URL-prefix trick. Each partition is one
lake "bucket": `lake.NewFromClient(ctx, s3.New(…, BaseEndpoint: <endpoint>/<analytics bucket>),
"<partition>")`. lake writes `<analytics bucket>/<partition>/lakedb.catalog` and
`<partition>/<table>/…`, and never lists (V4).

| Partition id | Tables | Writers | Contents |
|---|---|---|---|
| `log-<yyyymmdd>-<k>` | `log_line` | workers | lines of builds whose ULID time is that UTC day; `k = fnv32a(buildID) mod log.shards` (4) |
| `run-<yyyymm>` | `package_run` | workers | one row per finished package build |
| `job-<yyyymm>` | `job_run` | build controller | one row per finalised job (duration, worker, class, drv count), feeds the planner |

The tables are S10 §5.1 `LogLine` and `PackageRun`, without a `sort` tag (L1 still open), plus
`JobRun {BuildID, JobID lake.String; Class, Worker string; Drvs, SlotMs, WallMs int64}`.
`BuildRecord.lg` stores the log partition id.

#### R-LOG-001 Periodic batched writes (kept, D55 interval)
- **Statement:**
  - A worker buffers lines in memory and commits to its partition every `log.flush_interval`
    (30 s), or at 10,000 lines / 2 MiB, and at package end and job end.
  - The bucket-wide budget is ≤ 5 commits/s per partition. The interval grows to
    `max(30 s, active workers on that partition / 5 s)`.
  - A non-`ErrOptimisticLock` commit error is ambiguous (see STATUS). The batch is retried, and
    readers dedupe by `(build, package, attempt, seq)`.
  - Lines survive crashes up to the last successful commit.
- **Acceptance:**
  - 50 workers × 200 lines/s for 5 min on RustFS with lake HEAD: after the run, every line
    committed before each worker's last commit is readable exactly once (after dedupe); 0 lost
    lines among committed batches.
  - A worker killed with SIGKILL loses only lines after its last commit.
- **Source:** R-LOG-001, D20, D55, S10 §5.2, lake 045e301.

#### R-LOG-100 lake workarounds until upstream fixes
- **Statement:** until the owner fixes L1, L3 and L4 upstream:
  - no `sort` tags;
  - compaction only by the `lake-maintenance` leader, with a `lake.Bucket` object used by
    nothing else;
  - readers open a fresh `lake.Bucket` for every query (no retry on `NoSuchKey`: a fresh
    catalog per query never names a shard compacted away before it, L4, and sees new commits,
    L7);
  - compaction only for partitions that are older than 10 min and written within the last
    2 days (F7).
- **Acceptance:** the S10 repro tests L3 (two compactors) and L4 (read during compaction) pass
  with these rules: no duplicates, and no failed query.
- **Source:** S10 §3.7. **[open]** Owner: fix L1/L3/L4 upstream (reference patch
  `spikes/s10-lake/lake-s10.patch`).

### 05.10.2 Worker in-memory log

Per job, the worker keeps every line since the last successful commit, plus the last 10,000
committed lines per package (ring). The buffer is never blocked by S3 or by subscribers. Seq is
per (build, package) and continues across attempts. Attempt n ≥ 2 starts after the last
persisted seq (`JobResult.LogLast` of the previous attempt, or a lake read) and emits a
`LiveRestart`.

### 05.10.3 Live pull

#### R-LOG-002 Live log is pulled on demand (replaces fan-out/peering)
- **Statement:**
  - When at least one follower on orchestrator O watches build B, O does the following for each
    non-terminal job of B: it reads `job/<id>` to find the owner W and sends `LiveSubscribe`
    over W's Attach stream to O, with the cursors O already has.
  - O renews the subscription every 5 s (expiry 15 s) and sends `LiveUnsubscribe` when the last
    follower leaves.
  - W sends `LiveChunk` every 250 ms while the subscription is alive. One subscription per
    (orchestrator, job), whatever the number of followers.
  - Without followers, a worker sends no log data to any orchestrator.
  - There is no orchestrator↔orchestrator connection.
- **Acceptance:**
  - 20 builds × 200 lines/s, 50 followers on one build spread over 3 orchestrators: line →
    follower p99 ≤ 1 s **[proposal]** (S17 fan-out at 200 ms push: p99 223 ms) [8c/15G].
  - With 0 followers, the bytes from workers to orchestrators are ≤ 1 KiB/s per worker
    (Hello/SlotState/Pong only).
  - Kill the worker's Attach target → the follower stalls ≤ 3 s (its orchestrator falls back
    to the lake), 0 gaps and 0 duplicates.
- **Source:** D55 (supersedes D12 and S17's push), ADR 0019 (rewritten).

#### R-LOG-101 Merge and fallback
- **Statement:** orchestrators merge lake rows and live chunks per follower with the S17 §5.4
  algorithm (valid attempt for seq s = `max{a : from(a) ≤ s}`).
  - O reads the lake for a build when:
    - a follower has a gap for 1 s;
    - no live chunk arrived for 6 s;
    - the job's worker is not attached to O.
  - While degraded, O reads the lake every 5 s, shared per (O, build), and sets
    `delayed = true` (no shard cache: lake downloads whole shards by design; GetLog p99 12–27 ms
    without one).
- **Acceptance:** the S17 failure matrix (12 runs) with pull instead of push: 0 gaps, 0
  duplicates, final stream = lake content. Degraded latency p99 ≤ `flush_interval` + 10 s
  **[proposal]**.
- **Source:** S17 §5.4/§5.5.

#### R-LOG-003 Attribution per package (kept)
- **Acceptance:** `GetLog(build, package)` returns only the lines of that package; the
  failing-package view of a 50-package build shows the lines of one package. p99 ≤ 500 ms on the
  S10 100M-line data set **[proposal]** (S10: 111 ms).

#### R-LOG-102 Hostile worker input
- **Statement:** an orchestrator accepts a `LiveChunk` only for a job whose record has
  `o`=caller and `ep`=chunk.epoch, with ≤ 4 KiB per line, ≤ 5,000 lines and ≤ 4 MiB per chunk,
  and ≤ 1 MiB/s per worker (4 MiB burst). It drops a chunk that violates these limits and logs
  `invalid_argument: log line invalid: <package>/<seq>`. The UI renders log text inert.
- **Acceptance:** S17's 14 security cases adapted to pull: oversize, flood, cross-job chunk and
  stale epoch are all rejected, and follower output is unchanged. The forged-chunk tests need a
  worker Attach client (`tests/serverenv/workerclient`, Gate H G20 follow-up for the milestone that
  implements worker registration).

### 05.10.4 Reader API (for §06)

```go
package logstore // import "github.com/megakuul/bob/internal/logstore"

// Reader reads persisted logs from lake partitions.
type Reader struct{ /* unexported */ }

// NewReader returns a reader over the analytics bucket; every query opens a fresh
// lake.Bucket (no shard cache, R-LOG-100).
//
// Example:
//
//	r := logstore.NewReader(s3client, "bob-analytics")
//	lines, err := r.Package(ctx, buildID, "//svc/api:server", 0, 10000)
func NewReader(client *s3.Client, analyticsBucket string) *Reader

// Package returns the lines of one package after afterSeq, ordered by seq, deduplicated by
// (attempt, seq) and filtered by the restart rule.
func (r *Reader) Package(ctx context.Context, buildID, pkg string, afterSeq int64, limit int) ([]Line, error)

// Build returns all lines of a build ordered by package, seq.
func (r *Reader) Build(ctx context.Context, buildID string, limit int) ([]Line, error)

// Follow merges persisted and live lines for one follower and calls emit in order until
// ctx ends or every package has its final marker.
func Follow(ctx context.Context, src *Sources, buildID string, cur map[string]Cursor, emit func(Batch) error) error
```

### 05.10.5 Retention

#### R-STORE-007 Retention (kept, made exact for §05 data)
- **Statement:** the `retention` controller runs hourly under its lease:
  - log partitions older than `retention.logs` (30 d) are dropped as whole partitions
    (R-STORE-101 sweep);
  - `run-*` and `job-*` partitions older than `retention.analytics` (400 d) are dropped;
  - terminal build records older than `retention.builds` (365 d), together with their
    `bnodes`, are deleted by walking `bpage` pages from the oldest page (known keys); page
    entries become tombstones;
  - fleet `qs` slots below `head − 1000` and `wrs` slots below `cursor − 1000` are deleted by
    known key;
  - cache paths are removed by walking `promo/<day>` logs older than `retention.cache` (90 d)
    that no release or recent build references (§06 owns the reference rule).

  Releases are never collected.
- **Acceptance:** with the clock advanced by 31 d / 366 d / 91 d, each class of data is gone and
  releases remain. Only the R-STORE-101 sweeps issued a LIST.

---

## 05.11 Orchestrator HA without a coordinator

#### R-SRV-004 Reconciliation (kept, controller list fixed)
- **Statement:** every orchestrator runs every controller. A controller works only while it
  holds `ctl/<name>`, and every write it makes is a CAS, so two leaders at once are safe.

| Controller | Interval | Work (all GET/CAS on known keys) |
|---|---|---|
| `membership` | 10 s | own `orch/<id>` heartbeat (P); add self to `fleet/cfg.m`; remove members with heartbeat > 30 s old |
| `registrar` | 2 s | read `wrs/reg/s/<cu+1…>` until 404; approve; token rotation sweep over `widx` every 60 s; `widx` summaries |
| `build` | event + 5 s | plan, enqueue, batch (S14), finalise jobs, update `build`/`bpage`/`bidx`, concurrency rule |
| `job-reconciler` | 10 s | GC `jact/*`; requeue by heartbeat rule; re-slot after 60 s; finalise orphaned terminal jobs |
| `promotion` | event + 10 s | promote `succeeded` jobs' staging (§06 protocol), delete staged keys |
| `release` | event + 30 s | release state machine |
| `lake-maintenance` | 30 s | compaction (R-LOG-100) |
| `retention` | 1 h | R-STORE-007 |
| `forge-sweeper` | 60 s | revoke expired per-job forge tokens (`ftok/*`, D62) |
| `index-repair` | 10 min | finish intent-first entries older than 5 min (`bpage`, `rmem`, `jact`, `pidx`); rehash shards after a gen change |
| `key` | 1 h | create the next orchestrator key 7 d before `notAfter`; publish it in `fleet/cfg.k` |
| `notify` | event + 2 s | deliver due `nout/*` entries (§06 §6.9.8) |

- **Acceptance:** with 3 orchestrators and `kill -9` of a random orchestrator every 20 s during a
  300-job drain plus 20 build triggers: every build reaches a terminal state, no job runs
  without a lease, every controller lease changes owner within 3 × interval after its holder
  dies, and 0 LIST.
- **Source:** R-SRV-003/004/005, R-NFR-007, ADR 0011.

#### R-SRV-101 Intent-first membership
- **Statement:** an index or membership entry is written before the record it points to, and
  removed after the record is gone. Readers skip entries whose record is missing. `index-repair`
  removes such entries after 5 min. Index entries are hints; records are the truth.
- **Acceptance:** fault injection kills the writer between each pair of writes for build create,
  job enqueue, role binding and project create. After `index-repair`, every index matches its
  records, and no reader showed a phantom record.

#### R-SRV-102 Orchestrator membership and discovery
- **Statement:** an orchestrator publishes `orch/<id>` every 10 s and adds itself to
  `fleet/cfg.m` at start (CAS). Workers learn members only from `fleet/cfg` (GC every 30 s)
  and pin each member's TLS SPKI hash. A member is removed when its heartbeat is older than
  30 s.
- **Acceptance:** a new orchestrator receives Attach streams from all workers within 40 s. A
  killed one is removed from `fleet/cfg` within 40 s. A redirected address with a wrong key is
  refused (S17 security case).

### 05.11.1 Configuration (`bob-server.yaml`, env `BOB_<UPPER_SNAKE>`)

| Key | Default | Meaning |
|---|---|---|
| `storage.endpoint`, `storage.region` | — | S3 endpoint and region |
| `storage.buckets.{control,fleet,analytics,artifacts}` | `bob-control`, `bob-fleet`, `bob-analytics`, `bob-artifacts` | |
| `storage.worker_probe_credential` | unset | R-SEC-102 |
| `fleet.poll_interval` | `30s` | idle poll per class (± 20 %) |
| `fleet.heartbeat` | `15s` | `whb` interval |
| `fleet.lease_ttl` | `60s` | claim grace and heartbeat expiry |
| `fleet.skew` | `5s` | clock skew allowance |
| `fleet.reslot_after` | `60s` | re-slot a queued, unclaimed job |
| `fleet.gone_after` | `5m` | worker `stale` |
| `fleet.max_attempts` | `3` | infra retries per job |
| `fleet.job_shards`, `fleet.worker_shards` | `16`, `4` | `jact`, `widx` shard counts |
| `worker.slots` | `max(1, nproc/2)` | concurrent jobs |
| `worker.min_free` | `15%` | store disk headroom |
| `log.flush_interval` | `30s` | lake commit interval |
| `log.shards` | `4` | log partitions per day |
| `live_log.chunk_interval` | `250ms` | worker chunk cadence while subscribed |
| `retention.logs`, `.analytics`, `.builds`, `.cache` | `30d`, `400d`, `365d`, `90d` | |
| `server.listen`, `server.admin_listen`, `server.trusted_proxies`, `server_name`, `max_parallel_downloads` | `:8443`, `127.0.0.1:9090`, —, —, `256` | HTTP listeners and proxy settings, §06 §6.2/§6.6.3 |

Config changes that workers need (`fleet.*`, `log.*`) are published in `fleet/cfg.pr` by the
orchestrators. The highest `fleet/cfg.g` wins. Workers read only `fleet/cfg`.

---

## 05.12 Budgets [proposal]

Measured values come from loaded-host runs where noted. S9, S10, S11 and S17 have no
quiet-machine logs; their numbers are indicative and must be re-measured in Stage 3 (harness
budgets file).

| R-id | Budget | Basis | Profile |
|---|---|---|---|
| R-NFR-100 | Job pickup with wake-up: enqueue → claim p99 ≤ 1 s | V2: p50 106 ms, p99 182 ms (burst, 20 claimers) | [8c/15G] |
| R-NFR-101 | Job pickup polling only: p99 ≤ 1.2 × poll_interval + 1 s | S9 §3.5 model | [8c/15G] |
| R-NFR-102 | Requests per job ≤ 26 (no LIST) | protocol count §05.5.1 | — |
| R-NFR-103 | Idle worker ≤ 0.15 req/s | §05.5.1 | — |
| R-NFR-104 | Dead worker's job queued again ≤ lease_ttl + 10 s + skew (75 s) | S9 chaos (3 s TTL scaled) | [8c/15G] |
| R-NFR-105 | Live log line → screen p99 ≤ 1 s; degraded p99 ≤ flush_interval + 10 s | S17 fan-out 223 ms; S10 lake 2.7 s at 5 s flush | [8c/15G] |
| R-NFR-106 | Registration → active p99 ≤ 5 s | registrar 2 s poll; S11 crypto < 1 ms | [8c/15G] |
| R-NFR-107 | Revocation effective ≤ 10 s | S11 record cache | — |
| R-NFR-108 | Per-job worker overhead (excluding work) ≤ 1 s; evalbox setup ≤ 25 ms | S14 quiet: drv-closure fetch 0.24 s + upload 0.29 s; S13 quiet: 18.7 ms [8c], 15.0 ms [2c] | [8c/15G], [2c/4G] |
| R-NFR-109 | Total steady S3 load (workers + orchestrators) at 100 workers / 100 running jobs ≤ 30 req/s, 0 LIST | §05.5.1: idle 15 + reconcile 7 + jobs | — |

#### R-NFR-007 Crash safety (kept)
- **Acceptance:** R-FLEET-003 chaos + R-SRV-004 orchestrator kill run + R-SRV-101 fault
  injection together. 0 lost accepted work, 0 corrupt records (every record decodes and
  validates), 0 stale results accepted.

#### R-NFR-005 Scale target (§05 part)
- **Statement:** v1 targets ≤ 5,000 projects, ≤ 1,000 workers, ≤ 2,000 running builds and
  ≤ 1,024 jobs per build. Each limit is enforced, and a request over a limit is refused with an
  error that names the limit.
- **Acceptance:** each limit has a test at limit and limit + 1, at the lowest layer that enforces
  it (store layer with in-process fakes for projects, workers and jobs; the 2,000 running builds
  row stays black-box). The seeding hooks are built by the milestone that implements each limit
  (Gate H, G18).

---

## 05.13 Verifications run for this section

All on RustFS 1.0.3 (single node, `127.0.0.1:22550`), 2026-10-21, [8c/15G], load ≈ 1.5. The
code and outputs are in `/root/scratch-spec05/` (`RESULTS.txt`).

| # | What | Result |
|---|---|---|
| V1 | Raw GET `If-None-Match` (current ETag) → 304; changed → 200 with the new body; missing → 404 `NoSuchKey`; the ETag that `dynamitedb.Get` injects works for a raw conditional GET → 304 | all PASS |
| V1b | SeqLog append by Create-probing: 20 appenders × 50, no tail hint | 1,000 dense, unique slots; 17,594 412s (the reason for the tail hint) |
| V2 | Queue design (tail/head hints, slots, job CAS), 20 claimers, 3 enqueuers, 300 jobs | slot-first order lost 1 job (→ R-FLEET-011 job-first); job-first: 300/300, 0 doubles, 0 LIST; spread pick p50 106 ms, p99 182 ms; cursor-only p50 1.07 s (herd) |
| V3 | SigV4 with URL-prefix endpoint (`<ep>/analytics`, bucket = partition): PUT `If-None-Match`, stale `If-Match` → 412, `If-Match` OK, GET 404, DELETE | all PASS; objects land under `<partition>/` |
| V4 | lake 045e301 through the prefix: 2 partitions × 3 commits × 100 rows, filtered Scan | 300/300 rows each; one catalog per partition; lake made 0 LIST calls |

The herd runs (20 idle claimers at 20 Hz) cost 42–57 requests per job. The targeted wake-up path
avoids this (§05.7). R-FLEET-009 is checked with wake-ups on.

---

## 05.14 Open items

- **[open]** R-STORE-101: the remaining cold-path LIST (lake partition drop, staging sweep).
  Recommendation: keep it until lake has a delete API and S3 lifecycle is verified on RustFS.
- **[open]** lake L1/L3/L4 upstream fixes (R-LOG-100). bob works around them.
- **[open]** `fleet.heartbeat` default 15 s vs 60 s: a trade-off between AWS PUT cost and
  dead-worker detection. Recommendation: 15 s, documented.
- **[open]** Worker container hardening beyond namespaces (a seccomp filter on the Nix process
  against nested userns in builds, S13 risk 3). Recommendation: v1 without it; revisit with
  the Stage 3 security review.
- Re-measure S9/S10/S11/S17 numbers on a quiet machine. None of them has a quiet log.

## Escalations

None. D55 supersedes R-LOG-002's peering and D12. D56 limits ADR 0002's rootless rule to
developer machines (workers run as root). D57 removes STS. All three are owner decisions and
need no conflict resolution.
