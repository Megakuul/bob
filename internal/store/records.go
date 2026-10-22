package store

import (
	"fmt"
	"time"
)

// Record types and keys of SPEC §05.4. Every record is a plain struct (doc.go): string key
// fields tagged pk/sk with `cbor:"-"` (the key is the object name, not data), an ETag field,
// and data fields with the CBOR names of the SPEC tables and omitempty/omitzero.
//
// §06 owns the fields of the identity, forge and release records; for those this file only
// builds the keys (§05 owns keys and access ops).

// ---- Fleet bucket (§05.4.1) ----

// FleetConfig is `fleet/cfg`: CAS by orchestrators, GetCached every 30 s by everybody.
type FleetConfig struct {
	Name    string        `pk:"fleet" cbor:"-"` // always "cfg"
	ETag    string        `etag:"true" cbor:"-"`
	Gen     int64         `cbor:"g,omitempty"`
	Members []FleetMember `cbor:"m,omitempty"`
	Classes []FleetClass  `cbor:"c,omitempty"` // ≤ 256
	Keys    []FleetKey    `cbor:"k,omitempty"`
	Params  FleetParams   `cbor:"pr,omitzero"`
}

// FleetMember is one orchestrator of FleetConfig.m.
type FleetMember struct {
	Orch     string    `cbor:"o,omitempty"`
	Endpoint string    `cbor:"u,omitempty"`
	SPKIPin  string    `cbor:"fp,omitempty"`
	Since    time.Time `cbor:"s,omitzero"`
}

// FleetClass is one scheduling class of FleetConfig.c.
type FleetClass struct {
	ID       string   `cbor:"id,omitempty"`
	Platform string   `cbor:"pl,omitempty"`
	Caps     []string `cbor:"cp,omitempty"`
	Labels   []string `cbor:"lb,omitempty"`
}

// FleetKey is an orchestrator public key of FleetConfig.k (JobSpec signatures).
type FleetKey struct {
	Kid      string    `cbor:"kid,omitempty"`
	Pub      []byte    `cbor:"pub,omitempty"`
	NotAfter time.Time `cbor:"na,omitzero"`
}

// FleetParams are the fleet.* and log.* settings workers need (§05.11.1).
type FleetParams struct {
	PollInterval  time.Duration `cbor:"pi,omitempty"`
	Heartbeat     time.Duration `cbor:"hb,omitempty"`
	LeaseTTL      time.Duration `cbor:"lt,omitempty"`
	Skew          time.Duration `cbor:"sk,omitempty"`
	ReslotAfter   time.Duration `cbor:"ra,omitempty"`
	GoneAfter     time.Duration `cbor:"ga,omitempty"`
	MaxAttempts   int           `cbor:"ma,omitempty"`
	JobShards     int           `cbor:"js,omitempty"`
	WorkerShards  int           `cbor:"ws,omitempty"`
	FlushInterval time.Duration `cbor:"fi,omitempty"`
	LogShards     int           `cbor:"ls,omitempty"`
	ChunkInterval time.Duration `cbor:"ci,omitempty"`
}

// FleetConfigKey is the key model of `fleet/cfg`.
func FleetConfigKey() *FleetConfig { return &FleetConfig{Name: "cfg"} }

// OrchRecord is `orch/<orchID>`: PutOwned by that orchestrator every 10 s.
type OrchRecord struct {
	ID        string    `pk:"orch" cbor:"-"`
	ETag      string    `etag:"true" cbor:"-"`
	Endpoint  string    `cbor:"u,omitempty"`
	SPKIPin   string    `cbor:"fp,omitempty"`
	Heartbeat time.Time `cbor:"h,omitzero"`
	Started   time.Time `cbor:"st,omitzero"`
	Version   string    `cbor:"v,omitempty"`
}

// ControllerLease is `ctl/<controller>` as the controllers read it; Lease writes it.
type ControllerLease struct {
	Name    string    `pk:"ctl" cbor:"-"`
	ETag    string    `etag:"true" cbor:"-"`
	Owner   string    `cbor:"o,omitempty"`
	Epoch   uint64    `cbor:"ep,omitempty"`
	Expires time.Time `cbor:"ex,omitzero"`
	Cursor  []byte    `cbor:"cu,omitempty"` // controller-specific, e.g. the registration seq
}

// WorkerReg is `wreg/<wid>`: Create by the worker.
type WorkerReg struct {
	Wid  string `pk:"wreg" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`
	Body []byte `cbor:"b,omitempty"` // canonical CBOR RegBody
	Sig  []byte `cbor:"s,omitempty"` // Ed25519
}

// RegSlot is a slot of the registration inbox `wrs/reg/s/<seq20>` (SeqLog).
type RegSlot struct {
	Log  string    `pk:"wrs" cbor:"-"` // always "reg"
	Seq  string    `sk:"s" cbor:"-"`
	ETag string    `etag:"true" cbor:"-"`
	Wid  string    `cbor:"w,omitempty"`
	At   time.Time `cbor:"at,omitzero"`
}

// RegLog is the registration inbox: slots wrs/reg/s/<seq20>, tail hint wrt/reg. It has no
// head hint (the registrar keeps its cursor in ctl/registrar), so Head and AdvanceHead fail.
func RegLog(fleet *Bucket) *SeqLog[RegSlot] {
	return NewSeqLog[RegSlot](fleet, "wrs", "wrt", "", "reg")
}

// WorkerRecord is `worker/<wid>`: CAS by the registrar and admin RPCs.
type WorkerRecord struct {
	Wid       string    `pk:"worker" cbor:"-"`
	ETag      string    `etag:"true" cbor:"-"`
	State     string    `cbor:"st,omitempty"` // active|stale|draining|revoked
	Gen       int64     `cbor:"g,omitempty"`
	Serial    int64     `cbor:"n,omitempty"`
	SigPub    []byte    `cbor:"sp,omitempty"`
	Suite     string    `cbor:"su,omitempty"`
	KEMPub    []byte    `cbor:"kp,omitempty"`
	Platform  string    `cbor:"pl,omitempty"`
	Caps      []string  `cbor:"cp,omitempty"`
	Labels    []string  `cbor:"lb,omitempty"`
	Slots     int       `cbor:"sl,omitempty"`
	TokenExp  time.Time `cbor:"te,omitzero"`
	Sealed    []byte    `cbor:"ct,omitempty"`
	RequestID string    `cbor:"rq,omitempty"`
}

// Heartbeat is `whb/<wid>`: PutOwned by the worker every fleet.heartbeat.
type Heartbeat struct {
	Wid  string         `pk:"whb" cbor:"-"`
	ETag string         `etag:"true" cbor:"-"`
	At   time.Time      `cbor:"at,omitzero"`
	Free int            `cbor:"fr,omitempty"`
	Jobs []HeldJobEpoch `cbor:"j,omitempty"`
	Sig  []byte         `cbor:"s,omitempty"` // Ed25519 over (wid, at, j)
}

// HeldJobEpoch is one held job of a heartbeat.
type HeldJobEpoch struct {
	Job   string `cbor:"job,omitempty"`
	Epoch int64  `cbor:"ep,omitempty"`
}

// WorkerIndexEntry is one entry of `widx/<sh2>` (ShardSet "widx", fleet.worker_shards).
type WorkerIndexEntry struct {
	Platform  string    `cbor:"pl,omitempty"`
	State     string    `cbor:"st,omitempty"`
	Heartbeat time.Time `cbor:"hb,omitzero"`
	Free      int       `cbor:"fr,omitempty"`
	Jobs      int       `cbor:"nj,omitempty"`
}

// WorkerIndex is the sharded worker index widx/<sh2>.
func WorkerIndex(fleet *Bucket, shards int) *ShardSet[WorkerIndexEntry] {
	return NewShardSet[WorkerIndexEntry](fleet, "widx", shards)
}

// QueueSlot is a slot `qs/<classID>/s/<seq20>` of a class queue (SeqLog with tail qt/, head qh/).
type QueueSlot struct {
	Class string    `pk:"qs" cbor:"-"`
	Seq   string    `sk:"s" cbor:"-"`
	ETag  string    `etag:"true" cbor:"-"`
	Job   string    `cbor:"j,omitempty"`
	At    time.Time `cbor:"at,omitzero"`
	Taken string    `cbor:"tk,omitempty"` // wid | "stale" | "orphan"
}

// Queue is the sequence log of one class.
func Queue(fleet *Bucket, classID string) *SeqLog[QueueSlot] {
	return NewSeqLog[QueueSlot](fleet, "qs", "qt", "qh", classID)
}

// QueueSlotKey is the key model of slot seq of class classID (for CAS of `tk`).
func QueueSlotKey(classID string, seq uint64) *QueueSlot {
	s, _ := Seq20(seq)
	return &QueueSlot{Class: classID, Seq: s}
}

// ActiveJob is one entry of `jact/<sh2>` (ShardSet "jact", fleet.job_shards).
type ActiveJob struct {
	Build   string    `cbor:"b,omitempty"`
	Class   string    `cbor:"c,omitempty"`
	Created time.Time `cbor:"at,omitzero"`
}

// ActiveJobs is the sharded active-job set jact/<sh2>.
func ActiveJobs(fleet *Bucket, shards int) *ShardSet[ActiveJob] {
	return NewShardSet[ActiveJob](fleet, "jact", shards)
}

// JobRecord is the fleet-side truth of one job while it is not finalised (§05.4.3).
type JobRecord struct {
	ID   string `pk:"job" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	Spec    []byte `cbor:"sp,omitempty"` // canonical CBOR JobSpec, ≤ 64 KiB
	SpecSig []byte `cbor:"ss,omitempty"` // Ed25519("bob-jobspec-v1"‖0‖LV(sp)) by FleetConfig key kid
	Kid     string `cbor:"kid,omitempty"`

	State   string    `cbor:"st,omitempty"` // queued|claimed|running|succeeded|failed|cancelled
	Owner   string    `cbor:"o,omitempty"`  // wid while claimed/running
	Epoch   int64     `cbor:"ep,omitempty"` // +1 on every claim
	Attempt int64     `cbor:"at,omitempty"` // 1 on first claim, +1 on each requeue-claim
	Claimed time.Time `cbor:"ca,omitzero"`
	Changed time.Time `cbor:"ch,omitzero"`
	Queued  time.Time `cbor:"qa,omitzero"` // last time it became queued
	Cancel  bool      `cbor:"cx,omitempty"`

	Result    []byte `cbor:"rs,omitempty"` // canonical CBOR JobResult
	ResultSig []byte `cbor:"rg,omitempty"` // Ed25519 by Owner's sigpub over ("bob-jobres-v1"‖jobID‖epoch‖rs)

	// Promotion of a succeeded job (§06 §6.6.4): "" (pending) | "done" |
	// "blocked: staging incomplete" | "failed: artifact verification".
	Promotion       string   `cbor:"pm,omitempty"`
	NonReproducible []string `cbor:"nr,omitempty"` // store paths (R-NFR-006)
}

// MaxJobSpec bounds JobRecord.Spec.
const MaxJobSpec = 64 << 10

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

// JobResult is written by the worker that holds the lease.
type JobResult struct {
	V        int              `cbor:"1,keyasint"`
	Status   string           `cbor:"2,keyasint"` // ok | failed | infra
	Complete string           `cbor:"3,keyasint"` // key of COMPLETE.json, "" on failure
	CompSHA  string           `cbor:"4,keyasint"` // hex SHA-256 of COMPLETE.json
	Failed   []string         `cbor:"5,keyasint"` // failing packages (labels)
	Error    string           `cbor:"6,keyasint"` // translated error (§02 taxonomy), ≤ 8 KiB
	LogLast  map[string]int64 `cbor:"7,keyasint"` // package → last persisted seq
	Finished int64            `cbor:"8,keyasint"`
	// EvalSHA is the hex SHA-256 of <staging>eval.json, the evaluation report of an evalbuild
	// or eval job (internal/build); it binds the report to the owner's signature.
	EvalSHA string `cbor:"9,keyasint,omitempty"`
}

// ---- Control bucket (§05.4.2, §05.4.4, §05.4.5) ----

// BuildRecord is `build/<buildID>` (§05.4.4).
type BuildRecord struct {
	ID           string            `pk:"build" cbor:"-"`
	ETag         string            `etag:"true" cbor:"-"`
	Project      string            `cbor:"pr,omitempty"`
	Number       uint64            `cbor:"n,omitempty"`
	State        string            `cbor:"st,omitempty"` // pending|queued|running|succeeded|failed|cancelled|superseded|skipped
	Phase        string            `cbor:"ph,omitempty"` // evaluating|building|promoting
	Trigger      BuildTrigger      `cbor:"tr,omitzero"`
	Branch       string            `cbor:"br,omitempty"`
	Commit       string            `cbor:"cm,omitempty"`
	Author       string            `cbor:"au,omitempty"` // commit author (control bucket only)
	Created      time.Time         `cbor:"cr,omitzero"`
	Started      time.Time         `cbor:"sa,omitzero"`
	Finished     time.Time         `cbor:"fi,omitzero"`
	Platforms    []string          `cbor:"pl,omitempty"`
	SupersededBy uint64            `cbor:"sp,omitempty"`
	RetryOf      uint64            `cbor:"rt,omitempty"`
	Source       BuildSource       `cbor:"src,omitzero"`
	Targets      []string          `cbor:"tg,omitempty"`
	Variants     []string          `cbor:"va,omitempty"`
	Pipelines    []PipelineRun     `cbor:"pi,omitempty"`
	Reported     map[string]string `cbor:"rp,omitempty"` // forge status context → last state sent
	Jobs         []BuildJob        `cbor:"jb,omitempty"` // ≤ 1,024
	Result       BuildResult       `cbor:"rs,omitzero"`
	LogPartition string            `cbor:"lg,omitempty"`
	RequestID    string            `cbor:"rq,omitempty"`

	// Fields of the build controller (internal/build, §06 Build message) beyond the §05.4.4
	// list; all optional.
	Attempt   int           `cbor:"an,omitempty"`  // 1 + retries in the retry-of chain (0 = 1)
	RerunOf   uint64        `cbor:"ro,omitempty"`  // build number this build re-runs
	Pipeline  string        `cbor:"pn,omitempty"`  // CreateBuild{pipeline}: run this pipeline only
	Test      bool          `cbor:"ts,omitempty"`  // targets are tests (bob test --remote)
	Reason    string        `cbor:"why,omitempty"` // CreateBuild reason (≤ 200 bytes)
	Evaluated bool          `cbor:"ev,omitempty"`  // an evaluation result was accepted
	Counts    PackageCounts `cbor:"pc,omitzero"`
	Errors    []BuildError  `cbor:"er,omitempty"` // first failures, ≤ 20
	// NonReproducible is JobRecord.nr of the finalised jobs, by job id (R-NFR-006).
	NonReproducible map[string][]string `cbor:"nr,omitempty"`
}

// PackageCounts is BuildRecord.pc (§06 PackageCounts).
type PackageCounts struct {
	Total     int `cbor:"t,omitempty"`
	Cached    int `cbor:"c,omitempty"`
	Built     int `cbor:"b,omitempty"`
	Running   int `cbor:"r,omitempty"`
	Queued    int `cbor:"q,omitempty"`
	Failed    int `cbor:"f,omitempty"`
	Skipped   int `cbor:"s,omitempty"`
	Cancelled int `cbor:"x,omitempty"`
}

// BuildError is one entry of BuildRecord.er (§06 ErrorSummary).
type BuildError struct {
	Code    string `cbor:"c,omitempty"` // BOB-E-NNNN
	Message string `cbor:"m,omitempty"`
	Label   string `cbor:"l,omitempty"`
	Hint    string `cbor:"h,omitempty"`
}

// MaxBuildJobs bounds BuildRecord.Jobs.
const MaxBuildJobs = 1024

// BuildTrigger is BuildRecord.tr.
type BuildTrigger struct {
	Kind  string `cbor:"k,omitempty"` // push|tag|pr|api|remote|rerun|retry
	By    string `cbor:"by,omitempty"`
	Event string `cbor:"ev,omitempty"` // forge event id

	Ref    string `cbor:"rf,omitempty"` // refs/heads/…, refs/tags/…, refs/pull/N/head
	PR     int64  `cbor:"pr,omitempty"` // pull request number
	Fork   bool   `cbor:"fk,omitempty"` // pull request from a fork
	Sender string `cbor:"sd,omitempty"` // forge login (display only)
}

// BuildSource is BuildRecord.src: a git ref or an uploaded snapshot.
type BuildSource struct {
	Ref      string `cbor:"ref,omitempty"`
	SHA      string `cbor:"sha,omitempty"`
	FetchRef string `cbor:"fr,omitempty"`
	Upload   string `cbor:"up,omitempty"` // sha256 of the uploaded tar.zst
}

// PipelineRun is one entry of BuildRecord.pi.
type PipelineRun struct {
	Name    string `cbor:"n,omitempty"`
	State   string `cbor:"st,omitempty"`
	Release string `cbor:"rel,omitempty"` // release id

	Concurrency string `cbor:"cc,omitempty"` // queue|supersede|parallel ("" = per-ref default)
	Releases    bool   `cbor:"rl,omitempty"` // a release pipeline (never superseded)
}

// BuildJob is one entry of BuildRecord.jb.
type BuildJob struct {
	ID       string        `cbor:"id,omitempty"`
	Kind     string        `cbor:"k,omitempty"`
	Class    string        `cbor:"c,omitempty"`
	State    string        `cbor:"st,omitempty"`
	Attempts int64         `cbor:"at,omitempty"`
	Epoch    int64         `cbor:"ep,omitempty"`
	Worker   string        `cbor:"w,omitempty"`
	Duration time.Duration `cbor:"d,omitempty"`
	// Promotion is the job's final JobRecord.pm (§06 Job.promotion).
	Promotion string `cbor:"pm,omitempty"`
}

// BuildResult is BuildRecord.rs.
type BuildResult struct {
	Failed []string `cbor:"f,omitempty"`
	Error  string   `cbor:"e,omitempty"`
}

// BuildEntry is one entry of the build pages `bpage/<projectID>/p/<num10>` (P = 50).
type BuildEntry struct {
	N        uint64        `cbor:"n,omitempty"`
	Build    string        `cbor:"id,omitempty"`
	State    string        `cbor:"st,omitempty"`
	Branch   string        `cbor:"br,omitempty"`
	Commit   string        `cbor:"cm,omitempty"` // first 12 chars
	Trigger  string        `cbor:"tr,omitempty"`
	Created  time.Time     `cbor:"cr,omitzero"`
	Duration time.Duration `cbor:"du,omitempty"`
}

// BuildPages is the newest-first build list of a project.
func BuildPages(control *Bucket, projectID string) *PagedIndex[BuildEntry] {
	return NewPagedIndex[BuildEntry](control, "bpage", projectID, 50)
}

// BranchBuilds is the per-branch index of build numbers bbr/<projectID.enc(branch)>/p/<num10>
// (P = 50); its count lives in BranchCounter.
func BranchBuilds(control *Bucket, projectID, branch string) (*PagedIndex[uint64], error) {
	scope, err := Scoped(projectID, branch)
	if err != nil {
		return nil, err
	}
	return NewPagedIndex[uint64](control, "bbr", scope, 50), nil
}

// BranchCounter is `bbrm/<projectID.enc(branch)>`: the entry count of BranchBuilds.
type BranchCounter struct {
	Scope string `pk:"bbrm" cbor:"-"`
	ETag  string `etag:"true" cbor:"-"`
	Count uint64 `cbor:"n,omitempty"`
}

// BranchCounterKey is the key model of the branch counter.
func BranchCounterKey(projectID, branch string) (*BranchCounter, error) {
	scope, err := Scoped(projectID, branch)
	if err != nil {
		return nil, err
	}
	return &BranchCounter{Scope: scope}, nil
}

// ProjectCounters is `pctr/<projectID>`.
type ProjectCounters struct {
	Project  string `pk:"pctr" cbor:"-"`
	ETag     string `etag:"true" cbor:"-"`
	Builds   uint64 `cbor:"b,omitempty"`
	Releases uint64 `cbor:"r,omitempty"`
}

// RunningBuild is one entry of `bidx/running`.
type RunningBuild struct {
	Project string    `cbor:"p,omitempty"`
	N       uint64    `cbor:"n,omitempty"`
	Started time.Time `cbor:"sa,omitzero"`
}

// RunningBuilds is `bidx/running` (≤ 2,000 entries).
type RunningBuilds struct {
	Name   string                  `pk:"bidx" cbor:"-"` // "running"
	ETag   string                  `etag:"true" cbor:"-"`
	Builds map[string]RunningBuild `cbor:"m,omitempty"`
}

// FailedBuild is one entry of the `bidx/failed` ring.
type FailedBuild struct {
	Build    string    `cbor:"id,omitempty"`
	Project  string    `cbor:"p,omitempty"`
	N        uint64    `cbor:"n,omitempty"`
	Finished time.Time `cbor:"fi,omitzero"`
}

// FailedBuilds is `bidx/failed`: the last 200 failed builds, oldest first.
type FailedBuilds struct {
	Name   string        `pk:"bidx" cbor:"-"` // "failed"
	ETag   string        `etag:"true" cbor:"-"`
	Builds []FailedBuild `cbor:"r,omitempty"`
}

// PromoEntry is a slot of the promotion log promo/<yyyymmdd>/s/<seq20> (tail promot/<yyyymmdd>).
type PromoEntry struct {
	StoreHash string    `cbor:"sh,omitempty"`
	Project   string    `cbor:"p,omitempty"`
	Build     string    `cbor:"b,omitempty"`
	At        time.Time `cbor:"at,omitzero"`
}

// PromoLog is the promotion log of one UTC day. It has no head hint (cache GC walks old days).
func PromoLog(control *Bucket, day time.Time) *SeqLog[PromoEntry] {
	return NewSeqLog[PromoEntry](control, "promo", "promot", "", day.UTC().Format("20060102"))
}

// ---- Keys of the §06-owned control records ----

// ReleaseID is the API id of release n of a project: <projectID>.<num10>.
func ReleaseID(projectID string, n uint64) (string, error) {
	num, _ := Num10(n)
	return Segment(projectID + "." + num)
}

// ControlKey builds the key of a control-bucket row of §05.4.2/§05.4.5 whose segments come
// from outside input; it applies the row's encoding. kind is the row's pk name; args are the
// raw values in the order of the row.
//
//	pname   name                 → pname/<enc(name)>
//	uname   name                 → uname/<enc(name)>
//	rname   name                 → rname/<enc(name)>
//	oidcsub issuer, subject      → oidcsub/<hash64(iss‖0‖sub)>
//	tok     token                → tok/<hash64(token)>     (also sess, oac, devc, oidcst)
//	devu    user code            → devu/<user code>
//	relv    project, name, ver   → relv/<projectID.enc(name)>/v/<enc(version)>
//	repo    forge, repo id       → repo/<forgeID.enc(repo id)>
//	fdel    forge, delivery id   → fdel/<forgeID.enc(delivery id)>
//
// relv, repo and fdel join their two leading values into one segment ("."): R-STORE-104 allows
// only <pk>/<v> or <pk>/<v>/<sk>/<v>, and the SPEC rows have one segment too many.
func ControlKey(kind string, args ...string) (string, error) {
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("store: key %s takes %d values, got %d", kind, n, len(args))
		}
		return nil
	}
	switch kind {
	case "pname", "uname", "rname":
		if err := need(1); err != nil {
			return "", err
		}
		e, _ := Enc(args[0])
		return Key(kind, e)
	case "oidcsub":
		if err := need(2); err != nil {
			return "", err
		}
		h, _ := Hash64(args[0] + "\x00" + args[1])
		return Key(kind, h)
	case "tok", "sess", "oac", "devc", "oidcst":
		if err := need(1); err != nil {
			return "", err
		}
		if args[0] == "" {
			return "", ErrKeySegment
		}
		h, _ := Hash64(args[0])
		return Key(kind, h)
	case "devu":
		if err := need(1); err != nil {
			return "", err
		}
		return Key(kind, args[0])
	case "relv":
		if err := need(3); err != nil {
			return "", err
		}
		scope, err := Scoped(args[0], args[1])
		if err != nil {
			return "", err
		}
		v, _ := Enc(args[2])
		return Key(kind, scope, "v", v)
	case "repo", "fdel":
		if err := need(2); err != nil {
			return "", err
		}
		scope, err := Scoped(args[0], args[1])
		if err != nil {
			return "", err
		}
		return Key(kind, scope)
	}
	return "", fmt.Errorf("store: no key rule for %q", kind)
}

// ReleaseKey is rel/<projectID>/n/<num10>.
func ReleaseKey(projectID string, n uint64) (string, error) {
	num, _ := Num10(n)
	return Key("rel", projectID, "n", num)
}

// CheckoutShardKey is ftok/<sh2(jobID)>.
func CheckoutShardKey(jobID string, shards int) (string, error) {
	sh, err := Sh2(jobID, shards)
	if err != nil {
		return "", err
	}
	return Key("ftok", sh)
}
