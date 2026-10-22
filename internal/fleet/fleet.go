// Package fleet holds the wire formats and the cryptography that workers and orchestrators
// share (SPEC §05.9.2, S11 §5.2/§5.3): worker ids, registration documents, sealed worker
// tokens, proofs of possession, signed heartbeats, signed job specs and job results, and the
// sealed checkout credential. It performs no I/O.
package fleet

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/megakuul/bob/internal/store"
)

// Domain separation strings: every signature and every HPKE info starts with one.
const (
	dsWorkerID  = "bob-worker-id-v1"
	dsReg       = "bob-wreg-v1"
	dsToken     = "bob-wtok-v1"
	dsSeal      = "bob-wseal-v1"
	dsPoP       = "bob-pop-v1"
	dsHeartbeat = "bob-whb-v1"
	dsSpec      = "bob-jobspec-v1"
	dsResult    = "bob-jobres-v1"
	dsCheckout  = "bob-wco-v1"
)

// Audience is the aud claim of every worker token.
const Audience = "bob-worker-api"

// Limits on hostile input (S11 §5.2).
const (
	MaxRegBody  = 4096
	MaxToken    = 2048
	MaxSkew     = 5 * time.Minute  // registration created in the future
	MaxPoPSkew  = 60 * time.Second // proof-of-possession timestamp
	PoPReplayed = 2 * MaxPoPSkew   // how long a PoP signature is remembered
)

// Suite selects the HPKE KEM; the KDF is HKDF-SHA256 and the AEAD ChaCha20-Poly1305.
type Suite uint16

// Supported suites. New workers use MLKEM768X25519; X25519 is accepted (S11 §5.3).
const (
	SuiteX25519         Suite = 0x0020
	SuiteMLKEM768X25519 Suite = 0x647a
)

// KEM returns the HPKE KEM of the suite.
func (s Suite) KEM() (hpke.KEM, error) {
	switch s {
	case SuiteX25519:
		return hpke.DHKEM(ecdh.X25519()), nil
	case SuiteMLKEM768X25519:
		return hpke.MLKEM768X25519(), nil
	}
	return nil, fmt.Errorf("unsupported KEM suite 0x%04x", uint16(s))
}

// String is the suite name stored in WorkerRecord.su.
func (s Suite) String() string { return "0x" + strconv.FormatUint(uint64(s), 16) }

// ParseSuite reads WorkerRecord.su.
func ParseSuite(s string) (Suite, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("unsupported KEM suite %q", s)
	}
	if _, err := Suite(n).KEM(); err != nil {
		return 0, err
	}
	return Suite(n), nil
}

var (
	kdf  = hpke.HKDFSHA256()
	aead = hpke.ChaCha20Poly1305()
)

var (
	canon, _  = cbor.CanonicalEncOptions().EncMode()
	strict, _ = cbor.DecOptions{MaxArrayElements: 1024, MaxMapPairs: 256, MaxNestedLevels: 8,
		DupMapKey: cbor.DupMapKeyEnforcedAPF}.DecMode()
)

// Encode is canonical CBOR (RFC 8949 §4.2.1), the encoding of every signed document.
func Encode(v any) ([]byte, error) { return canon.Marshal(v) }

// Decode reads CBOR with limits for hostile input.
func Decode(b []byte, v any) error { return strict.Unmarshal(b, v) }

// lv appends a 4-byte big-endian length and b.
func lv(buf *bytes.Buffer, b []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	buf.Write(l[:])
	buf.Write(b)
}

// msg is ds ‖ 0 ‖ LV(part)… — the input of every signature and HPKE info.
func msg(ds string, parts ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString(ds)
	b.WriteByte(0)
	for _, p := range parts {
		lv(&b, p)
	}
	return b.Bytes()
}

func itoa(n int64) []byte { return []byte(strconv.FormatInt(n, 10)) }

var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// WorkerID is "w" + base32(SHA-256("bob-worker-id-v1" ‖ LV(sigpub) ‖ u16(suite) ‖ LV(kempub)))[:26].
// The id is a hash of both keys, so a record cannot be re-pointed at other keys.
func WorkerID(sigPub []byte, s Suite, kemPub []byte) string {
	var b bytes.Buffer
	b.WriteString(dsWorkerID)
	lv(&b, sigPub)
	_ = binary.Write(&b, binary.BigEndian, uint16(s))
	lv(&b, kemPub)
	sum := sha256.Sum256(b.Bytes())
	return "w" + b32.EncodeToString(sum[:])[:26]
}

// RegBody is what a worker signs at registration (wreg/<wid>.b).
type RegBody struct {
	V        int      `cbor:"1,keyasint"`
	WorkerID string   `cbor:"2,keyasint"`
	SigPub   []byte   `cbor:"3,keyasint"`
	Suite    uint16   `cbor:"4,keyasint"`
	KEMPub   []byte   `cbor:"5,keyasint"`
	Platform string   `cbor:"6,keyasint"`
	Caps     []string `cbor:"7,keyasint,omitempty"`
	Labels   []string `cbor:"8,keyasint,omitempty"`
	Created  int64    `cbor:"9,keyasint"` // unix seconds
	Nonce    []byte   `cbor:"10,keyasint"`
}

// SignReg returns the canonical body and its signature.
func SignReg(key ed25519.PrivateKey, b RegBody) (body, sig []byte, err error) {
	body, err = Encode(b)
	if err != nil {
		return nil, nil, err
	}
	return body, ed25519.Sign(key, msg(dsReg, body)), nil
}

// ValidateReg checks a registration read from wreg/<wid> (hostile input, S11 §5.2): size,
// canonical CBOR, version, key sizes, suite, self-signature, wid = hash of the keys = object
// key, created within [now−window, now+5 min]. The error is the rejection reason.
func ValidateReg(wid string, body, sig []byte, now time.Time, window time.Duration) (*RegBody, error) {
	if len(body) == 0 || len(body) > MaxRegBody {
		return nil, fmt.Errorf("body size %d", len(body))
	}
	var b RegBody
	if err := Decode(body, &b); err != nil {
		return nil, fmt.Errorf("body is not CBOR: %v", err)
	}
	if c, err := Encode(b); err != nil || !bytes.Equal(c, body) {
		return nil, errors.New("body is not canonical CBOR")
	}
	if b.V != 1 || len(b.SigPub) != ed25519.PublicKeySize || len(b.Nonce) != 16 {
		return nil, errors.New("version, key or nonce size")
	}
	if _, err := Suite(b.Suite).KEM(); err != nil {
		return nil, err
	}
	if !ed25519.Verify(b.SigPub, msg(dsReg, body), sig) {
		return nil, errors.New("signature invalid")
	}
	if id := WorkerID(b.SigPub, Suite(b.Suite), b.KEMPub); id != b.WorkerID || id != wid {
		return nil, errors.New("worker id does not match keys")
	}
	created := time.Unix(b.Created, 0)
	if created.After(now.Add(MaxSkew)) || now.Sub(created) > window {
		return nil, fmt.Errorf("created %s outside the registration window", created.UTC().Format(time.RFC3339))
	}
	return &b, nil
}

// sealInfo binds a sealed token to the worker id, generation and serial: a ciphertext moved to
// another worker record, generation or serial fails to open.
func sealInfo(wid string, gen, serial int64) []byte {
	return msg(dsSeal, []byte(wid), itoa(gen), itoa(serial))
}

func seal(s Suite, kemPub, info, plaintext []byte) ([]byte, error) {
	kem, err := s.KEM()
	if err != nil {
		return nil, err
	}
	pk, err := kem.NewPublicKey(kemPub)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(pk, kdf, aead, info, plaintext)
}

// SealToken encrypts a token envelope to the worker KEM key (WorkerRecord.ct).
func SealToken(s Suite, kemPub []byte, wid string, gen, serial int64, env TokenEnvelope) ([]byte, error) {
	pt, err := Encode(env)
	if err != nil {
		return nil, err
	}
	return seal(s, kemPub, sealInfo(wid, gen, serial), pt)
}

// OpenToken decrypts WorkerRecord.ct with the worker's KEM key.
func OpenToken(k hpke.PrivateKey, wid string, gen, serial int64, ct []byte) (TokenEnvelope, error) {
	var env TokenEnvelope
	pt, err := hpke.Open(k, kdf, aead, sealInfo(wid, gen, serial), ct)
	if err != nil {
		return env, err
	}
	return env, Decode(pt, &env)
}

// TokenEnvelope is the HPKE plaintext of a sealed token.
type TokenEnvelope struct {
	Token string `cbor:"1,keyasint"`
	Exp   int64  `cbor:"2,keyasint"`
}

// Claims are the signed contents of a worker token (bwt1).
type Claims struct {
	V      int    `cbor:"1,keyasint"`
	Aud    string `cbor:"2,keyasint"`
	Kid    string `cbor:"3,keyasint"`
	Wid    string `cbor:"4,keyasint"`
	Gen    int64  `cbor:"5,keyasint"`
	Serial int64  `cbor:"6,keyasint"`
	Iat    int64  `cbor:"7,keyasint"`
	Exp    int64  `cbor:"8,keyasint"`
	Cnf    []byte `cbor:"9,keyasint"` // SHA-256 of the worker's Ed25519 public key
}

// SignToken returns "bwt1.<b64url canonical CBOR claims>.<b64url Ed25519 signature>".
func SignToken(key ed25519.PrivateKey, c Claims) (string, error) {
	cb, err := Encode(c)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(key, msg(dsToken, cb))
	return "bwt1." + base64.RawURLEncoding.EncodeToString(cb) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseToken splits and decodes a token. The signature is checked by VerifyToken.
func ParseToken(tok string) (c Claims, cb, sig []byte, err error) {
	parts := strings.Split(tok, ".")
	if len(tok) > MaxToken || len(parts) != 3 || parts[0] != "bwt1" {
		return c, nil, nil, errors.New("format")
	}
	if cb, err = base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return c, nil, nil, errors.New("encoding")
	}
	if sig, err = base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return c, nil, nil, errors.New("encoding")
	}
	if err := Decode(cb, &c); err != nil {
		return c, nil, nil, errors.New("claims")
	}
	return c, cb, sig, nil
}

// VerifyToken checks the token signature over the claims bytes.
func VerifyToken(pub ed25519.PublicKey, cb, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, msg(dsToken, cb), sig)
}

// Cnf is the key binding of a token: SHA-256 of the worker's Ed25519 public key.
func Cnf(sigPub []byte) []byte {
	s := sha256.Sum256(sigPub)
	return s[:]
}

// PoPMessage is the signed input of a proof of possession: method, path, unix time, a fresh
// nonce and the hash of the token it accompanies.
func PoPMessage(method, path string, ts int64, nonce []byte, token string) []byte {
	th := sha256.Sum256([]byte(token))
	return msg(dsPoP, []byte(method), []byte(path), itoa(ts), nonce, th[:])
}

// SignPoP returns the Bob-PoP header value "<unix ts>.<b64url nonce>.<b64url sig>".
func SignPoP(key ed25519.PrivateKey, token, method, path string, now time.Time, nonce []byte) string {
	ts := now.Unix()
	sig := ed25519.Sign(key, PoPMessage(method, path, ts, nonce, token))
	return strconv.FormatInt(ts, 10) + "." + base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ParsePoP splits a Bob-PoP header value.
func ParsePoP(pop string) (ts int64, nonce, sig []byte, err error) {
	parts := strings.Split(pop, ".")
	if len(pop) > 512 || len(parts) != 3 {
		return 0, nil, nil, errors.New("format")
	}
	if ts, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return 0, nil, nil, errors.New("format")
	}
	if nonce, err = base64.RawURLEncoding.DecodeString(parts[1]); err != nil || len(nonce) < 8 {
		return 0, nil, nil, errors.New("format")
	}
	if sig, err = base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return 0, nil, nil, errors.New("format")
	}
	return ts, nonce, sig, nil
}

// HeartbeatMessage is the signed input of `whb/<wid>`: wid, time and held jobs.
func HeartbeatMessage(wid string, at time.Time, jobs []store.HeldJobEpoch) []byte {
	parts := [][]byte{[]byte(wid), itoa(at.UnixNano())}
	for _, j := range jobs {
		parts = append(parts, []byte(j.Job), itoa(j.Epoch))
	}
	return msg(dsHeartbeat, parts...)
}

// SignHeartbeat signs a heartbeat record in place.
func SignHeartbeat(key ed25519.PrivateKey, hb *store.Heartbeat) {
	hb.Sig = ed25519.Sign(key, HeartbeatMessage(hb.Wid, hb.At, hb.Jobs))
}

// VerifyHeartbeat checks a heartbeat record against the worker's signing key.
func VerifyHeartbeat(pub ed25519.PublicKey, hb *store.Heartbeat) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, HeartbeatMessage(hb.Wid, hb.At, hb.Jobs), hb.Sig)
}

// SignSpec is JobRecord.ss: Ed25519("bob-jobspec-v1"‖0‖LV(sp)).
func SignSpec(key ed25519.PrivateKey, spec []byte) []byte {
	return ed25519.Sign(key, msg(dsSpec, spec))
}

// VerifySpec checks JobRecord.ss against any of the orchestrator keys.
func VerifySpec(keys []ed25519.PublicKey, spec, sig []byte) bool {
	m := msg(dsSpec, spec)
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, m, sig) {
			return true
		}
	}
	return false
}

// ResultMessage is the signed input of JobRecord.rg: job id, epoch and the result bytes.
func ResultMessage(jobID string, epoch int64, rs []byte) []byte {
	return msg(dsResult, []byte(jobID), itoa(epoch), rs)
}

// SignResult is JobRecord.rg by the owner's Ed25519 key.
func SignResult(key ed25519.PrivateKey, jobID string, epoch int64, rs []byte) []byte {
	return ed25519.Sign(key, ResultMessage(jobID, epoch, rs))
}

// VerifyResult checks JobRecord.rg against the owner's key.
func VerifyResult(pub ed25519.PublicKey, jobID string, epoch int64, rs, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, ResultMessage(jobID, epoch, rs), sig)
}

// CheckoutPlain is the plaintext of GetCheckoutCredentialResponse.sealed_checkout (§05.9.1).
type CheckoutPlain struct {
	CloneURL string `cbor:"1,keyasint"`
	Username string `cbor:"2,keyasint"`
	Token    string `cbor:"3,keyasint"`
	Expires  int64  `cbor:"4,keyasint"`
	FetchRef string `cbor:"5,keyasint,omitempty"`
	SHA      string `cbor:"6,keyasint,omitempty"`
}

func checkoutInfo(wid, jobID string, epoch int64) []byte {
	return msg(dsCheckout, []byte(wid), []byte(jobID), itoa(epoch))
}

// SealCheckout encrypts a checkout credential to the worker KEM key, bound to wid, job and
// epoch (info "bob-wco-v1"‖0‖LV(wid)‖LV(job_id)‖LV(epoch)).
func SealCheckout(s Suite, kemPub []byte, wid, jobID string, epoch int64, c CheckoutPlain) ([]byte, error) {
	pt, err := Encode(c)
	if err != nil {
		return nil, err
	}
	return seal(s, kemPub, checkoutInfo(wid, jobID, epoch), pt)
}

// OpenCheckout is SealCheckout's inverse on the worker.
func OpenCheckout(k hpke.PrivateKey, wid, jobID string, epoch int64, ct []byte) (CheckoutPlain, error) {
	var c CheckoutPlain
	pt, err := hpke.Open(k, kdf, aead, checkoutInfo(wid, jobID, epoch), ct)
	if err != nil {
		return c, err
	}
	return c, Decode(pt, &c)
}

// DecodeSpec reads a canonical CBOR JobSpec; ok is false for anything else.
func DecodeSpec(b []byte) (store.JobSpec, bool) {
	var s store.JobSpec
	if len(b) == 0 || len(b) > store.MaxJobSpec || Decode(b, &s) != nil || s.V != 1 {
		return store.JobSpec{}, false
	}
	return s, true
}

// DecodeResult reads JobRecord.rs.
func DecodeResult(b []byte) (store.JobResult, bool) {
	var r store.JobResult
	if len(b) == 0 || Decode(b, &r) != nil {
		return store.JobResult{}, false
	}
	return r, true
}
