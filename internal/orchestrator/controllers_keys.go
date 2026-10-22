package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"slices"
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

// KeyID is the kid of a public key: "k" + base32 of the first 10 bytes of its SHA-256 (the
// scheme of fleetsrv).
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "k" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:10]))
}

// Keys is the key controller.
//
// Write order: okey/<kid> (the record) is created before its public key is published in
// fleet/cfg.k (the index), so a published key always has its seed. A crash in between leaves
// an unpublished okey/<kid> that nothing uses; the next pass creates another key.
type Keys struct {
	Control, Fleet *store.Bucket
	Now            func() time.Time
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
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		rec := &OrchKeyRecord{Kid: KeyID(pub), Seed: priv.Seed(), Created: now, NotAfter: now.Add(KeyLifetime)}
		if err := store.Create(ctx, k.Control, rec); err != nil {
			return nil, err
		}
		add = &store.FleetKey{Kid: rec.Kid, Pub: pub, NotAfter: rec.NotAfter}
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

// Controller returns the key controller (hourly, lease ctl/key).
func (k *Keys) Controller(owner string, skew time.Duration) *Controller {
	return &Controller{ID: CtlKey, Fleet: k.Fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: k.Now, Persist: true, Pass: k.Pass}
}
