package reg

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/megakuul/dynamitedb"
)

var enc, _ = cbor.CanonicalEncOptions().EncMode()

// Limits applied to hostile registration input [proposal].
const (
	MaxRegBody   = 4096
	MaxClockSkew = 5 * time.Minute
	MaxRegAge    = 24 * time.Hour // a registration older than this is never approved
)

// Errors the orchestrator reports for a rejected registration or request.
var (
	ErrRegInvalid   = errors.New("registration invalid")
	ErrTokenInvalid = errors.New("token invalid")
	ErrRevoked      = errors.New("worker revoked")
	ErrPoP          = errors.New("proof of possession invalid")
)

// guard converts a dynamitedb panic into an error (R-STORE-005).
func guard(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("dynamitedb panic: %v", r)
	}
}

// ---------------------------------------------------------------- worker side

// Register writes the self-signed registration record.
func Register(ctx context.Context, fleet *dynamitedb.Bucket, k *WorkerKeys, platform string, caps []string, now time.Time) (err error) {
	defer guard(&err)
	nonce := make([]byte, 16)
	rand.Read(nonce)
	body, err := enc.Marshal(RegBody{V: 1, WorkerID: k.ID(), SigPub: k.SigPub(), Suite: uint16(k.Suite), KEMPub: k.KEMPub,
		Platform: platform, Caps: caps, Created: now.Unix(), Nonce: nonce})
	if err != nil {
		return err
	}
	sig := ed25519.Sign(k.Sig, msg(dsReg, body))
	return dynamitedb.Create(ctx, fleet, &Registration{ID: dynamitedb.Key(k.ID()), Body: dynamitedb.Set(body), Sig: dynamitedb.Set(sig)})
}

// FetchToken reads worker/<wid> and opens the sealed token. ErrNotFound means "not yet approved".
func FetchToken(ctx context.Context, fleet *dynamitedb.Bucket, k *WorkerKeys) (env *TokenEnvelope, serial int, err error) {
	defer guard(&err)
	rec, err := dynamitedb.Get(ctx, fleet, &WorkerRecord{ID: dynamitedb.Key(k.ID())})
	if err != nil {
		return nil, 0, err
	}
	if rec.State.Value() != StateActive {
		return nil, 0, ErrRevoked
	}
	pt, err := k.Open(k.ID(), rec.Gen.Value(), rec.Serial.Value(), rec.Sealed.Value())
	if err != nil {
		return nil, 0, fmt.Errorf("open sealed token: %w", err)
	}
	env = &TokenEnvelope{}
	if err := cbor.Unmarshal(pt, env); err != nil {
		return nil, 0, err
	}
	return env, rec.Serial.Value(), nil
}

// Heartbeat refreshes whb/<wid>.
func Beat(ctx context.Context, fleet *dynamitedb.Bucket, k *WorkerKeys, now time.Time) (err error) {
	defer guard(&err)
	at := now.Unix()
	sig := ed25519.Sign(k.Sig, msg("bob-whb-v1", []byte(k.ID()), []byte(strconv.FormatInt(at, 10))))
	return dynamitedb.Put(ctx, fleet, &Heartbeat{ID: dynamitedb.Key(k.ID()), At: dynamitedb.Set(at), Sig: dynamitedb.Set(sig)})
}

// Request is the part of an RPC that the proof of possession covers.
type Request struct {
	Method, Authority, Path string
	Body                    []byte
}

// PoP signs a request: "bob-pop-v1" || method || authority || path || ts || sha256(body) || sha256(token).
func PoP(k *WorkerKeys, token string, r Request, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.Sig, popMsg(token, r, ts)))
}

func popMsg(token string, r Request, ts string) []byte {
	bh := sha256.Sum256(r.Body)
	th := sha256.Sum256([]byte(token))
	return msg(dsPoP, []byte(r.Method), []byte(r.Authority), []byte(r.Path), []byte(ts), bh[:], th[:])
}

// ---------------------------------------------------------- orchestrator side

// Orchestrator holds what one orchestrator needs. Several may run concurrently.
type Orchestrator struct {
	Fleet   *dynamitedb.Bucket
	KID     string
	Key     ed25519.PrivateKey
	Pubs    map[string]ed25519.PublicKey // kid -> key, for verification (old keys kept until tokens expire)
	TTL     time.Duration                // token lifetime
	HBStale time.Duration                // heartbeat older than this: no rotation
	Now     func() time.Time
	// MintS3, if set, adds short-lived S3 credentials to every token (STS).
	MintS3 func(ctx context.Context, wid string) (*S3Grant, error)

	mu     sync.Mutex
	replay map[string]time.Time
}

// Stats counts reconcile outcomes.
type Stats struct {
	Approved, Rotated, Rejected, LostRace, Skipped int
}

// ValidateRegistration checks a registration record (hostile input).
func ValidateRegistration(r *Registration, now time.Time) (*RegBody, error) {
	body, sig := r.Body.Value(), r.Sig.Value()
	if len(body) == 0 || len(body) > MaxRegBody {
		return nil, fmt.Errorf("%w: body size %d", ErrRegInvalid, len(body))
	}
	var b RegBody
	if err := cbor.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegInvalid, err)
	}
	if canon, err := enc.Marshal(b); err != nil || !bytes.Equal(canon, body) {
		return nil, fmt.Errorf("%w: body is not canonical CBOR", ErrRegInvalid)
	}
	if b.V != 1 || len(b.SigPub) != ed25519.PublicKeySize || len(b.Nonce) != 16 {
		return nil, fmt.Errorf("%w: version/key/nonce", ErrRegInvalid)
	}
	if _, err := Suite(b.Suite).kem(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegInvalid, err)
	}
	if !ed25519.Verify(b.SigPub, msg(dsReg, body), sig) {
		return nil, fmt.Errorf("%w: %v", ErrRegInvalid, ErrBadSignature)
	}
	if id := WorkerID(b.SigPub, Suite(b.Suite), b.KEMPub); id != b.WorkerID || id != r.ID.Value() {
		return nil, fmt.Errorf("%w: worker id does not match keys", ErrRegInvalid)
	}
	created := time.Unix(b.Created, 0)
	if created.After(now.Add(MaxClockSkew)) || now.Sub(created) > MaxRegAge {
		return nil, fmt.Errorf("%w: created %s outside window", ErrRegInvalid, created.UTC().Format(time.RFC3339))
	}
	return &b, nil
}

// Reconcile runs one pass over all registrations and worker records.
func (o *Orchestrator) Reconcile(ctx context.Context) (st Stats, err error) {
	defer guard(&err)
	now := o.Now()
	regs, err := dynamitedb.Query(ctx, o.Fleet, &Registration{ID: dynamitedb.KeyPrefix("")})
	if err != nil {
		return st, err
	}
	for _, r := range regs {
		wid := r.ID.Value()
		rec, err := dynamitedb.Get(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(wid)})
		switch {
		case errors.Is(err, dynamitedb.ErrNotFound):
			b, verr := ValidateRegistration(r, now)
			if verr != nil {
				st.Rejected++
				continue
			}
			if err := o.approve(ctx, b, now); errors.Is(err, dynamitedb.ErrAlreadyExists) {
				st.LostRace++
			} else if err != nil {
				return st, err
			} else {
				st.Approved++
			}
		case err != nil:
			return st, err
		default:
			ok, err := o.maybeRotate(ctx, rec, now, false)
			if errors.Is(err, dynamitedb.ErrConcurrencyConflict) {
				st.LostRace++
			} else if err != nil {
				return st, err
			} else if ok {
				st.Rotated++
			} else {
				st.Skipped++
			}
		}
	}
	return st, nil
}

func (o *Orchestrator) approve(ctx context.Context, b *RegBody, now time.Time) error {
	sealed, exp, err := o.issue(ctx, b.WorkerID, Suite(b.Suite), b.KEMPub, b.SigPub, 0, 1, now)
	if err != nil {
		return err
	}
	return dynamitedb.Create(ctx, o.Fleet, &WorkerRecord{
		ID: dynamitedb.Key(b.WorkerID), State: dynamitedb.Set(StateActive), Gen: dynamitedb.Set(0), Serial: dynamitedb.Set(1),
		SigPub: dynamitedb.Set(b.SigPub), Suite: dynamitedb.Set(int(b.Suite)), KEMPub: dynamitedb.Set(b.KEMPub),
		Platform: dynamitedb.Set(b.Platform), Approved: dynamitedb.Set(now.Unix()), TokenExp: dynamitedb.Set(exp), Sealed: dynamitedb.Set(sealed),
	})
}

// maybeRotate reissues the token when less than half its lifetime is left and the worker is alive.
func (o *Orchestrator) maybeRotate(ctx context.Context, rec *WorkerRecord, now time.Time, force bool) (bool, error) {
	if rec.State.Value() != StateActive {
		return false, nil
	}
	left := time.Unix(rec.TokenExp.Value(), 0).Sub(now)
	if left > o.TTL/2 && !force {
		return false, nil
	}
	wid := rec.ID.Value()
	hb, err := dynamitedb.Get(ctx, o.Fleet, &Heartbeat{ID: dynamitedb.Key(wid)})
	if err != nil || now.Sub(time.Unix(hb.At.Value(), 0)) > o.HBStale ||
		!ed25519.Verify(rec.SigPub.Value(), msg("bob-whb-v1", []byte(wid), []byte(strconv.FormatInt(hb.At.Value(), 10))), hb.Sig.Value()) {
		return false, nil // dead or forged heartbeat: let the token expire
	}
	serial := rec.Serial.Value() + 1
	sealed, exp, err := o.issue(ctx, wid, Suite(rec.Suite.Value()), rec.KEMPub.Value(), rec.SigPub.Value(), rec.Gen.Value(), serial, now)
	if err != nil {
		return false, err
	}
	return true, dynamitedb.Update(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(wid), ETag: rec.ETag,
		Serial: dynamitedb.Set(serial), TokenExp: dynamitedb.Set(exp), Sealed: dynamitedb.Set(sealed)})
}

func (o *Orchestrator) issue(ctx context.Context, wid string, s Suite, kemPub, sigPub []byte, gen, serial int, now time.Time) ([]byte, int64, error) {
	exp := now.Add(o.TTL).Unix()
	cnf := sha256.Sum256(sigPub)
	tok, err := o.SignToken(Claims{V: 1, Aud: "bob-worker-api", KID: o.KID, WID: wid, Gen: gen, Serial: serial, Iat: now.Unix(), Exp: exp, Cnf: cnf[:]})
	if err != nil {
		return nil, 0, err
	}
	env := TokenEnvelope{Token: tok, Exp: exp}
	if o.MintS3 != nil {
		g, err := o.MintS3(ctx, wid)
		if err != nil {
			return nil, 0, fmt.Errorf("mint s3 credentials: %w", err)
		}
		env.S3 = g
	}
	pt, err := enc.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	sealed, err := Seal(s, kemPub, wid, gen, serial, pt)
	return sealed, exp, err
}

// SignToken returns "bwt1.<b64url cbor claims>.<b64url ed25519 sig>".
func (o *Orchestrator) SignToken(c Claims) (string, error) {
	cb, err := enc.Marshal(c)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(o.Key, msg(dsToken, cb))
	return "bwt1." + base64.RawURLEncoding.EncodeToString(cb) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Revoke marks a worker revoked and bumps its generation (CAS on the record ETag).
func (o *Orchestrator) Revoke(ctx context.Context, wid string) (err error) {
	defer guard(&err)
	for range 5 {
		rec, err := dynamitedb.Get(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(wid)})
		if err != nil {
			return err
		}
		err = dynamitedb.Update(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(wid), ETag: rec.ETag,
			State: dynamitedb.Set(StateRevoked), Gen: dynamitedb.Increment(1), Sealed: dynamitedb.Set([]byte{}), TokenExp: dynamitedb.Set(int64(0))})
		if !errors.Is(err, dynamitedb.ErrConcurrencyConflict) {
			return err
		}
	}
	return dynamitedb.ErrConcurrencyConflict
}

// Verify checks token + proof of possession for one request. It reads the worker
// record (the SPEC would cache it for a few seconds; the spike reads it every time).
func (o *Orchestrator) Verify(ctx context.Context, token, pop string, r Request) (wid string, err error) {
	defer guard(&err)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "bwt1" || len(token) > 2048 {
		return "", fmt.Errorf("%w: format", ErrTokenInvalid)
	}
	cb, err1 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("%w: encoding", ErrTokenInvalid)
	}
	var c Claims
	if err := cbor.Unmarshal(cb, &c); err != nil {
		return "", fmt.Errorf("%w: claims", ErrTokenInvalid)
	}
	pub, ok := o.Pubs[c.KID]
	if !ok || !ed25519.Verify(pub, msg(dsToken, cb), sig) {
		return "", fmt.Errorf("%w: signature", ErrTokenInvalid)
	}
	now := o.Now()
	if c.V != 1 || c.Aud != "bob-worker-api" || now.Unix() >= c.Exp {
		return "", fmt.Errorf("%w: expired or wrong audience", ErrTokenInvalid)
	}
	rec, err := dynamitedb.Get(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(c.WID)})
	if err != nil {
		return "", fmt.Errorf("%w: worker record: %v", ErrTokenInvalid, err)
	}
	if rec.State.Value() != StateActive || rec.Gen.Value() != c.Gen {
		return "", ErrRevoked
	}
	// current and previous serial are valid; anything older was rotated away.
	if c.Serial < rec.Serial.Value()-1 {
		return "", fmt.Errorf("%w: superseded serial %d (current %d)", ErrTokenInvalid, c.Serial, rec.Serial.Value())
	}
	cnf := sha256.Sum256(rec.SigPub.Value())
	if !bytes.Equal(cnf[:], c.Cnf) {
		return "", fmt.Errorf("%w: key binding", ErrTokenInvalid)
	}
	ts, psig, ok := strings.Cut(pop, ".")
	t, err := strconv.ParseInt(ts, 10, 64)
	ps, err2 := base64.RawURLEncoding.DecodeString(psig)
	if !ok || err != nil || err2 != nil {
		return "", fmt.Errorf("%w: format", ErrPoP)
	}
	if d := now.Sub(time.Unix(t, 0)); d > 60*time.Second || d < -60*time.Second {
		return "", fmt.Errorf("%w: timestamp skew %s", ErrPoP, d)
	}
	if !ed25519.Verify(rec.SigPub.Value(), popMsg(token, r, ts), ps) {
		return "", fmt.Errorf("%w: signature", ErrPoP)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.replay == nil {
		o.replay = map[string]time.Time{}
	}
	if _, seen := o.replay[psig]; seen {
		return "", fmt.Errorf("%w: replayed", ErrPoP)
	}
	o.replay[psig] = now
	return c.WID, nil
}

// MarshalBody encodes a RegBody canonically (test helper for forged records).
func MarshalBody(b RegBody) []byte {
	out, _ := enc.Marshal(b)
	return out
}

// RegisterAt overwrites wreg/<id> with a record signed by k (attack simulation: Put, not Create).
func RegisterAt(ctx context.Context, fleet *dynamitedb.Bucket, k *WorkerKeys, id string, now time.Time) (err error) {
	defer guard(&err)
	nonce := make([]byte, 16)
	rand.Read(nonce)
	body := MarshalBody(RegBody{V: 1, WorkerID: k.ID(), SigPub: k.SigPub(), Suite: uint16(k.Suite), KEMPub: k.KEMPub,
		Platform: "aarch64-linux", Created: now.Unix(), Nonce: nonce})
	sig := ed25519.Sign(k.Sig, msg(dsReg, body))
	return dynamitedb.Put(ctx, fleet, &Registration{ID: dynamitedb.Key(id), Body: dynamitedb.Set(body), Sig: dynamitedb.Set(sig)})
}

// ForceRotate issues a new token now (e.g. after a suspected token leak).
func (o *Orchestrator) ForceRotate(ctx context.Context, wid string) (err error) {
	defer guard(&err)
	rec, err := dynamitedb.Get(ctx, o.Fleet, &WorkerRecord{ID: dynamitedb.Key(wid)})
	if err != nil {
		return err
	}
	ok, err := o.maybeRotate(ctx, rec, o.Now(), true)
	if err == nil && !ok {
		return errors.New("not rotated (revoked or no live heartbeat)")
	}
	return err
}
