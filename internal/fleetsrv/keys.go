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

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

// Keys are the orchestrator signing keys this process uses.
type Keys struct {
	Kid    string
	Key    ed25519.PrivateKey
	Verify map[string]ed25519.PublicKey // every published key by kid (tokens of rotated keys)
	Pubs   []ed25519.PublicKey          // the same keys, for JobSpec verification
}

// EnsureKeys (start-up; the hourly rotation is orchestrator.Keys, the key controller) returns the fleet's current signing key, creating and publishing one when
// fleet/cfg has no valid key yet, and publishes params in fleet/cfg.pr when it holds none.
// Orchestrators that start together may each publish a key; all of them then pick the same
// one (keysOf), so every orchestrator signs with one key and the
// others stay verifiable.
func EnsureKeys(ctx context.Context, control, fleet *store.Bucket, now time.Time, params store.FleetParams) (*Keys, error) {
	cfg, err := store.Get(ctx, fleet, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("fleet/cfg: %w", err)
	}
	if cfg == nil || !hasValid(cfg.Keys.Value(), now) || cfg.Params.Value().PollInterval == 0 {
		var fresh *orchestrator.OrchKeyRecord
		if cfg == nil || !hasValid(cfg.Keys.Value(), now) {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return nil, err
			}
			fresh = &orchestrator.OrchKeyRecord{Kid: store.ID(kidOf(pub)), Seed: store.Set(priv.Seed()), Created: store.Set(now.UTC()), NotAfter: store.Set(now.Add(orchestrator.KeyLifetime).UTC())}
			// The seed first: a published key whose seed is missing could never sign.
			if err := store.Create(ctx, control, fresh); err != nil {
				return nil, fmt.Errorf("okey/%s: %w", fresh.Kid.Value(), err)
			}
		}
		cfg, err = store.CAS(ctx, fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
			next := store.FleetConfig{Name: store.ID("cfg")}
			if cur != nil {
				next = *cur
				next.Keys = store.Set(slices.Clone(cur.Keys.Value()))
			}
			changed := false
			if fresh != nil && !hasValid(next.Keys.Value(), now) {
				pub := ed25519.NewKeyFromSeed(fresh.Seed.Value()).Public().(ed25519.PublicKey)
				next.Keys = store.Set(append(next.Keys.Value(), store.FleetKey{Kid: fresh.Kid.Value(), Pub: pub, NotAfter: fresh.NotAfter.Value()}))
				changed = true
			}
			if next.Params.Value().PollInterval == 0 {
				next.Params = store.Set(params)
				changed = true
			}
			if !changed {
				return nil, store.ErrNoChange
			}
			next.Gen = store.Set(next.Gen.Value() + 1)
			return &next, nil
		})
		if err != nil {
			return nil, fmt.Errorf("fleet/cfg: %w", err)
		}
	}
	return keysOf(ctx, control, cfg, now)
}

// kidOf is the kid of a start-up key: "k" + base32 of the first 10 bytes of the public key's
// SHA-256 (the key controller's rotated keys are "k" + 6 digits).
func kidOf(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "k" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:10]))
}

func hasValid(ks []store.FleetKey, now time.Time) bool {
	for _, k := range ks {
		if len(k.Pub) == ed25519.PublicKeySize && k.NotAfter.After(now) {
			return true
		}
	}
	return false
}

// signMargin is how long a key must still be valid to be picked for signing: a key the key
// controller just published (7 d before the old one ends) is used only once the old one has
// 3.5 d left, long after every worker's 30 s config refresh has seen it.
const signMargin = 84 * time.Hour

// keysOf picks the signing key of cfg and loads its seed: the oldest valid key (earliest
// notAfter) with more than signMargin left, else the valid key that lives longest; ties by
// kid, so orchestrators that read the same fleet/cfg pick the same key.
func keysOf(ctx context.Context, control *store.Bucket, cfg *store.FleetConfig, now time.Time) (*Keys, error) {
	ks := &Keys{Verify: map[string]ed25519.PublicKey{}}
	var pick, longest *store.FleetKey
	before := func(a, b *store.FleetKey) bool {
		return a.NotAfter.Before(b.NotAfter) || a.NotAfter.Equal(b.NotAfter) && a.Kid < b.Kid
	}
	for i, k := range cfg.Keys.Value() {
		if len(k.Pub) != ed25519.PublicKeySize {
			continue
		}
		ks.Verify[k.Kid] = ed25519.PublicKey(k.Pub)
		ks.Pubs = append(ks.Pubs, ed25519.PublicKey(k.Pub))
		k := &cfg.Keys.Value()[i]
		if !k.NotAfter.After(now) {
			continue
		}
		if k.NotAfter.After(now.Add(signMargin)) && (pick == nil || before(k, pick)) {
			pick = k
		}
		if longest == nil || before(longest, k) {
			longest = k
		}
	}
	if pick == nil {
		pick = longest
	}
	if pick == nil {
		return nil, errors.New("fleet/cfg holds no valid orchestrator key")
	}
	rec, err := store.Get(ctx, control, &orchestrator.OrchKeyRecord{Kid: store.ID(pick.Kid)})
	if err != nil {
		return nil, fmt.Errorf("okey/%s: %w", pick.Kid, err)
	}
	if len(rec.Seed.Value()) != ed25519.SeedSize {
		return nil, fmt.Errorf("okey/%s: invalid seed", pick.Kid)
	}
	key := ed25519.NewKeyFromSeed(rec.Seed.Value())
	if !bytes.Equal(key.Public().(ed25519.PublicKey), pick.Pub) {
		return nil, fmt.Errorf("okey/%s does not match the published key", pick.Kid)
	}
	ks.Kid, ks.Key = pick.Kid, key
	return ks, nil
}
