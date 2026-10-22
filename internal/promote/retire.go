package promote

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/megakuul/bob/internal/cache"
)

// NarInfoLister walks the served narinfo keys: the maintenance LIST of R-REG-153
// (sweep.NarInfos; never a hot path).
type NarInfoLister interface {
	Each(ctx context.Context, fn func(key string) error) error
}

// RetireKeys finishes every RETIRING key (R-REG-153): it re-signs each narinfo that carries
// the key's signature with the ACTIVE key and drops the retiring signature (If-Match
// rewrite), then marks the key RETIRED, which removes it from the public keys. A crash
// leaves the key RETIRING and the next pass sweeps again (the rewrite is idempotent).
func (p *Promoter) RetireKeys(ctx context.Context, list NarInfoLister) error {
	x, err := p.c.Keys.Index(ctx)
	if err != nil {
		return err
	}
	var retiring []string
	for _, e := range x.Keys.Value() {
		if e.State == KeyRetiring {
			retiring = append(retiring, e.Name)
		}
	}
	if len(retiring) == 0 {
		return nil
	}
	if list == nil {
		return errors.New("promote: keys are retiring, but no narinfo lister is configured")
	}
	active, err := p.c.Keys.Active(ctx)
	if err != nil {
		return err
	}
	n := 0
	err = list.Each(ctx, func(key string) error {
		hash, ok := strings.CutSuffix(strings.TrimPrefix(key, "nix/narinfo/"), ".narinfo")
		if !ok || !cache.ValidHash(hash) {
			return nil
		}
		changed, err := p.resign(ctx, hash, retiring, active)
		if changed {
			n++
		}
		return err
	})
	if err != nil {
		return err
	}
	for _, name := range retiring {
		if _, err := p.c.Keys.SetState(ctx, name, KeyRetired, KeyRetiring); err != nil {
			return err
		}
		p.c.Log.Info("signing key retired", "key", name, "resigned", n, "active", active.Name)
	}
	return nil
}

// resign rewrites the narinfo of hash when it carries a signature of a retiring key.
func (p *Promoter) resign(ctx context.Context, hash string, retiring []string, active *cache.SecretKey) (bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		ni, etag, err := p.served(ctx, hash)
		if err != nil || ni == nil {
			return false, err
		}
		var sigs []string
		has := false
		for _, s := range ni.Sigs {
			if slices.Contains(retiring, keyNameOf(s)) {
				has = true
				continue
			}
			sigs = append(sigs, s)
		}
		if !has {
			return false, nil
		}
		if !slices.ContainsFunc(sigs, func(s string) bool { return keyNameOf(s) == active.Name }) {
			sigs = append(sigs, active.Sign(ni.Info()))
		}
		ni.Sigs = sigs
		_, err = p.c.Objects.Put(ctx, cache.NarInfoKey(hash), ni.Format(), "text/x-nix-narinfo", cache.Cond{IfMatch: etag})
		if errors.Is(err, cache.ErrPrecondition) {
			continue
		}
		return err == nil, err
	}
	return false, fmt.Errorf("promote: narinfo %s changed too often during re-signing", hash)
}
