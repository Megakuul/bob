package orchestrator

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/store"
)

// ErrUnauthenticated marks every refusal of Authenticate; the message after it says why. It is
// the API's sentinel, so the API boundary maps it to Unauthenticated with errors.Is.
var ErrUnauthenticated = api.ErrWorkerUnauthenticated

func unauth(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUnauthenticated}, args...)...)
}

// recordCache is how long Authenticate trusts a worker record it read (R-NFR-107: revocation
// effective within 10 s on every orchestrator).
const recordCache = 10 * time.Second

// Registrar is the registrar controller and the worker authentication of an orchestrator
// (SPEC §05.6.3, §05.9.2).
type Registrar struct {
	cfg RegistrarConfig
	pub ed25519.PublicKey

	mu     sync.Mutex
	recs   map[string]cachedRecord // wid -> worker record
	replay map[string]time.Time    // PoP signature -> first use
}

type cachedRecord struct {
	rec *store.WorkerRecord
	at  time.Time
}

// RegistrarConfig configures a Registrar.
type RegistrarConfig struct {
	Control, Fleet *store.Bucket
	Name           string             // orchestrator id
	Key            ed25519.PrivateKey // signs and verifies tokens (FleetConfig key kid)
	Kid            string
	TokenTTL       time.Duration // 1 h
	RegWindow      time.Duration // registrations older than this are rejected (24 h)
	Heartbeat      time.Duration // fleet.heartbeat
	MaxPerPass     int           // approvals per pass (50)
	Now            func() time.Time

	// VerifyKeys are further token keys by kid (keys being rotated out); may be nil.
	VerifyKeys map[string]ed25519.PublicKey
	// WorkerShards is fleet.worker_shards (widx); 0 means 4.
	WorkerShards int
}

// NewRegistrar performs no I/O.
func NewRegistrar(cfg RegistrarConfig) (*Registrar, error) {
	if cfg.Fleet == nil || len(cfg.Key) != ed25519.PrivateKeySize || cfg.Kid == "" || cfg.Name == "" {
		return nil, errors.New("orchestrator: registrar needs the fleet bucket, a name and an Ed25519 key with its kid")
	}
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.RegWindow <= 0 {
		cfg.RegWindow = 24 * time.Hour
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	if cfg.MaxPerPass <= 0 {
		cfg.MaxPerPass = 50
	}
	if cfg.WorkerShards <= 0 {
		cfg.WorkerShards = 4
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Registrar{cfg: cfg, pub: cfg.Key.Public().(ed25519.PublicKey),
		recs: map[string]cachedRecord{}, replay: map[string]time.Time{}}, nil
}

// RegPass is what one registrar pass did.
type RegPass struct {
	Approved int
	Rejected []string // reasons, "worker registration rejected: <reason>"
}

// Pass reads `wrs/reg/s/<cursor+1…>` until 404, validates each registration (S11 §5.2) and
// approves at most MaxPerPass of them. Validation failures are rejected and skipped.
func (r *Registrar) Pass(ctx context.Context) (RegPass, error) {
	var res RegPass
	cur, err := r.cursor(ctx)
	if err != nil {
		return res, err
	}
	inbox := store.RegLog(r.cfg.Fleet)
	done := cur
	for res.Approved < r.cfg.MaxPerPass {
		slot, err := inbox.Read(ctx, done+1)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return res, err
		}
		approved, reason, err := r.consider(ctx, slot.Wid.Value())
		if err != nil {
			return res, err
		}
		if approved {
			res.Approved++
		}
		if reason != "" {
			msg := "worker registration rejected: " + reason
			slog.Warn(msg, "wid", slot.Wid.Value(), "orchestrator", r.cfg.Name)
			res.Rejected = append(res.Rejected, msg)
		}
		done++
	}
	if done > cur {
		if err := r.saveCursor(ctx, done); err != nil {
			return res, err
		}
	}
	return res, nil
}

// cursor is ctl/registrar.cu: the last registration slot every registrar has handled.
func (r *Registrar) cursor(ctx context.Context) (uint64, error) {
	l, err := store.Get(ctx, r.cfg.Fleet, &store.ControllerLease{Name: store.ID("registrar")})
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseUint(string(l.Cursor.Value()), 10, 64)
	return n, nil
}

// saveCursor raises ctl/registrar.cu (monotonic CAS; the lease fields are kept).
func (r *Registrar) saveCursor(ctx context.Context, n uint64) error {
	err := store.Change(ctx, r.cfg.Fleet, &store.ControllerLease{Name: store.ID("registrar")}, func(cur *store.ControllerLease) (*store.ControllerLease, error) {
		next := store.ControllerLease{Name: store.ID("registrar")}
		if cur != nil {
			if old, _ := strconv.ParseUint(string(cur.Cursor.Value()), 10, 64); old >= n {
				return nil, store.ErrNoChange
			}
			next = *cur
		}
		next.Cursor = store.Set([]byte(strconv.FormatUint(n, 10)))
		return &next, nil
	})
	return err
}

// consider validates the registration of wid and creates its worker record. approved is
// true only for the registrar whose Create won; reason is set for a rejection.
func (r *Registrar) consider(ctx context.Context, wid string) (approved bool, reason string, err error) {
	if _, err := store.Segment(wid); err != nil {
		return false, "invalid worker id", nil
	}
	reg, err := store.Get(ctx, r.cfg.Fleet, &store.WorkerReg{Wid: store.ID(wid)})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, "registration record missing", nil
	case err != nil && decodeFailed(err):
		// Any worker may overwrite wreg/ (S11 §3.4); garbage is a rejection, not an error.
		return false, "registration record unreadable", nil
	case err != nil:
		return false, "", err
	}
	now := r.cfg.Now()
	body, verr := fleet.ValidateReg(wid, reg.Body.Value(), reg.Sig.Value(), now, r.cfg.RegWindow)
	if verr != nil {
		return false, verr.Error(), nil
	}
	suite := fleet.Suite(body.Suite)
	rec := &store.WorkerRecord{Wid: store.ID(wid), State: store.Set[string]("active"), Gen: store.Set[int64](0), Serial: store.Set[int64](1), SigPub: store.Set(body.SigPub), Suite: store.Set(suite.String()),
		KEMPub: store.Set(body.KEMPub), Platform: store.Set(body.Platform), Caps: store.Set(body.Caps), Labels: store.Set(body.Labels)}
	if err := r.issue(rec, wid, 0, 1, suite.String(), body.SigPub, body.KEMPub, now); err != nil {
		return false, "", err
	}
	if err := store.Create(ctx, r.cfg.Fleet, rec); errors.Is(err, store.ErrExists) {
		return false, "", nil // another registrar approved it
	} else if err != nil {
		return false, "", err
	}
	entry := store.WorkerIndexEntry{Platform: body.Platform, State: "active"}
	if err := store.WorkerIndex(r.cfg.Fleet, r.cfg.WorkerShards).Add(ctx, wid, entry); err != nil {
		return true, "", err
	}
	return true, "", nil
}

// issue signs a token for rec's current gen and serial and seals it into rec.
// issue seals a new token for worker wid (generation gen, serial) into rec.
func (r *Registrar) issue(rec *store.WorkerRecord, wid string, gen, serial int64, suiteName string, sigPub, kemPub []byte, now time.Time) error {
	suite, err := fleet.ParseSuite(suiteName)
	if err != nil {
		return err
	}
	exp := now.Add(r.cfg.TokenTTL)
	tok, err := fleet.SignToken(r.cfg.Key, fleet.Claims{V: 1, Aud: fleet.Audience, Kid: r.cfg.Kid, Wid: wid,
		Gen: gen, Serial: serial, Iat: now.Unix(), Exp: exp.Unix(), Cnf: fleet.Cnf(sigPub)})
	if err != nil {
		return err
	}
	sealed, err := fleet.SealToken(suite, kemPub, wid, gen, serial, fleet.TokenEnvelope{Token: tok, Exp: exp.Unix()})
	if err != nil {
		return err
	}
	rec.Sealed, rec.TokenExp = store.Set(sealed), store.Set(time.Unix(exp.Unix(), 0).UTC())
	return nil
}

func (r *Registrar) remember(rec *store.WorkerRecord, now time.Time) {
	r.mu.Lock()
	r.recs[rec.Wid.Value()] = cachedRecord{rec: rec, at: now}
	r.mu.Unlock()
}

// record returns the worker record, cached for at most recordCache.
func (r *Registrar) record(ctx context.Context, wid string, now time.Time) (*store.WorkerRecord, error) {
	r.mu.Lock()
	c, ok := r.recs[wid]
	r.mu.Unlock()
	if ok && now.Sub(c.at) < recordCache && !now.Before(c.at) {
		return c.rec, nil
	}
	rec, err := store.Get(ctx, r.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil {
		return nil, err
	}
	r.remember(rec, now)
	return rec, nil
}

func (r *Registrar) tokenKey(kid string) ed25519.PublicKey {
	if kid == r.cfg.Kid {
		return r.pub
	}
	return r.cfg.VerifyKeys[kid]
}

// Authenticate checks `Authorization: Bob-Worker <token>` and `Bob-PoP` for one request and
// returns the worker id. Errors read like "unauthenticated: proof of possession invalid",
// "unauthenticated: replayed", "unauthenticated: worker revoked",
// "unauthenticated: superseded serial 3 (current 5)", "unauthenticated: token expired".
func (r *Registrar) Authenticate(ctx context.Context, authorization, pop, method, path string) (string, error) {
	raw, ok := strings.CutPrefix(authorization, "Bob-Worker ")
	if !ok {
		return "", unauth("worker token invalid")
	}
	c, cb, sig, err := fleet.ParseToken(raw)
	if err != nil {
		return "", unauth("worker token invalid")
	}
	if !fleet.VerifyToken(r.tokenKey(c.Kid), cb, sig) || c.V != 1 || c.Aud != fleet.Audience {
		return "", unauth("worker token invalid")
	}
	now := r.cfg.Now()
	if now.Unix() >= c.Exp {
		return "", unauth("token expired")
	}
	rec, err := r.record(ctx, c.Wid, now)
	if errors.Is(err, store.ErrNotFound) {
		return "", unauth("worker token invalid")
	}
	if err != nil {
		return "", err
	}
	if rec.State.Value() == "revoked" || rec.State.Value() == "gone" || rec.Gen.Value() != c.Gen {
		return "", unauth("worker revoked")
	}
	if c.Serial < rec.Serial.Value()-1 {
		return "", unauth("superseded serial %d (current %d)", c.Serial, rec.Serial.Value())
	}
	if string(c.Cnf) != string(fleet.Cnf(rec.SigPub.Value())) {
		return "", unauth("worker token invalid")
	}
	ts, nonce, psig, err := fleet.ParsePoP(pop)
	if err != nil {
		return "", unauth("proof of possession invalid")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > fleet.MaxPoPSkew || d < -fleet.MaxPoPSkew {
		return "", unauth("proof of possession invalid")
	}
	if !ed25519.Verify(rec.SigPub.Value(), fleet.PoPMessage(method, path, ts, nonce, raw), psig) {
		return "", unauth("proof of possession invalid")
	}
	key := base64.RawURLEncoding.EncodeToString(psig)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, at := range r.replay {
		if now.Sub(at) > fleet.PoPReplayed {
			delete(r.replay, k)
		}
	}
	if _, seen := r.replay[key]; seen {
		return "", unauth("replayed")
	}
	r.replay[key] = now
	return c.Wid, nil
}

// Rotate issues new tokens at half-life for workers with a fresh signed heartbeat.
func (r *Registrar) Rotate(ctx context.Context) (rotated int, err error) {
	all, err := store.WorkerIndex(r.cfg.Fleet, r.cfg.WorkerShards).All(ctx)
	if err != nil {
		return 0, err
	}
	for wid := range all {
		ok, err := r.rotate(ctx, wid, false)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return rotated, err
		}
		if ok {
			rotated++
		}
	}
	return rotated, nil
}

// ForceRotate issues a new token for wid at once (admin / test).
func (r *Registrar) ForceRotate(ctx context.Context, wid string) error {
	ok, err := r.rotate(ctx, wid, true)
	if err == nil && !ok {
		err = fmt.Errorf("orchestrator: worker %s: %w", wid, errNotActive)
	}
	return err
}

var errNotActive = errors.New("worker is not active (revoked)")

// rotate issues the next serial: when forced, or when less than half the token lifetime is left
// and the worker's signed heartbeat is fresh (a dead worker's token just expires).
func (r *Registrar) rotate(ctx context.Context, wid string, force bool) (bool, error) {
	now := r.cfg.Now()
	rec, err := store.Get(ctx, r.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil {
		return false, err
	}
	if rec.State.Value() != "active" {
		return false, nil
	}
	if !force {
		if rec.TokenExp.Value().Sub(now) >= r.cfg.TokenTTL/2 || !r.alive(ctx, rec, now) {
			return false, nil
		}
	}
	serial := rec.Serial.Value()
	next, err := store.CAS(ctx, r.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
		if cur == nil || cur.State.Value() != "active" || cur.Serial.Value() != serial {
			return nil, store.ErrNoChange
		}
		n := *cur
		n.Serial = store.Set(cur.Serial.Value() + 1)
		if err := r.issue(&n, cur.Wid.Value(), cur.Gen.Value(), cur.Serial.Value()+1, cur.Suite.Value(), cur.SigPub.Value(), cur.KEMPub.Value(), now); err != nil {
			return nil, err
		}
		return &n, nil
	})
	if err != nil || next == nil || next.Serial.Value() == serial {
		return false, err
	}
	r.remember(next, now)
	return true, nil
}

// alive reports a fresh heartbeat signed by the worker's registered key.
func (r *Registrar) alive(ctx context.Context, rec *store.WorkerRecord, now time.Time) bool {
	hb, err := store.Get(ctx, r.cfg.Fleet, &store.Heartbeat{Wid: rec.Wid})
	if err != nil || now.Sub(hb.At.Value()) >= 2*r.cfg.Heartbeat {
		return false
	}
	return fleet.VerifyHeartbeat(rec.SigPub.Value(), hb)
}

// Revoke revokes a worker: generation + 1, sealed token cleared.
func (r *Registrar) Revoke(ctx context.Context, wid, reason string) error {
	next, err := store.CAS(ctx, r.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		if cur.State.Value() == "revoked" {
			return nil, store.ErrNoChange
		}
		n := *cur
		n.State, n.Gen, n.Sealed, n.TokenExp = store.Set[string]("revoked"), store.Set(cur.Gen.Value()+1), nil, nil
		return &n, nil
	})
	if err != nil {
		return err
	}
	slog.Warn("worker revoked", "wid", wid, "reason", reason, "orchestrator", r.cfg.Name)
	if next != nil {
		r.remember(next, r.cfg.Now())
	}
	return nil
}
