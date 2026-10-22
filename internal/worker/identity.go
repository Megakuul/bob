package worker

import (
	"context"
	"io"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// Identity is a worker's key material: an Ed25519 signing key and an HPKE KEM key
// (MLKEM768-X25519), generated afresh at every process start (S11 §5.1). The worker id is a
// hash of the two public keys.
type Identity struct{}

// NewIdentity generates keys from rand (crypto/rand in production).
func NewIdentity(rand io.Reader) (*Identity, error) {
	return nil, diag.NotImplemented("worker.NewIdentity")
}

// ID is the worker id (wid).
func (i *Identity) ID() string { return "" }

// Register writes `wreg/<wid>` (Create, canonical CBOR RegBody + Ed25519 signature) and
// appends the registration slot `wrs/reg/s/<seq>`. now is the registration time.
func Register(ctx context.Context, fleet *store.Bucket, id *Identity, now time.Time) (seq uint64, err error) {
	return 0, diag.NotImplemented("worker.Register")
}

// Sealed is the sealed token of a worker record plus what binds it.
type Sealed struct {
	Bytes  []byte
	Serial uint64
	Gen    uint64
	Expiry time.Time
}

// FetchSealed reads `worker/<wid>`. store.ErrNotFound until the registrar approved it.
func FetchSealed(ctx context.Context, fleet *store.Bucket, wid string) (*Sealed, error) {
	return nil, diag.NotImplemented("worker.FetchSealed")
}

// Token is an opened worker token (`bwt1`).
type Token struct{}

// OpenToken opens a sealed token with this identity's KEM key. Another identity, another
// serial or another generation fail with "message authentication failed".
func (i *Identity) OpenToken(s *Sealed) (*Token, error) {
	return nil, diag.NotImplemented("worker.Identity.OpenToken")
}

// Authorization is the value of the header "Authorization: Bob-Worker <bwt1 token>".
func (t *Token) Authorization() string { return "" }

// PoP returns the value of the header "Bob-PoP" for one request: an Ed25519 signature over
// the method, path, time and a fresh nonce by this identity.
func (t *Token) PoP(id *Identity, method, path string, now time.Time) string { return "" }

// Heartbeat writes `whb/<wid>`: free slots and held jobs, signed with the Ed25519 key.
func Heartbeat(ctx context.Context, fleet *store.Bucket, id *Identity, free int, jobs []string, now time.Time) error {
	return diag.NotImplemented("worker.Heartbeat")
}

// RegBytes is the registration document of this identity at time now: canonical CBOR RegBody
// (id, Ed25519 and KEM public keys, time) and its Ed25519 signature, as stored in wreg/<wid>.
func (i *Identity) RegBytes(now time.Time) ([]byte, error) {
	return nil, diag.NotImplemented("worker.Identity.RegBytes")
}

// ForgeRegBytes builds the document of an attacker: it claims sigOf's Ed25519 key and kemOf's
// KEM key but is signed by signer. A correct registrar rejects it.
func ForgeRegBytes(sigOf, kemOf, signer *Identity, now time.Time) ([]byte, error) {
	return nil, diag.NotImplemented("worker.ForgeRegBytes")
}

// RegisterRaw writes the document as wreg/<wid> (Create) and appends the registration slot.
func RegisterRaw(ctx context.Context, fleet *store.Bucket, wid string, doc []byte, now time.Time) (uint64, error) {
	return 0, diag.NotImplemented("worker.RegisterRaw")
}

// RawReg reads the stored registration document of wid.
func RawReg(ctx context.Context, fleet *store.Bucket, wid string) ([]byte, error) {
	return nil, diag.NotImplemented("worker.RawReg")
}

// ForgeResult writes a job result for jobID at epoch as if owner had produced it, but signed
// by forger (a worker can overwrite any job record, ADR 0013 T10).
func ForgeResult(ctx context.Context, fleet *store.Bucket, forger *Identity, owner, jobID string, epoch int64) error {
	return diag.NotImplemented("worker.ForgeResult")
}
