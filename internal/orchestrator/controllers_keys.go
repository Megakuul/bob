package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Orchestrator signing keys (§05.9.2): seeds in control okey/<kid>, public keys in fleet/cfg.k.
const (
	KeyLifetime     = 90 * 24 * time.Hour
	KeyRotateBefore = 7 * 24 * time.Hour // the key controller creates the next key this long before notAfter
	// keyRetire is how long an expired key stays in fleet/cfg.k (tokens it signed live 1 h).
	keyRetire = 2 * time.Hour
)

// OrchKeyRecord is `okey/<kid>`: Ed25519 seed, created, notAfter (key controller, Create).
type OrchKeyRecord struct {
	Kid      string    `pk:"okey" cbor:"-"`
	ETag     string    `etag:"true" cbor:"-"`
	Seed     []byte    `cbor:"sd,omitempty"`
	Created  time.Time `cbor:"cr,omitzero"`
	NotAfter time.Time `cbor:"na,omitzero"`
}

// Keys manages the orchestrator signing keys. Kids are "k" + a 6-digit sequence, so two
// orchestrators that create the next key at the same moment meet on one okey/<kid> Create.
//
// Write order: okey/<kid> (the record) is created before its public key is published in
// fleet/cfg.k (the index): a published key always has its seed. A crash in between leaves an
// unpublished okey/<kid>, which the next pass publishes (same kid).
type Keys struct {
	Control, Fleet *store.Bucket
	Now            func() time.Time

	mu    sync.Mutex
	seeds map[string]ed25519.PrivateKey
	cfg   store.Cache[store.FleetConfig]
}

func (k *Keys) now() time.Time {
	if k.Now != nil {
		return k.Now().UTC()
	}
	return time.Now().UTC()
}

func kidOf(n int) string { return fmt.Sprintf("k%06d", n) }

func kidSeq(kid string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(kid, "k"))
	if err != nil || !strings.HasPrefix(kid, "k") {
		return 0
	}
	return n
}

// Pass is the key controller pass (hourly): when no published key is valid beyond now +
// KeyRotateBefore, create and publish the next one; drop keys expired for longer than
// keyRetire from fleet/cfg.k.
func (k *Keys) Pass(ctx context.Context, r *Run) error {
	_, err := k.ensure(ctx, r.Now)
	return err
}

// Ensure makes sure a signing key exists (an orchestrator calls it at start, before it signs
// anything) and returns the published keys.
func (k *Keys) Ensure(ctx context.Context) ([]store.FleetKey, error) {
	return k.ensure(ctx, k.now())
}

func (k *Keys) ensure(ctx context.Context, now time.Time) ([]store.FleetKey, error) {
	if k.Control == nil || k.Fleet == nil {
		return nil, errors.New("orchestrator: keys need the control and fleet buckets")
	}
	fc, _, err := store.GetCached(ctx, k.Fleet, &k.cfg, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	var keys []store.FleetKey
	if fc != nil {
		keys = slices.Clone(fc.Keys)
	}
	top := 0
	fresh := false
	for _, fk := range keys {
		top = max(top, kidSeq(fk.Kid))
		if fk.NotAfter.After(now.Add(KeyRotateBefore)) {
			fresh = true
		}
	}
	var add *store.FleetKey
	if !fresh {
		// An existing okey/<kid> that is not fresh (keys retired from fleet/cfg.k while the
		// fleet was down) is skipped: kids only grow.
		var rec *OrchKeyRecord
		for n := top + 1; rec == nil; n++ {
			if n > top+1000 {
				return nil, errors.New("orchestrator: no free key id within 1000 of the published ones")
			}
			got, err := k.create(ctx, kidOf(n), now)
			if err != nil {
				return nil, err
			}
			if got.NotAfter.After(now.Add(KeyRotateBefore)) {
				rec = got
			}
		}
		add = &store.FleetKey{Kid: rec.Kid, Pub: ed25519.NewKeyFromSeed(rec.Seed).Public().(ed25519.PublicKey), NotAfter: rec.NotAfter}
	}
	retire := func(fk store.FleetKey) bool { return fk.NotAfter.Add(keyRetire).Before(now) }
	if add == nil && !slices.ContainsFunc(keys, retire) {
		return keys, nil
	}
	got, err := store.CAS(ctx, k.Fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		next := store.FleetConfigKey()
		if cur != nil {
			*next = *cur
		}
		ks := slices.DeleteFunc(slices.Clone(next.Keys), retire)
		if add != nil && !slices.ContainsFunc(ks, func(fk store.FleetKey) bool { return fk.Kid == add.Kid }) {
			ks = append(ks, *add)
		}
		if len(ks) == len(next.Keys) && add == nil {
			return nil, store.ErrNoChange
		}
		next.Keys = ks
		next.Gen++
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, nil
	}
	return got.Keys, nil
}

// create creates okey/<kid> or, when another orchestrator won the Create, reads its key.
func (k *Keys) create(ctx context.Context, kid string, now time.Time) (*OrchKeyRecord, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	rec := &OrchKeyRecord{Kid: kid, Seed: seed, Created: now, NotAfter: now.Add(KeyLifetime)}
	err := store.Create(ctx, k.Control, rec)
	if errors.Is(err, store.ErrExists) {
		rec, err = store.Get(ctx, k.Control, &OrchKeyRecord{Kid: kid})
	}
	if err != nil {
		return nil, err
	}
	if len(rec.Seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("orchestrator: okey/%s has no valid seed", kid)
	}
	return rec, nil
}

// Signing returns the key this orchestrator signs with: among the published keys whose seed
// it can read, the one expiring first that still has more than half of KeyRotateBefore left
// (a new key is used only after workers had days to learn it), else the newest.
func (k *Keys) Signing(ctx context.Context) (kid string, key ed25519.PrivateKey, err error) {
	keys, err := k.Ensure(ctx)
	if err != nil {
		return "", nil, err
	}
	now := k.now()
	cands := slices.DeleteFunc(slices.Clone(keys), func(fk store.FleetKey) bool { return !fk.NotAfter.After(now) })
	if len(cands) == 0 {
		return "", nil, errors.New("orchestrator: no valid signing key in fleet/cfg.k")
	}
	slices.SortFunc(cands, func(a, b store.FleetKey) int { return a.NotAfter.Compare(b.NotAfter) })
	pick := cands[len(cands)-1]
	for _, fk := range cands {
		if fk.NotAfter.After(now.Add(KeyRotateBefore / 2)) {
			pick = fk
			break
		}
	}
	priv, err := k.seed(ctx, pick.Kid)
	return pick.Kid, priv, err
}

// seed reads okey/<kid> once per process.
func (k *Keys) seed(ctx context.Context, kid string) (ed25519.PrivateKey, error) {
	k.mu.Lock()
	priv, ok := k.seeds[kid]
	k.mu.Unlock()
	if ok {
		return priv, nil
	}
	rec, err := store.Get(ctx, k.Control, &OrchKeyRecord{Kid: kid})
	if err != nil {
		return nil, err
	}
	if len(rec.Seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("orchestrator: okey/%s has no valid seed", kid)
	}
	priv = ed25519.NewKeyFromSeed(rec.Seed)
	k.mu.Lock()
	if k.seeds == nil {
		k.seeds = map[string]ed25519.PrivateKey{}
	}
	k.seeds[kid] = priv
	k.mu.Unlock()
	return priv, nil
}

// Verify returns the published public keys by kid (token and spec verification).
func (k *Keys) Verify(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	fc, _, err := store.GetCached(ctx, k.Fleet, &k.cfg, store.FleetConfigKey())
	if errors.Is(err, store.ErrNotFound) {
		return map[string]ed25519.PublicKey{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]ed25519.PublicKey, len(fc.Keys))
	for _, fk := range fc.Keys {
		if len(fk.Pub) == ed25519.PublicKeySize {
			out[fk.Kid] = ed25519.PublicKey(fk.Pub)
		}
	}
	return out, nil
}

// Controller returns the key controller (hourly, lease ctl/key).
func (k *Keys) Controller(owner string, skew time.Duration) *Controller {
	return &Controller{ID: CtlKey, Fleet: k.Fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: k.Now, Persist: true, Pass: k.Pass}
}
