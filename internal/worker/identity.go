package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/store"
)

// ErrRevoked is returned when the worker record says revoked: the process must exit; a new
// process registers with new keys (S11 §5.1).
var ErrRevoked = errors.New("worker revoked")

// Identity is a worker's key material: an Ed25519 signing key and an HPKE KEM key
// (MLKEM768-X25519), generated afresh at every process start (S11 §5.1). The worker id is a
// hash of the two public keys.
type Identity struct {
	// Profile is what the registration announces (platform, caps, labels).
	Profile store.WorkerProfile

	suite  fleet.Suite
	sig    ed25519.PrivateKey
	kem    hpke.PrivateKey
	kemPub []byte
	id     string
}

// NewIdentity generates keys from rand (crypto/rand in production).
func NewIdentity(rand io.Reader) (*Identity, error) {
	_, sig, err := ed25519.GenerateKey(rand)
	if err != nil {
		return nil, fmt.Errorf("worker: signing key: %w", err)
	}
	suite := fleet.SuiteMLKEM768X25519
	kem, err := suite.KEM()
	if err != nil {
		return nil, err
	}
	ikm := make([]byte, 64)
	if _, err := io.ReadFull(rand, ikm); err != nil {
		return nil, fmt.Errorf("worker: KEM key: %w", err)
	}
	dk, err := kem.DeriveKeyPair(ikm)
	if err != nil {
		return nil, fmt.Errorf("worker: KEM key: %w", err)
	}
	kemPub := dk.PublicKey().Bytes()
	pub := sig.Public().(ed25519.PublicKey)
	return &Identity{suite: suite, sig: sig, kem: dk, kemPub: kemPub, id: fleet.WorkerID(pub, suite, kemPub)}, nil
}

// ID is the worker id (wid).
func (i *Identity) ID() string { return i.id }

// SigningKey is the Ed25519 key that signs heartbeats, results and proofs of possession
// (Config.SigKey).
func (i *Identity) SigningKey() ed25519.PrivateKey { return i.sig }

// KEM is the HPKE private key that opens sealed tokens and checkout credentials.
func (i *Identity) KEM() hpke.PrivateKey { return i.kem }

// regDoc is the stored registration document: wreg/<wid> = {b: body, s: sig}.
type regDoc struct {
	B []byte `cbor:"b,omitempty"`
	S []byte `cbor:"s,omitempty"`
}

// RegBytes is the registration document of this identity at time now: canonical CBOR RegBody
// (id, Ed25519 and KEM public keys, time) and its Ed25519 signature, as stored in wreg/<wid>.
func (i *Identity) RegBytes(now time.Time) ([]byte, error) {
	return regBytes(i, i, i, now)
}

// ForgeRegBytes builds the document of an attacker: it claims sigOf's Ed25519 key and kemOf's
// KEM key but is signed by signer. A correct registrar rejects it.
func ForgeRegBytes(sigOf, kemOf, signer *Identity, now time.Time) ([]byte, error) {
	return regBytes(sigOf, kemOf, signer, now)
}

func regBytes(sigOf, kemOf, signer *Identity, now time.Time) ([]byte, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	pub := sigOf.sig.Public().(ed25519.PublicKey)
	b := fleet.RegBody{V: 1, WorkerID: fleet.WorkerID(pub, kemOf.suite, kemOf.kemPub), SigPub: pub,
		Suite: uint16(kemOf.suite), KEMPub: kemOf.kemPub, Platform: sigOf.Profile.Platform,
		Caps: sigOf.Profile.Caps, Labels: sigOf.Profile.Labels, Created: now.Unix(), Nonce: nonce}
	body, sig, err := fleet.SignReg(signer.sig, b)
	if err != nil {
		return nil, err
	}
	return fleet.Encode(regDoc{B: body, S: sig})
}

// Register writes `wreg/<wid>` (Create, canonical CBOR RegBody + Ed25519 signature) and
// appends the registration slot `wrs/reg/s/<seq>`. now is the registration time.
func Register(ctx context.Context, fleetB *store.Bucket, id *Identity, now time.Time) (seq uint64, err error) {
	doc, err := id.RegBytes(now)
	if err != nil {
		return 0, err
	}
	return RegisterRaw(ctx, fleetB, id.ID(), doc, now)
}

// RegisterRaw writes the document as wreg/<wid> (Create) and appends the registration slot.
func RegisterRaw(ctx context.Context, fleetB *store.Bucket, wid string, doc []byte, now time.Time) (uint64, error) {
	var d regDoc
	if err := fleet.Decode(doc, &d); err != nil {
		return 0, fmt.Errorf("worker: registration document: %w", err)
	}
	if err := store.Create(ctx, fleetB, &store.WorkerReg{Wid: wid, Body: d.B, Sig: d.S}); err != nil {
		return 0, err
	}
	return store.RegLog(fleetB).Append(ctx, &store.RegSlot{Wid: wid, At: now})
}

// RawReg reads the stored registration document of wid.
func RawReg(ctx context.Context, fleetB *store.Bucket, wid string) ([]byte, error) {
	r, err := store.Get(ctx, fleetB, &store.WorkerReg{Wid: wid})
	if err != nil {
		return nil, err
	}
	return fleet.Encode(regDoc{B: r.Body, S: r.Sig})
}

// Sealed is the sealed token of a worker record plus what binds it.
type Sealed struct {
	Bytes  []byte
	Serial uint64
	Gen    uint64
	Expiry time.Time
}

// FetchSealed reads `worker/<wid>`. store.ErrNotFound until the registrar approved it;
// ErrRevoked once it is revoked.
func FetchSealed(ctx context.Context, fleetB *store.Bucket, wid string) (*Sealed, error) {
	r, err := store.Get(ctx, fleetB, &store.WorkerRecord{Wid: wid})
	if err != nil {
		return nil, err
	}
	if r.State == "revoked" {
		return nil, fmt.Errorf("worker %s: %w", wid, ErrRevoked)
	}
	return &Sealed{Bytes: r.Sealed, Serial: uint64(r.Serial), Gen: uint64(r.Gen), Expiry: r.TokenExp}, nil
}

// Token is an opened worker token (`bwt1`).
type Token struct {
	raw    string
	exp    time.Time
	serial uint64
}

// OpenToken opens a sealed token with this identity's KEM key. Another identity, another
// serial or another generation fail with "message authentication failed".
func (i *Identity) OpenToken(s *Sealed) (*Token, error) {
	env, err := fleet.OpenToken(i.kem, i.id, int64(s.Gen), int64(s.Serial), s.Bytes)
	if err != nil {
		return nil, fmt.Errorf("worker: open sealed token: %w", err)
	}
	return &Token{raw: env.Token, exp: time.Unix(env.Exp, 0), serial: s.Serial}, nil
}

// Expiry is the token's expiry.
func (t *Token) Expiry() time.Time { return t.exp }

// Serial is the serial the token was sealed at.
func (t *Token) Serial() uint64 { return t.serial }

// Authorization is the value of the header "Authorization: Bob-Worker <bwt1 token>".
func (t *Token) Authorization() string { return "Bob-Worker " + t.raw }

// PoP returns the value of the header "Bob-PoP" for one request: an Ed25519 signature over
// the method, path, time and a fresh nonce by this identity.
func (t *Token) PoP(id *Identity, method, path string, now time.Time) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails (Go ≥ 1.24)
	return fleet.SignPoP(id.sig, t.raw, method, path, now, nonce)
}

// Heartbeat writes `whb/<wid>`: free slots and held jobs, signed with the Ed25519 key.
func Heartbeat(ctx context.Context, fleetB *store.Bucket, id *Identity, free int, jobs []string, now time.Time) error {
	held := make([]store.HeldJobEpoch, 0, len(jobs))
	for _, j := range jobs {
		held = append(held, store.HeldJobEpoch{Job: j})
	}
	return heartbeat(ctx, fleetB, id.id, id.sig, free, held, now)
}

func heartbeat(ctx context.Context, fleetB *store.Bucket, wid string, key ed25519.PrivateKey, free int, held []store.HeldJobEpoch, now time.Time) error {
	hb := &store.Heartbeat{Wid: wid, At: now.UTC(), Free: free, Jobs: held}
	fleet.SignHeartbeat(key, hb)
	return store.PutOwned(ctx, fleetB, hb)
}

// ForgeResult writes a job result for jobID at epoch as if owner had produced it, but signed
// by forger (a worker can overwrite any job record, ADR 0013 T10).
func ForgeResult(ctx context.Context, fleetB *store.Bucket, forger *Identity, owner, jobID string, epoch int64) error {
	rs, err := fleet.Encode(store.JobResult{V: 1, Status: "ok", Finished: time.Now().Unix()})
	if err != nil {
		return err
	}
	_, err = store.CAS(ctx, fleetB, &store.JobRecord{ID: jobID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		next := *cur
		next.State, next.Owner, next.Epoch = "succeeded", owner, epoch
		next.Result, next.ResultSig = rs, fleet.SignResult(forger.sig, jobID, epoch, rs)
		return &next, nil
	})
	return err
}
