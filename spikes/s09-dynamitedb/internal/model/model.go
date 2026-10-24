// Package model holds the spike's proposed key schema for the fleet and control buckets.
// Every key segment value must match ^[A-Za-z0-9@._-]{0,100}$ (dynamitedb keySanitizer).
package model

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	d "github.com/megakuul/dynamitedb"
)

// ---------- fleet bucket ----------

// Job states.
const (
	JobQueued    = "queued"
	JobClaimed   = "claimed" // lease held, worker preparing
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Lease is the part of a job that every CAS transition reads and writes.
// It is one DataField so a CustomUpdate can check and change all of it atomically.
type Lease struct {
	State    string    `cbor:"s"`
	Owner    string    `cbor:"o,omitempty"` // worker id
	Epoch    int       `cbor:"e"`           // +1 on every claim; fences stale workers
	Expires  time.Time `cbor:"x,omitempty"`
	Attempts int       `cbor:"a"`
	Changed  time.Time `cbor:"c"`
}

// LiveJob: key live/<platform>/j/<jobID>. Truth for one job while it is not terminal.
type LiveJob struct {
	Platform d.KeyField             `pk:"live" cbor:"-"`
	ID       d.KeyField             `sk:"j" cbor:"-"`
	ETag     d.ETagField            `etag:"true" cbor:"-"`
	Lease    d.DataField[Lease]     `cbor:"l,omitempty"`
	Build    d.DataField[string]    `cbor:"b,omitempty"`
	Needs    d.DataField[[]string]  `cbor:"n,omitempty"` // required capabilities/labels
	Project  d.DataField[string]    `cbor:"pr,omitempty"`
	Created  d.DataField[time.Time] `cbor:"t,omitempty"`
}

// JobRecord: key jobs/<buildID>/j/<jobID>. History, written once the job is terminal.
type JobRecord struct {
	Build    d.KeyField             `pk:"jobs" cbor:"-"`
	ID       d.KeyField             `sk:"j" cbor:"-"`
	Lease    d.DataField[Lease]     `cbor:"l,omitempty"`
	Platform d.DataField[string]    `cbor:"p,omitempty"`
	Created  d.DataField[time.Time] `cbor:"t,omitempty"`
	Result   d.DataField[string]    `cbor:"r,omitempty"`
}

// QueueEntry: key jobq/<platform>/j/<jobID>. Hint: "this job may be claimable".
// Written before the job is queued, deleted after it leaves queued.
type QueueEntry struct {
	Platform d.KeyField             `pk:"jobq" cbor:"-"`
	Job      d.KeyField             `sk:"j" cbor:"-"`
	ETag     d.ETagField            `etag:"true" cbor:"-"`
	Build    d.DataField[string]    `cbor:"b,omitempty"`
	Needs    d.DataField[[]string]  `cbor:"n,omitempty"`
	Since    d.DataField[time.Time] `cbor:"t,omitempty"`
}

// RunEntry: key jobrun/<shard>/j/<jobID>. Hint: "this job may hold a lease".
// Written by the claimer before the claim CAS, deleted after the job is terminal or requeued.
type RunEntry struct {
	Shard d.KeyField             `pk:"jobrun" cbor:"-"`
	Job   d.KeyField             `sk:"j" cbor:"-"`
	ETag  d.ETagField            `etag:"true" cbor:"-"`
	Build d.DataField[string]    `cbor:"b,omitempty"`
	Since d.DataField[time.Time] `cbor:"t,omitempty"`
}

// Worker: key worker/<workerID>.
type Worker struct {
	ID        d.KeyField              `pk:"worker" cbor:"-"`
	ETag      d.ETagField             `etag:"true" cbor:"-"`
	Platform  d.DataField[string]     `cbor:"p,omitempty"`
	Caps      d.DataField[[]string]   `cbor:"c,omitempty"`
	Labels    d.DataField[map[string]string] `cbor:"lb,omitempty"`
	State     d.DataField[string]     `cbor:"s,omitempty"` // pending|active|draining|gone
	Heartbeat d.DataField[time.Time]  `cbor:"h,omitempty"`
	PubKey    d.DataField[string]     `cbor:"k,omitempty"`
	Token     d.DataField[string]     `cbor:"tk,omitempty"` // encrypted to PubKey
}

// Controller: key ctl/<name>. Optional leader lease for one controller.
type Controller struct {
	Name  d.KeyField         `pk:"ctl" cbor:"-"`
	ETag  d.ETagField        `etag:"true" cbor:"-"`
	Lease d.DataField[Lease] `cbor:"l,omitempty"`
}

// Orchestrator: key orch/<id>. Heartbeat + endpoint, used by workers for discovery.
type Orchestrator struct {
	ID        d.KeyField             `pk:"orch" cbor:"-"`
	Endpoint  d.DataField[string]    `cbor:"u,omitempty"`
	Heartbeat d.DataField[time.Time] `cbor:"h,omitempty"`
}

// ---------- control bucket ----------

type Project struct {
	ID        d.KeyField          `pk:"project" cbor:"-"`
	ETag      d.ETagField         `etag:"true" cbor:"-"`
	Name      d.DataField[string] `cbor:"n,omitempty"`
	Repo      d.DataField[string] `cbor:"r,omitempty"`
	NextBuild d.DataField[int]    `cbor:"nb,omitempty"`
}

// Build: key build/<projectID>/n/<revNumber>. Newest first via inverted number.
type Build struct {
	Project d.KeyField             `pk:"build" cbor:"-"`
	Num     d.KeyField             `sk:"n" cbor:"-"`
	ETag    d.ETagField            `etag:"true" cbor:"-"`
	ID      d.DataField[string]    `cbor:"id,omitempty"`
	Number  d.DataField[int]       `cbor:"no,omitempty"`
	Branch  d.DataField[string]    `cbor:"br,omitempty"`
	Commit  d.DataField[string]    `cbor:"c,omitempty"`
	State   d.DataField[string]    `cbor:"s,omitempty"`
	Created d.DataField[time.Time] `cbor:"t,omitempty"`
}

// BuildRef: key buildidx/<kind>/b/<revTimeID>; kind = running | failed | all.
type BuildRef struct {
	Kind    d.KeyField          `pk:"buildidx" cbor:"-"`
	Ref     d.KeyField          `sk:"b" cbor:"-"`
	Project d.DataField[string] `cbor:"p,omitempty"`
	Num     d.DataField[int]    `cbor:"no,omitempty"`
}

// Release: key release/<projectID>/v/<sortableVersion>.
type Release struct {
	Project d.KeyField             `pk:"release" cbor:"-"`
	Version d.KeyField             `sk:"v" cbor:"-"`
	Public  d.DataField[bool]      `cbor:"pub,omitempty"`
	Created d.DataField[time.Time] `cbor:"t,omitempty"`
}

type User struct {
	ID    d.KeyField          `pk:"user" cbor:"-"`
	ETag  d.ETagField         `etag:"true" cbor:"-"`
	Name  d.DataField[string] `cbor:"n,omitempty"`
	Email d.DataField[string] `cbor:"e,omitempty"`
	Kind  d.DataField[string] `cbor:"k,omitempty"` // local|oidc
}

type Role struct {
	ID    d.KeyField              `pk:"role" cbor:"-"`
	ETag  d.ETagField             `etag:"true" cbor:"-"`
	Perms d.DataField[[]string]   `cbor:"p,omitempty"` // "<rpc>@<project|*>"
}

// UserRole: key userrole/<userID>/r/<roleID> and RoleUser: roleuser/<roleID>/u/<userID>.
// Rule: a pk name is used by pk-only types or by pk+sk types, never both (SeaweedFS file/dir clash).
type UserRole struct {
	User d.KeyField `pk:"userrole" cbor:"-"`
	Role d.KeyField `sk:"r" cbor:"-"`
	By   d.DataField[string] `cbor:"by,omitempty"`
}
type RoleUser struct {
	Role d.KeyField `pk:"roleuser" cbor:"-"`
	User d.KeyField `sk:"u" cbor:"-"`
	By   d.DataField[string] `cbor:"by,omitempty"`
}

// ---------- ids ----------

// NewID returns a 26-char time-sortable id (48-bit ms time + 80 random bits, Crockford base32).
func NewID(t time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(t.UnixMilli())<<16)
	rand.Read(b[6:])
	return crockford(b)
}

// NewRevID is like NewID but sorts newest first (time bits inverted).
func NewRevID(t time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], (uint64(math.MaxUint64>>16)-uint64(t.UnixMilli()))<<16)
	rand.Read(b[6:])
	return crockford(b)
}

const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func crockford(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alpha[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// RevNum renders n so that larger n sorts first: 10 digits of (9999999999-n).
func RevNum(n int) string { return fmt.Sprintf("%010d", 9999999999-n) }
