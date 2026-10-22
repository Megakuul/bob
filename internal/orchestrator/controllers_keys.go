package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Orchestrator signing keys (§05.9.2): seeds in control okey/<kid>, public keys in fleet/cfg.k.
// The start-up side (create the first key, pick the signing key) is internal/fleetsrv's
// EnsureKeys; this is the hourly key controller of §05.11 (rotation and retirement).
const (
	KeyLifetime     = 90 * 24 * time.Hour
	KeyRotateBefore = 7 * 24 * time.Hour // the next key is created this long before notAfter
	// keyRetire is how long an expired key stays in fleet/cfg.k (tokens it signed live 1 h).
	keyRetire = 2 * time.Hour
)

// OrchKeyRecord is `okey/<kid>`: Ed25519 seed, created, notAfter (Create). Same encoding as
// fleetsrv.OrchKey.
type OrchKeyRecord struct {
	Kid      string    `pk:"okey" cbor:"-"`
	ETag     string    `etag:"true" cbor:"-"`
	Seed     []byte    `cbor:"sd,omitempty"`
	Created  time.Time `cbor:"cr,omitzero"`
	NotAfter time.Time `cbor:"na,omitzero"`
}

// rotatedKid is the kid of the n-th rotated key: "k" + 6 digits. The key controller derives
// the next kid from the published ones, so a pass that crashed between okey/<kid> and
// fleet/cfg.k meets its own record again (Create → exists) and publishes it: no orphan seed.
func rotatedKid(n int) string { return fmt.Sprintf("k%06d", n) }

// kidSeq is n of a rotated kid; 0 for any other kid (fleetsrv's start-up keys).
func kidSeq(kid string) int {
	d, ok := strings.CutPrefix(kid, "k")
	if !ok || len(d) != 6 {
		return 0
	}
	n, err := strconv.Atoi(d)
	if err != nil {
		return 0
	}
	return n
}

// Keys is the key controller.
//
// Write order: okey/<kid> (the seed) is created before its public key is published in
// fleet/cfg.k, so a published key always has its seed (a published key without one could
// never sign). A crash in between is finished by the next pass (same kid, see rotatedKid).
type Keys struct {
	Control, Fleet *store.Bucket
	Now            func() time.Time
	Log            *slog.Logger
}

// Pass is the key controller pass (hourly): when no published key is valid beyond now +
// KeyRotateBefore, create and publish the next one; drop keys expired for longer than
// keyRetire from fleet/cfg.k.
func (k *Keys) Pass(ctx context.Context, r *Run) error {
	_, err := k.Rotate(ctx, r.Now)
	return err
}

// Rotate is Pass at time now; it returns the published keys.
func (k *Keys) Rotate(ctx context.Context, now time.Time) ([]store.FleetKey, error) {
	if k.Control == nil || k.Fleet == nil {
		return nil, errors.New("orchestrator: keys need the control and fleet buckets")
	}
	fc, err := store.Get(ctx, k.Fleet, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	var keys []store.FleetKey
	if fc != nil {
		keys = fc.Keys
	}
	fresh := func(fk store.FleetKey) bool {
		return len(fk.Pub) == ed25519.PublicKeySize && fk.NotAfter.After(now.Add(KeyRotateBefore))
	}
	retire := func(fk store.FleetKey) bool { return fk.NotAfter.Add(keyRetire).Before(now) }
	var add *store.FleetKey
	if !slices.ContainsFunc(keys, fresh) {
		top := 0
		for _, fk := range keys {
			top = max(top, kidSeq(fk.Kid))
		}
		rec, err := k.next(ctx, top, now)
		if err != nil {
			return nil, err
		}
		add = &store.FleetKey{Kid: rec.Kid, Pub: ed25519.NewKeyFromSeed(rec.Seed).Public().(ed25519.PublicKey), NotAfter: rec.NotAfter}
	}
	if add == nil && !slices.ContainsFunc(keys, retire) {
		return keys, nil
	}
	got, err := store.CAS(ctx, k.Fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		next := store.FleetConfigKey()
		if cur != nil {
			*next = *cur
		}
		had := slices.ContainsFunc(next.Keys, fresh) // another orchestrator may have rotated meanwhile
		ks := slices.DeleteFunc(slices.Clone(next.Keys), retire)
		if add != nil && !had {
			ks = append(ks, *add)
		}
		if len(ks) == len(next.Keys) && (add == nil || had) {
			return nil, store.ErrNoChange
		}
		next.Keys = ks
		next.Gen++
		return next, nil
	})
	if err != nil || got == nil {
		return nil, err
	}
	return got.Keys, nil
}

// next creates okey/<kid> for the first rotated kid after top, or adopts the record an
// earlier (crashed) pass or another orchestrator created under that kid. A record that is not
// fresh (keys retired from fleet/cfg.k while the fleet was down) is skipped: kids only grow.
func (k *Keys) next(ctx context.Context, top int, now time.Time) (*OrchKeyRecord, error) {
	for n := top + 1; n <= top+1000; n++ {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		rec := &OrchKeyRecord{Kid: rotatedKid(n), Seed: seed, Created: now, NotAfter: now.Add(KeyLifetime)}
		err := store.Create(ctx, k.Control, rec)
		if errors.Is(err, store.ErrExists) {
			rec, err = store.Get(ctx, k.Control, &OrchKeyRecord{Kid: rotatedKid(n)})
		}
		if err != nil {
			return nil, err
		}
		if len(rec.Seed) == ed25519.SeedSize && rec.NotAfter.After(now.Add(KeyRotateBefore)) {
			return rec, nil
		}
	}
	return nil, errors.New("orchestrator: no usable key id within 1000 of the published ones")
}

// Controller returns the key controller (hourly, lease ctl/key).
func (k *Keys) Controller(owner string, skew time.Duration) *Controller {
	return &Controller{ID: CtlKey, Fleet: k.Fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: k.Now, Persist: true, Pass: k.Pass, Log: k.Log}
}
