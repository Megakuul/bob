package fleetsrv

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// OrchKey is `okey/<kid>` in the control bucket: the seed of an orchestrator signing key (SPEC
// §05.4.2, §05.9.2). Its public half is published in fleet/cfg.k. Workers cannot read it.
type OrchKey struct {
	Kid      string    `pk:"okey" cbor:"-"`
	ETag     string    `etag:"true" cbor:"-"`
	Seed     []byte    `cbor:"sd,omitempty"`
	Created  time.Time `cbor:"cr,omitzero"`
	NotAfter time.Time `cbor:"na,omitzero"`
}

// KeyLifetime is how long a new orchestrator key is valid. The key controller (§05.11, 1 h)
// creates the next one 7 days before notAfter.
const KeyLifetime = 90 * 24 * time.Hour

// Keys are the orchestrator signing keys this process uses.
type Keys struct {
	Kid    string
	Key    ed25519.PrivateKey
	Verify map[string]ed25519.PublicKey // every published key by kid (tokens of rotated keys)
	Pubs   []ed25519.PublicKey          // the same keys, for JobSpec verification
}

// kidOf derives the key id from the public key (stable and collision-free in practice).
func kidOf(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "k" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:10]))
}

// EnsureKeys returns the fleet's current signing key, creating and publishing one when
// fleet/cfg has no valid key yet, and publishes params in fleet/cfg.pr when it holds none.
// Orchestrators that start together may each publish a key; all of them then pick the same
// one (the valid key with the smallest kid), so every orchestrator signs with one key and the
// others stay verifiable.
func EnsureKeys(ctx context.Context, control, fleet *store.Bucket, now time.Time, params store.FleetParams) (*Keys, error) {
	cfg, err := store.Get(ctx, fleet, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("fleet/cfg: %w", err)
	}
	if cfg == nil || !hasValid(cfg.Keys, now) || cfg.Params.PollInterval == 0 {
		var fresh *OrchKey
		if cfg == nil || !hasValid(cfg.Keys, now) {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return nil, err
			}
			fresh = &OrchKey{Kid: kidOf(pub), Seed: priv.Seed(), Created: now.UTC(), NotAfter: now.Add(KeyLifetime).UTC()}
			// The seed first: a published key whose seed is missing could never sign.
			if err := store.Create(ctx, control, fresh); err != nil {
				return nil, fmt.Errorf("okey/%s: %w", fresh.Kid, err)
			}
		}
		cfg, err = store.CAS(ctx, fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
			next := store.FleetConfig{Name: "cfg"}
			if cur != nil {
				next = *cur
				next.Keys = slices.Clone(cur.Keys)
			}
			changed := false
			if fresh != nil && !hasValid(next.Keys, now) {
				pub := ed25519.NewKeyFromSeed(fresh.Seed).Public().(ed25519.PublicKey)
				next.Keys = append(next.Keys, store.FleetKey{Kid: fresh.Kid, Pub: pub, NotAfter: fresh.NotAfter})
				changed = true
			}
			if next.Params.PollInterval == 0 {
				next.Params = params
				changed = true
			}
			if !changed {
				return nil, store.ErrNoChange
			}
			next.Gen++
			return &next, nil
		})
		if err != nil {
			return nil, fmt.Errorf("fleet/cfg: %w", err)
		}
	}
	return keysOf(ctx, control, cfg, now)
}

func hasValid(ks []store.FleetKey, now time.Time) bool {
	for _, k := range ks {
		if len(k.Pub) == ed25519.PublicKeySize && k.NotAfter.After(now) {
			return true
		}
	}
	return false
}

// keysOf picks the signing key of cfg (the valid key with the smallest kid) and loads its seed.
func keysOf(ctx context.Context, control *store.Bucket, cfg *store.FleetConfig, now time.Time) (*Keys, error) {
	ks := &Keys{Verify: map[string]ed25519.PublicKey{}}
	var pick *store.FleetKey
	for i, k := range cfg.Keys {
		if len(k.Pub) != ed25519.PublicKeySize {
			continue
		}
		ks.Verify[k.Kid] = ed25519.PublicKey(k.Pub)
		ks.Pubs = append(ks.Pubs, ed25519.PublicKey(k.Pub))
		if k.NotAfter.After(now) && (pick == nil || k.Kid < pick.Kid) {
			pick = &cfg.Keys[i]
		}
	}
	if pick == nil {
		return nil, errors.New("fleet/cfg holds no valid orchestrator key")
	}
	rec, err := store.Get(ctx, control, &OrchKey{Kid: pick.Kid})
	if err != nil {
		return nil, fmt.Errorf("okey/%s: %w", pick.Kid, err)
	}
	if len(rec.Seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("okey/%s: invalid seed", pick.Kid)
	}
	key := ed25519.NewKeyFromSeed(rec.Seed)
	if !bytes.Equal(key.Public().(ed25519.PublicKey), pick.Pub) {
		return nil, fmt.Errorf("okey/%s does not match the published key", pick.Kid)
	}
	ks.Kid, ks.Key = pick.Kid, key
	return ks, nil
}
