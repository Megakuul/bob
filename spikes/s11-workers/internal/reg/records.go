package reg

import (
	"github.com/megakuul/dynamitedb"
)

// Fleet bucket layout (dynamitedb keys are "<pk-name>/<pk-value>"):
//
//	wreg/<wid>    Registration  worker-written (S3 policy: PutObject only, no delete)
//	worker/<wid>  WorkerRecord  orchestrator-written (S3 policy: read-only for workers)
//	whb/<wid>     Heartbeat     worker-written
//
// Control bucket (orchestrator only):
//
//	okey/<kid>    orchestrator token signing key

// Registration is written once by a starting worker (dynamitedb.Create = If-None-Match:*).
type Registration struct {
	ID   dynamitedb.KeyField  `pk:"wreg" cbor:"-"`
	ETag dynamitedb.ETagField `etag:"true" cbor:"-"`
	// Body is the canonical CBOR of RegBody; Sig is Ed25519(SigPub, "bob-wreg-v1"||Body).
	Body dynamitedb.DataField[[]byte] `cbor:"b,omitempty"`
	Sig  dynamitedb.DataField[[]byte] `cbor:"s,omitempty"`
}

// RegBody is what the worker signs.
type RegBody struct {
	V        int               `cbor:"1,keyasint"`
	WorkerID string            `cbor:"2,keyasint"`
	SigPub   []byte            `cbor:"3,keyasint"`
	Suite    uint16            `cbor:"4,keyasint"`
	KEMPub   []byte            `cbor:"5,keyasint"`
	Platform string            `cbor:"6,keyasint"` // e.g. "aarch64-linux"
	Caps     []string          `cbor:"7,keyasint,omitempty"`
	Labels   map[string]string `cbor:"8,keyasint,omitempty"`
	Created  int64             `cbor:"9,keyasint"`  // unix seconds
	Nonce    []byte            `cbor:"10,keyasint"` // 16 random bytes
}

// Worker states.
const (
	StateActive  = "active"
	StateRevoked = "revoked"
)

// WorkerRecord is owned by orchestrators. Workers can read it but not write it.
type WorkerRecord struct {
	ID   dynamitedb.KeyField  `pk:"worker" cbor:"-"`
	ETag dynamitedb.ETagField `etag:"true" cbor:"-"`

	State  dynamitedb.DataField[string] `cbor:"st,omitempty"`
	Gen    dynamitedb.DataField[int]    `cbor:"g,omitempty"` // bumped on revoke
	Serial dynamitedb.DataField[int]    `cbor:"n,omitempty"` // bumped on every token issue
	// Copies of the registered keys: rotation and PoP checks never re-read wreg/,
	// so a later overwrite of wreg/<wid> cannot redirect the token.
	SigPub   dynamitedb.DataField[[]byte] `cbor:"sp,omitempty"`
	Suite    dynamitedb.DataField[int]    `cbor:"su,omitempty"`
	KEMPub   dynamitedb.DataField[[]byte] `cbor:"kp,omitempty"`
	Platform dynamitedb.DataField[string] `cbor:"pl,omitempty"`
	Approved dynamitedb.DataField[int64]  `cbor:"ap,omitempty"`
	TokenExp dynamitedb.DataField[int64]  `cbor:"te,omitempty"`
	Sealed   dynamitedb.DataField[[]byte] `cbor:"ct,omitempty"` // HPKE(sealInfo(wid,gen,serial), TokenEnvelope)
}

// Heartbeat is refreshed by the worker; the orchestrator only rotates tokens of live workers.
type Heartbeat struct {
	ID   dynamitedb.KeyField          `pk:"whb" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	At   dynamitedb.DataField[int64]  `cbor:"at,omitempty"`
	Sig  dynamitedb.DataField[[]byte] `cbor:"s,omitempty"` // Ed25519 over wid||at
}

// OrchKey is the orchestrator token signing key (control bucket).
type OrchKey struct {
	KID  dynamitedb.KeyField          `pk:"okey" cbor:"-"`
	Seed dynamitedb.DataField[[]byte] `cbor:"seed,omitempty"`
}

// TokenEnvelope is the HPKE plaintext.
type TokenEnvelope struct {
	Token string    `cbor:"1,keyasint"`
	Exp   int64     `cbor:"2,keyasint"`
	S3    *S3Grant  `cbor:"3,keyasint,omitempty"`
}

// S3Grant carries optional short-lived S3 credentials (STS) for the worker.
type S3Grant struct {
	AK      string `cbor:"1,keyasint"`
	SK      string `cbor:"2,keyasint"`
	Session string `cbor:"3,keyasint"`
	Exp     int64  `cbor:"4,keyasint"`
}

// Claims are the signed contents of a worker token.
type Claims struct {
	V      int    `cbor:"1,keyasint"`
	Aud    string `cbor:"2,keyasint"` // "bob-worker-api"
	KID    string `cbor:"3,keyasint"`
	WID    string `cbor:"4,keyasint"`
	Gen    int    `cbor:"5,keyasint"`
	Serial int    `cbor:"6,keyasint"`
	Iat    int64  `cbor:"7,keyasint"`
	Exp    int64  `cbor:"8,keyasint"`
	Cnf    []byte `cbor:"9,keyasint"` // SHA-256 of the worker's Ed25519 public key
}
