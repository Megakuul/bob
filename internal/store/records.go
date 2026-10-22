package store

import (
	"fmt"
	"time"

	"github.com/megakuul/dynamitedb"
)

// Record types and keys of SPEC §05.4. Every record kind is one dynamitedb model (doc.go):
// KeyField fields tagged pk/sk with `cbor:"-"` (the key is the object name, not data), an
// ETagField, and DataField fields with the CBOR names of the SPEC tables and omitempty. Each
// record type has a Draft method: the fresh update value that writes a fetched record again
// (CAS builds its write from it, R-STORE-103 rule 4).
//
// §06 owns the fields of the identity, forge and release records; for those this file only
// builds the keys (§05 owns keys and access ops).

// ---- Fleet bucket (§05.4.1) ----

// FleetConfig is `fleet/cfg`: CAS by orchestrators, GetCached every 30 s by everybody.
type FleetConfig struct {
	Name    dynamitedb.KeyField                 `pk:"fleet" cbor:"-"` // always "cfg"
	ETag    dynamitedb.ETagField                `etag:"true" cbor:"-"`
	Gen     dynamitedb.DataField[int64]         `cbor:"g,omitempty"`
	Members dynamitedb.DataField[[]FleetMember] `cbor:"m,omitempty"`
	Classes dynamitedb.DataField[[]FleetClass]  `cbor:"c,omitempty"` // ≤ 256
	Keys    dynamitedb.DataField[[]FleetKey]    `cbor:"k,omitempty"`
	Params  dynamitedb.DataField[FleetParams]   `cbor:"pr,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *FleetConfig) Draft() *FleetConfig {
	return &FleetConfig{
		Name:    r.Name,
		ETag:    r.ETag,
		Gen:     Carry(r.Gen),
		Members: Carry(r.Members),
		Classes: Carry(r.Classes),
		Keys:    Carry(r.Keys),
		Params:  Carry(r.Params),
	}
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
func FleetConfigKey() *FleetConfig { return &FleetConfig{Name: dynamitedb.Key("cfg")} }

// OrchRecord is `orch/<orchID>`: PutOwned by that orchestrator every 10 s.
type OrchRecord struct {
	ID        dynamitedb.KeyField             `pk:"orch" cbor:"-"`
	ETag      dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Endpoint  dynamitedb.DataField[string]    `cbor:"u,omitempty"`
	SPKIPin   dynamitedb.DataField[string]    `cbor:"fp,omitempty"`
	Heartbeat dynamitedb.DataField[time.Time] `cbor:"h,omitempty"`
	Started   dynamitedb.DataField[time.Time] `cbor:"st,omitempty"`
	Version   dynamitedb.DataField[string]    `cbor:"v,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OrchRecord) Draft() *OrchRecord {
	return &OrchRecord{
		ID:        r.ID,
		ETag:      r.ETag,
		Endpoint:  Carry(r.Endpoint),
		SPKIPin:   Carry(r.SPKIPin),
		Heartbeat: Carry(r.Heartbeat),
		Started:   Carry(r.Started),
		Version:   Carry(r.Version),
	}
}

// ControllerLease is `ctl/<controller>` as the controllers read it; Lease writes it.
type ControllerLease struct {
	Name    dynamitedb.KeyField             `pk:"ctl" cbor:"-"`
	ETag    dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Owner   dynamitedb.DataField[string]    `cbor:"o,omitempty"`
	Epoch   dynamitedb.DataField[uint64]    `cbor:"ep,omitempty"`
	Expires dynamitedb.DataField[time.Time] `cbor:"ex,omitempty"`
	Cursor  dynamitedb.DataField[[]byte]    `cbor:"cu,omitempty"` // controller-specific, e.g. the registration seq
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ControllerLease) Draft() *ControllerLease {
	return &ControllerLease{
		Name:    r.Name,
		ETag:    r.ETag,
		Owner:   Carry(r.Owner),
		Epoch:   Carry(r.Epoch),
		Expires: Carry(r.Expires),
		Cursor:  Carry(r.Cursor),
	}
}

// NewControllerLease is the leader lease `ctl/<name>` of a controller (§05.6.5).
func NewControllerLease(fleet *Bucket, name, owner string, ttl time.Duration) *Lease {
	return NewLease(fleet, func(s LeaseState) *ControllerLease {
		return &ControllerLease{Name: dynamitedb.Key(name), Owner: setNZ(s.Owner), Epoch: setNZ(s.Epoch),
			Expires: setNZ(s.Expires), Cursor: setNZ(s.Cursor)}
	}, func(r *ControllerLease) LeaseState {
		return LeaseState{Owner: r.Owner.Value(), Epoch: r.Epoch.Value(), Expires: r.Expires.Value(), Cursor: r.Cursor.Value()}
	}, owner, ttl)
}

// WorkerReg is `wreg/<wid>`: Create by the worker.
type WorkerReg struct {
	Wid  dynamitedb.KeyField          `pk:"wreg" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	Body dynamitedb.DataField[[]byte] `cbor:"b,omitempty"` // canonical CBOR RegBody
	Sig  dynamitedb.DataField[[]byte] `cbor:"s,omitempty"` // Ed25519
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *WorkerReg) Draft() *WorkerReg {
	return &WorkerReg{
		Wid:  r.Wid,
		ETag: r.ETag,
		Body: Carry(r.Body),
		Sig:  Carry(r.Sig),
	}
}

// RegSlot is a slot of the registration inbox `wrs/reg/s/<seq20>` (SeqLog).
type RegSlot struct {
	Log  dynamitedb.KeyField             `pk:"wrs" cbor:"-"` // always "reg"
	Seq  dynamitedb.KeyField             `sk:"s" cbor:"-"`
	ETag dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Wid  dynamitedb.DataField[string]    `cbor:"w,omitempty"`
	At   dynamitedb.DataField[time.Time] `cbor:"at,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RegSlot) Draft() *RegSlot {
	return &RegSlot{
		Log:  r.Log,
		Seq:  r.Seq,
		ETag: r.ETag,
		Wid:  Carry(r.Wid),
		At:   Carry(r.At),
	}
}

// RegLog is the registration inbox: slots wrs/reg/s/<seq20>, tail hint wrt/reg. It has no
// head hint (the registrar keeps its cursor in ctl/registrar), so Head and AdvanceHead fail.
func RegLog(fleet *Bucket) *SeqLog[RegSlot] {
	return NewSeqLog(fleet, func(seq uint64) *RegSlot {
		s, _ := Seq20(seq)
		return &RegSlot{Log: dynamitedb.Key("reg"), Seq: dynamitedb.Key(s)}
	}, NewCounter(fleet, func(n uint64) *RegTail {
		return &RegTail{Log: dynamitedb.Key("reg"), N: setNZ(n)}
	}, func(h *RegTail) uint64 { return h.N.Value() }), nil)
}

// RegTail is the tail hint `wrt/reg` of the registration inbox (`SeqHint`: n).
type RegTail struct {
	Log  dynamitedb.KeyField          `pk:"wrt" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RegTail) Draft() *RegTail { return &RegTail{Log: r.Log, ETag: r.ETag, N: Carry(r.N)} }

// WorkerRecord is `worker/<wid>`: CAS by the registrar and admin RPCs.
type WorkerRecord struct {
	Wid       dynamitedb.KeyField             `pk:"worker" cbor:"-"`
	ETag      dynamitedb.ETagField            `etag:"true" cbor:"-"`
	State     dynamitedb.DataField[string]    `cbor:"st,omitempty"` // active|stale|draining|revoked
	Gen       dynamitedb.DataField[int64]     `cbor:"g,omitempty"`
	Serial    dynamitedb.DataField[int64]     `cbor:"n,omitempty"`
	SigPub    dynamitedb.DataField[[]byte]    `cbor:"sp,omitempty"`
	Suite     dynamitedb.DataField[string]    `cbor:"su,omitempty"`
	KEMPub    dynamitedb.DataField[[]byte]    `cbor:"kp,omitempty"`
	Platform  dynamitedb.DataField[string]    `cbor:"pl,omitempty"`
	Caps      dynamitedb.DataField[[]string]  `cbor:"cp,omitempty"`
	Labels    dynamitedb.DataField[[]string]  `cbor:"lb,omitempty"`
	Slots     dynamitedb.DataField[int]       `cbor:"sl,omitempty"`
	TokenExp  dynamitedb.DataField[time.Time] `cbor:"te,omitempty"`
	Sealed    dynamitedb.DataField[[]byte]    `cbor:"ct,omitempty"`
	RequestID dynamitedb.DataField[string]    `cbor:"rq,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *WorkerRecord) Draft() *WorkerRecord {
	return &WorkerRecord{
		Wid:       r.Wid,
		ETag:      r.ETag,
		State:     Carry(r.State),
		Gen:       Carry(r.Gen),
		Serial:    Carry(r.Serial),
		SigPub:    Carry(r.SigPub),
		Suite:     Carry(r.Suite),
		KEMPub:    Carry(r.KEMPub),
		Platform:  Carry(r.Platform),
		Caps:      Carry(r.Caps),
		Labels:    Carry(r.Labels),
		Slots:     Carry(r.Slots),
		TokenExp:  Carry(r.TokenExp),
		Sealed:    Carry(r.Sealed),
		RequestID: Carry(r.RequestID),
	}
}

// Heartbeat is `whb/<wid>`: PutOwned by the worker every fleet.heartbeat.
type Heartbeat struct {
	Wid  dynamitedb.KeyField                  `pk:"whb" cbor:"-"`
	ETag dynamitedb.ETagField                 `etag:"true" cbor:"-"`
	At   dynamitedb.DataField[time.Time]      `cbor:"at,omitempty"`
	Free dynamitedb.DataField[int]            `cbor:"fr,omitempty"`
	Jobs dynamitedb.DataField[[]HeldJobEpoch] `cbor:"j,omitempty"`
	Sig  dynamitedb.DataField[[]byte]         `cbor:"s,omitempty"` // Ed25519 over (wid, at, j)
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *Heartbeat) Draft() *Heartbeat {
	return &Heartbeat{
		Wid:  r.Wid,
		ETag: r.ETag,
		At:   Carry(r.At),
		Free: Carry(r.Free),
		Jobs: Carry(r.Jobs),
		Sig:  Carry(r.Sig),
	}
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
	return NewShardSet(fleet, shards, func(sh string, m map[string]WorkerIndexEntry) *WorkerIndexShard {
		return &WorkerIndexShard{Shard: dynamitedb.Key(sh), Workers: setNZ(m)}
	}, func(s *WorkerIndexShard) map[string]WorkerIndexEntry { return s.Workers.Value() })
}

// WorkerIndexShard is one shard `widx/<sh2>` of the worker index: wid -> entry.
type WorkerIndexShard struct {
	Shard   dynamitedb.KeyField                               `pk:"widx" cbor:"-"`
	ETag    dynamitedb.ETagField                              `etag:"true" cbor:"-"`
	Workers dynamitedb.DataField[map[string]WorkerIndexEntry] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *WorkerIndexShard) Draft() *WorkerIndexShard {
	return &WorkerIndexShard{Shard: r.Shard, ETag: r.ETag, Workers: Carry(r.Workers)}
}

// QueueSlot is a slot `qs/<classID>/s/<seq20>` of a class queue (SeqLog with tail qt/, head qh/).
type QueueSlot struct {
	Class dynamitedb.KeyField             `pk:"qs" cbor:"-"`
	Seq   dynamitedb.KeyField             `sk:"s" cbor:"-"`
	ETag  dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Job   dynamitedb.DataField[string]    `cbor:"j,omitempty"`
	At    dynamitedb.DataField[time.Time] `cbor:"at,omitempty"`
	Taken dynamitedb.DataField[string]    `cbor:"tk,omitempty"` // wid | "stale" | "orphan"
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *QueueSlot) Draft() *QueueSlot {
	return &QueueSlot{
		Class: r.Class,
		Seq:   r.Seq,
		ETag:  r.ETag,
		Job:   Carry(r.Job),
		At:    Carry(r.At),
		Taken: Carry(r.Taken),
	}
}

// Queue is the sequence log of one class.
func Queue(fleet *Bucket, classID string) *SeqLog[QueueSlot] {
	return NewSeqLog(fleet, func(seq uint64) *QueueSlot { return QueueSlotKey(classID, seq) },
		NewCounter(fleet, func(n uint64) *QueueTail {
			return &QueueTail{Class: dynamitedb.Key(classID), N: setNZ(n)}
		}, func(h *QueueTail) uint64 { return h.N.Value() }),
		NewCounter(fleet, func(n uint64) *QueueHead {
			return &QueueHead{Class: dynamitedb.Key(classID), N: setNZ(n)}
		}, func(h *QueueHead) uint64 { return h.N.Value() }))
}

// QueueTail is the tail hint `qt/<classID>` of a class queue (`SeqHint`: n).
type QueueTail struct {
	Class dynamitedb.KeyField          `pk:"qt" cbor:"-"`
	ETag  dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N     dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *QueueTail) Draft() *QueueTail {
	return &QueueTail{Class: r.Class, ETag: r.ETag, N: Carry(r.N)}
}

// QueueHead is the head hint `qh/<classID>` of a class queue (`SeqHint`: n).
type QueueHead struct {
	Class dynamitedb.KeyField          `pk:"qh" cbor:"-"`
	ETag  dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N     dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *QueueHead) Draft() *QueueHead {
	return &QueueHead{Class: r.Class, ETag: r.ETag, N: Carry(r.N)}
}

// QueueSlotKey is the key model of slot seq of class classID (for CAS of `tk`).
func QueueSlotKey(classID string, seq uint64) *QueueSlot {
	s, _ := Seq20(seq)
	return &QueueSlot{Class: dynamitedb.Key(classID), Seq: dynamitedb.Key(s)}
}

// ActiveJob is one entry of `jact/<sh2>` (ShardSet "jact", fleet.job_shards).
type ActiveJob struct {
	Build   string    `cbor:"b,omitempty"`
	Class   string    `cbor:"c,omitempty"`
	Created time.Time `cbor:"at,omitzero"`
}

// ActiveJobs is the sharded active-job set jact/<sh2>.
func ActiveJobs(fleet *Bucket, shards int) *ShardSet[ActiveJob] {
	return NewShardSet(fleet, shards, func(sh string, m map[string]ActiveJob) *ActiveJobShard {
		return &ActiveJobShard{Shard: dynamitedb.Key(sh), Jobs: setNZ(m)}
	}, func(s *ActiveJobShard) map[string]ActiveJob { return s.Jobs.Value() })
}

// ActiveJobShard is one shard `jact/<sh2>` of the active-job set: jobID -> entry.
type ActiveJobShard struct {
	Shard dynamitedb.KeyField                        `pk:"jact" cbor:"-"`
	ETag  dynamitedb.ETagField                       `etag:"true" cbor:"-"`
	Jobs  dynamitedb.DataField[map[string]ActiveJob] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ActiveJobShard) Draft() *ActiveJobShard {
	return &ActiveJobShard{Shard: r.Shard, ETag: r.ETag, Jobs: Carry(r.Jobs)}
}

// JobRecord is the fleet-side truth of one job while it is not finalised (§05.4.3).
type JobRecord struct {
	ID   dynamitedb.KeyField  `pk:"job" cbor:"-"`
	ETag dynamitedb.ETagField `etag:"true" cbor:"-"`

	Spec    dynamitedb.DataField[[]byte] `cbor:"sp,omitempty"` // canonical CBOR JobSpec, ≤ 64 KiB
	SpecSig dynamitedb.DataField[[]byte] `cbor:"ss,omitempty"` // Ed25519("bob-jobspec-v1"‖0‖LV(sp)) by FleetConfig key kid
	Kid     dynamitedb.DataField[string] `cbor:"kid,omitempty"`

	State   dynamitedb.DataField[string]    `cbor:"st,omitempty"` // queued|claimed|running|succeeded|failed|cancelled
	Owner   dynamitedb.DataField[string]    `cbor:"o,omitempty"`  // wid while claimed/running
	Epoch   dynamitedb.DataField[int64]     `cbor:"ep,omitempty"` // +1 on every claim
	Attempt dynamitedb.DataField[int64]     `cbor:"at,omitempty"` // 1 on first claim, +1 on each requeue-claim
	Claimed dynamitedb.DataField[time.Time] `cbor:"ca,omitempty"`
	Changed dynamitedb.DataField[time.Time] `cbor:"ch,omitempty"`
	Queued  dynamitedb.DataField[time.Time] `cbor:"qa,omitempty"` // last time it became queued
	Cancel  dynamitedb.DataField[bool]      `cbor:"cx,omitempty"`

	Result    dynamitedb.DataField[[]byte] `cbor:"rs,omitempty"` // canonical CBOR JobResult
	ResultSig dynamitedb.DataField[[]byte] `cbor:"rg,omitempty"` // Ed25519 by Owner's sigpub over ("bob-jobres-v1"‖jobID‖epoch‖rs)

	// Promotion of a succeeded job (§06 §6.6.4): "" (pending) | "done" |
	// "blocked: staging incomplete" | "failed: artifact verification".
	Promotion       dynamitedb.DataField[string]   `cbor:"pm,omitempty"`
	NonReproducible dynamitedb.DataField[[]string] `cbor:"nr,omitempty"` // store paths (R-NFR-006)
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *JobRecord) Draft() *JobRecord {
	return &JobRecord{
		ID:              r.ID,
		ETag:            r.ETag,
		Spec:            Carry(r.Spec),
		SpecSig:         Carry(r.SpecSig),
		Kid:             Carry(r.Kid),
		State:           Carry(r.State),
		Owner:           Carry(r.Owner),
		Epoch:           Carry(r.Epoch),
		Attempt:         Carry(r.Attempt),
		Claimed:         Carry(r.Claimed),
		Changed:         Carry(r.Changed),
		Queued:          Carry(r.Queued),
		Cancel:          Carry(r.Cancel),
		Result:          Carry(r.Result),
		ResultSig:       Carry(r.ResultSig),
		Promotion:       Carry(r.Promotion),
		NonReproducible: Carry(r.NonReproducible),
	}
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
	ID           dynamitedb.KeyField                     `pk:"build" cbor:"-"`
	ETag         dynamitedb.ETagField                    `etag:"true" cbor:"-"`
	Project      dynamitedb.DataField[string]            `cbor:"pr,omitempty"`
	Number       dynamitedb.DataField[uint64]            `cbor:"n,omitempty"`
	State        dynamitedb.DataField[string]            `cbor:"st,omitempty"` // pending|queued|running|succeeded|failed|cancelled|superseded|skipped
	Phase        dynamitedb.DataField[string]            `cbor:"ph,omitempty"` // evaluating|building|promoting
	Trigger      dynamitedb.DataField[BuildTrigger]      `cbor:"tr,omitempty"`
	Branch       dynamitedb.DataField[string]            `cbor:"br,omitempty"`
	Commit       dynamitedb.DataField[string]            `cbor:"cm,omitempty"`
	Author       dynamitedb.DataField[string]            `cbor:"au,omitempty"` // commit author (control bucket only)
	Created      dynamitedb.DataField[time.Time]         `cbor:"cr,omitempty"`
	Started      dynamitedb.DataField[time.Time]         `cbor:"sa,omitempty"`
	Finished     dynamitedb.DataField[time.Time]         `cbor:"fi,omitempty"`
	Platforms    dynamitedb.DataField[[]string]          `cbor:"pl,omitempty"`
	SupersededBy dynamitedb.DataField[uint64]            `cbor:"sp,omitempty"`
	RetryOf      dynamitedb.DataField[uint64]            `cbor:"rt,omitempty"`
	Source       dynamitedb.DataField[BuildSource]       `cbor:"src,omitempty"`
	Targets      dynamitedb.DataField[[]string]          `cbor:"tg,omitempty"`
	Variants     dynamitedb.DataField[[]string]          `cbor:"va,omitempty"`
	Pipelines    dynamitedb.DataField[[]PipelineRun]     `cbor:"pi,omitempty"`
	Reported     dynamitedb.DataField[map[string]string] `cbor:"rp,omitempty"` // forge status context → last state sent
	Jobs         dynamitedb.DataField[[]BuildJob]        `cbor:"jb,omitempty"` // ≤ 1,024
	Result       dynamitedb.DataField[BuildResult]       `cbor:"rs,omitempty"`
	LogPartition dynamitedb.DataField[string]            `cbor:"lg,omitempty"`
	RequestID    dynamitedb.DataField[string]            `cbor:"rq,omitempty"`

	// Fields of the build controller (internal/build, §06 Build message) beyond the §05.4.4
	// list; all optional.
	Attempt   dynamitedb.DataField[int]           `cbor:"an,omitempty"`  // 1 + retries in the retry-of chain (0 = 1)
	RerunOf   dynamitedb.DataField[uint64]        `cbor:"ro,omitempty"`  // build number this build re-runs
	Pipeline  dynamitedb.DataField[string]        `cbor:"pn,omitempty"`  // CreateBuild{pipeline}: run this pipeline only
	Test      dynamitedb.DataField[bool]          `cbor:"ts,omitempty"`  // targets are tests (bob test --remote)
	Reason    dynamitedb.DataField[string]        `cbor:"why,omitempty"` // CreateBuild reason (≤ 200 bytes)
	Evaluated dynamitedb.DataField[bool]          `cbor:"ev,omitempty"`  // an evaluation result was accepted
	Counts    dynamitedb.DataField[PackageCounts] `cbor:"pc,omitempty"`
	Errors    dynamitedb.DataField[[]BuildError]  `cbor:"er,omitempty"` // first failures, ≤ 20
	// NonReproducible is JobRecord.nr of the finalised jobs, by job id (R-NFR-006).
	NonReproducible dynamitedb.DataField[map[string][]string] `cbor:"nr,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *BuildRecord) Draft() *BuildRecord {
	return &BuildRecord{
		ID:              r.ID,
		ETag:            r.ETag,
		Project:         Carry(r.Project),
		Number:          Carry(r.Number),
		State:           Carry(r.State),
		Phase:           Carry(r.Phase),
		Trigger:         Carry(r.Trigger),
		Branch:          Carry(r.Branch),
		Commit:          Carry(r.Commit),
		Author:          Carry(r.Author),
		Created:         Carry(r.Created),
		Started:         Carry(r.Started),
		Finished:        Carry(r.Finished),
		Platforms:       Carry(r.Platforms),
		SupersededBy:    Carry(r.SupersededBy),
		RetryOf:         Carry(r.RetryOf),
		Source:          Carry(r.Source),
		Targets:         Carry(r.Targets),
		Variants:        Carry(r.Variants),
		Pipelines:       Carry(r.Pipelines),
		Reported:        Carry(r.Reported),
		Jobs:            Carry(r.Jobs),
		Result:          Carry(r.Result),
		LogPartition:    Carry(r.LogPartition),
		RequestID:       Carry(r.RequestID),
		Attempt:         Carry(r.Attempt),
		RerunOf:         Carry(r.RerunOf),
		Pipeline:        Carry(r.Pipeline),
		Test:            Carry(r.Test),
		Reason:          Carry(r.Reason),
		Evaluated:       Carry(r.Evaluated),
		Counts:          Carry(r.Counts),
		Errors:          Carry(r.Errors),
		NonReproducible: Carry(r.NonReproducible),
	}
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
	return NewPagedIndex(control, 50, func(k uint64, e []BuildEntry) *BuildPage {
		n, _ := Num10(k)
		return &BuildPage{Project: dynamitedb.Key(projectID), Page: dynamitedb.Key(n), Entries: setNZ(e)}
	}, func(p *BuildPage) []BuildEntry { return p.Entries.Value() })
}

// BuildPage is one page `bpage/<projectID>/p/<num10>` of a project's build list.
type BuildPage struct {
	Project dynamitedb.KeyField                `pk:"bpage" cbor:"-"`
	Page    dynamitedb.KeyField                `sk:"p" cbor:"-"`
	ETag    dynamitedb.ETagField               `etag:"true" cbor:"-"`
	Entries dynamitedb.DataField[[]BuildEntry] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *BuildPage) Draft() *BuildPage {
	return &BuildPage{Project: r.Project, Page: r.Page, ETag: r.ETag, Entries: Carry(r.Entries)}
}

// BranchBuilds is the per-branch index of build numbers bbr/<projectID.enc(branch)>/p/<num10>
// (P = 50); its count lives in BranchCounter.
func BranchBuilds(control *Bucket, projectID, branch string) (*PagedIndex[uint64], error) {
	scope, err := Scoped(projectID, branch)
	if err != nil {
		return nil, err
	}
	return NewPagedIndex(control, 50, func(k uint64, e []uint64) *BranchPage {
		n, _ := Num10(k)
		return &BranchPage{Scope: dynamitedb.Key(scope), Page: dynamitedb.Key(n), Builds: setNZ(e)}
	}, func(p *BranchPage) []uint64 { return p.Builds.Value() }), nil
}

// BranchPage is one page `bbr/<projectID.enc(branch)>/p/<num10>` of build numbers.
type BranchPage struct {
	Scope  dynamitedb.KeyField            `pk:"bbr" cbor:"-"`
	Page   dynamitedb.KeyField            `sk:"p" cbor:"-"`
	ETag   dynamitedb.ETagField           `etag:"true" cbor:"-"`
	Builds dynamitedb.DataField[[]uint64] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *BranchPage) Draft() *BranchPage {
	return &BranchPage{Scope: r.Scope, Page: r.Page, ETag: r.ETag, Builds: Carry(r.Builds)}
}

// BranchCounter is `bbrm/<projectID.enc(branch)>`: the entry count of BranchBuilds.
type BranchCounter struct {
	Scope dynamitedb.KeyField          `pk:"bbrm" cbor:"-"`
	ETag  dynamitedb.ETagField         `etag:"true" cbor:"-"`
	Count dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *BranchCounter) Draft() *BranchCounter {
	return &BranchCounter{
		Scope: r.Scope,
		ETag:  r.ETag,
		Count: Carry(r.Count),
	}
}

// BranchCounterKey is the key model of the branch counter.
func BranchCounterKey(projectID, branch string) (*BranchCounter, error) {
	scope, err := Scoped(projectID, branch)
	if err != nil {
		return nil, err
	}
	return &BranchCounter{Scope: dynamitedb.Key(scope)}, nil
}

// ProjectCounters is `pctr/<projectID>`.
type ProjectCounters struct {
	Project  dynamitedb.KeyField          `pk:"pctr" cbor:"-"`
	ETag     dynamitedb.ETagField         `etag:"true" cbor:"-"`
	Builds   dynamitedb.DataField[uint64] `cbor:"b,omitempty"`
	Releases dynamitedb.DataField[uint64] `cbor:"r,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ProjectCounters) Draft() *ProjectCounters {
	return &ProjectCounters{
		Project:  r.Project,
		ETag:     r.ETag,
		Builds:   Carry(r.Builds),
		Releases: Carry(r.Releases),
	}
}

// RunningBuild is one entry of `bidx/running`.
type RunningBuild struct {
	Project string    `cbor:"p,omitempty"`
	N       uint64    `cbor:"n,omitempty"`
	Started time.Time `cbor:"sa,omitzero"`
}

// RunningBuilds is `bidx/running` (≤ 2,000 entries).
type RunningBuilds struct {
	Name   dynamitedb.KeyField                           `pk:"bidx" cbor:"-"` // "running"
	ETag   dynamitedb.ETagField                          `etag:"true" cbor:"-"`
	Builds dynamitedb.DataField[map[string]RunningBuild] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RunningBuilds) Draft() *RunningBuilds {
	return &RunningBuilds{
		Name:   r.Name,
		ETag:   r.ETag,
		Builds: Carry(r.Builds),
	}
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
	Name   dynamitedb.KeyField                 `pk:"bidx" cbor:"-"` // "failed"
	ETag   dynamitedb.ETagField                `etag:"true" cbor:"-"`
	Builds dynamitedb.DataField[[]FailedBuild] `cbor:"r,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *FailedBuilds) Draft() *FailedBuilds {
	return &FailedBuilds{
		Name:   r.Name,
		ETag:   r.ETag,
		Builds: Carry(r.Builds),
	}
}

// PromoEntry is a slot of the promotion log promo/<yyyymmdd>/s/<seq20> (tail promot/<yyyymmdd>).
type PromoEntry struct {
	Day       dynamitedb.KeyField             `pk:"promo" cbor:"-"`
	Seq       dynamitedb.KeyField             `sk:"s" cbor:"-"`
	ETag      dynamitedb.ETagField            `etag:"true" cbor:"-"`
	StoreHash dynamitedb.DataField[string]    `cbor:"sh,omitempty"`
	Project   dynamitedb.DataField[string]    `cbor:"p,omitempty"`
	Build     dynamitedb.DataField[string]    `cbor:"b,omitempty"`
	At        dynamitedb.DataField[time.Time] `cbor:"at,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *PromoEntry) Draft() *PromoEntry {
	return &PromoEntry{
		Day: r.Day, Seq: r.Seq, ETag: r.ETag, StoreHash: Carry(r.StoreHash),
		Project: Carry(r.Project), Build: Carry(r.Build), At: Carry(r.At),
	}
}

// PromoTail is the tail hint `promot/<yyyymmdd>` of a day's promotion log.
type PromoTail struct {
	Day  dynamitedb.KeyField          `pk:"promot" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	N    dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *PromoTail) Draft() *PromoTail { return &PromoTail{Day: r.Day, ETag: r.ETag, N: Carry(r.N)} }

// PromoLog is the promotion log of one UTC day. It has no head hint (cache GC walks old days).
func PromoLog(control *Bucket, day time.Time) *SeqLog[PromoEntry] {
	d := day.UTC().Format("20060102")
	return NewSeqLog(control, func(seq uint64) *PromoEntry {
		s, _ := Seq20(seq)
		return &PromoEntry{Day: dynamitedb.Key(d), Seq: dynamitedb.Key(s)}
	}, NewCounter(control, func(n uint64) *PromoTail {
		return &PromoTail{Day: dynamitedb.Key(d), N: setNZ(n)}
	}, func(h *PromoTail) uint64 { return h.N.Value() }), nil)
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
