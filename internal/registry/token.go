package registry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Token endpoint (SPEC §6.7): tokens are
// bob-oci-v1.<b64url(json{sub, exp, acc, prj, kid})>.<b64url(HMAC-SHA256(key, "bob-oci-v1."+payload))>
// signed with an OciHmacKey (ocihmac/<kid>, control bucket) that every orchestrator shares.
const (
	tokenPrefix = "bob-oci-v1."
	// TokenTTL is expires_in of the token endpoint.
	TokenTTL = 900 * time.Second
	// keyLife is how long a key signs new tokens; it verifies for TokenTTL longer.
	keyLife     = 30 * 24 * time.Hour
	keyCacheTTL = time.Minute
	maxTokenLen = 64 << 10
	// maxTokenProjects bounds the project list a token carries (beyond it the token grants
	// only public objects of private repositories it names).
	maxTokenProjects = 1000
)

// Claims is the payload of an OCI token.
type Claims struct {
	Sub string              `json:"sub"`
	Exp int64               `json:"exp"`
	Acc map[string][]string `json:"acc"`
	// Prj are the projects whose private objects the bearer may read (RegistryService/
	// GetRepository); "*" = every project. Object visibility (§6.6.2) is decided from them.
	Prj []string `json:"prj,omitempty"`
	Kid string   `json:"kid"`
}

// CanPull reports whether the token grants pull on repo.
func (c *Claims) CanPull(repo string) bool {
	return c != nil && slices.Contains(c.Acc[repo], "pull")
}

// sees reports whether a visibility document's projects make it visible to the bearer.
func (c *Claims) sees(projects []string) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Prj {
		if p == "*" && len(projects) > 0 || slices.Contains(projects, p) {
			return true
		}
	}
	return false
}

// Keys are the shared HMAC keys of the token endpoint (ocihmacs/all + ocihmac/<kid>).
type Keys struct {
	Control *store.Bucket
	Now     func() time.Time

	mu     sync.Mutex
	keys   map[string]keyEntry
	newest string
	loaded time.Time
}

type keyEntry struct {
	key      []byte
	notAfter time.Time
}

func (k *Keys) now() time.Time {
	if k.Now == nil {
		return time.Now()
	}
	return k.Now()
}

// Ensure makes sure a key exists that signs for at least another day; a new key is created
// when none does (create-only record, then a CAS on the index; a concurrent creator's key
// wins and ours is removed).
func (k *Keys) Ensure(ctx context.Context) error {
	now := k.now()
	idx, err := store.Get(ctx, k.Control, &store.OciHmacKeys{Name: store.ID("all")})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if idx != nil && fresh(idx.Keys.Value(), now) {
		return nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	var id [6]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	kid := "k" + hex.EncodeToString(id[:])
	entry := store.OciHmacEntry{Kid: kid, Created: now, NotAfter: now.Add(keyLife)}
	if err := store.Create(ctx, k.Control, &store.OciHmacKey{Kid: store.ID(kid), Key: store.Set(raw[:]),
		Created: store.Set(now), NotAfter: store.Set(entry.NotAfter)}); err != nil {
		return err
	}
	added := false
	err = store.Change(ctx, k.Control, &store.OciHmacKeys{Name: store.ID("all")}, func(cur *store.OciHmacKeys) (*store.OciHmacKeys, error) {
		var keys []store.OciHmacEntry
		if cur != nil {
			if fresh(cur.Keys.Value(), now) {
				added = false
				return nil, store.ErrNoChange
			}
			for _, e := range cur.Keys.Value() {
				if e.NotAfter.Add(TokenTTL).After(now) {
					keys = append(keys, e)
				}
			}
		}
		added = true
		return &store.OciHmacKeys{Name: store.ID("all"), Keys: store.Set(append(keys, entry))}, nil
	})
	if err != nil || !added {
		if rec, gerr := store.Get(ctx, k.Control, &store.OciHmacKey{Kid: store.ID(kid)}); gerr == nil {
			_ = store.Delete(ctx, k.Control, rec)
		}
	}
	k.mu.Lock()
	k.loaded = time.Time{}
	k.mu.Unlock()
	return err
}

// fresh reports whether one of the keys still signs for more than a day.
func fresh(keys []store.OciHmacEntry, now time.Time) bool {
	return slices.ContainsFunc(keys, func(e store.OciHmacEntry) bool { return e.NotAfter.After(now.Add(24 * time.Hour)) })
}

// load refreshes the cached keys (at most once per keyCacheTTL unless forced).
func (k *Keys) load(ctx context.Context, force bool) error {
	k.mu.Lock()
	age := k.now().Sub(k.loaded)
	// a forced reload (unknown kid) happens at most every 5 s: forged tokens cost no GETs
	ok := k.keys != nil && (age < keyCacheTTL && !force || force && age < 5*time.Second)
	k.mu.Unlock()
	if ok {
		return nil
	}
	idx, err := store.Get(ctx, k.Control, &store.OciHmacKeys{Name: store.ID("all")})
	if errors.Is(err, store.ErrNotFound) {
		if err := k.Ensure(ctx); err != nil {
			return err
		}
		idx, err = store.Get(ctx, k.Control, &store.OciHmacKeys{Name: store.ID("all")})
	}
	if err != nil {
		return err
	}
	keys := map[string]keyEntry{}
	newest := ""
	var newestAt time.Time
	for _, e := range idx.Keys.Value() {
		rec, err := store.Get(ctx, k.Control, &store.OciHmacKey{Kid: store.ID(e.Kid)})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if len(rec.Key.Value()) != 32 {
			continue
		}
		keys[e.Kid] = keyEntry{key: rec.Key.Value(), notAfter: e.NotAfter}
		if e.Created.After(newestAt) || newest == "" {
			newest, newestAt = e.Kid, e.Created
		}
	}
	if newest == "" {
		return errors.New("registry: no OCI token key")
	}
	k.mu.Lock()
	k.keys, k.newest, k.loaded = keys, newest, k.now()
	k.mu.Unlock()
	return nil
}

func mac(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(tokenPrefix + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue signs c (Kid and Exp are set here).
func (k *Keys) Issue(ctx context.Context, c Claims) (string, error) {
	if err := k.load(ctx, false); err != nil {
		return "", err
	}
	k.mu.Lock()
	kid, e := k.newest, k.keys[k.newest]
	k.mu.Unlock()
	if !e.notAfter.After(k.now()) {
		if err := k.Ensure(ctx); err != nil {
			return "", err
		}
		if err := k.load(ctx, true); err != nil {
			return "", err
		}
		k.mu.Lock()
		kid, e = k.newest, k.keys[k.newest]
		k.mu.Unlock()
	}
	c.Kid = kid
	c.Exp = k.now().Add(TokenTTL).Unix()
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	return tokenPrefix + payload + "." + mac(e.key, payload), nil
}

// Verify checks a token's signature and expiry; nil claims and an error for anything else.
func (k *Keys) Verify(ctx context.Context, tok string) (*Claims, error) {
	rest, ok := strings.CutPrefix(tok, tokenPrefix)
	if !ok || len(tok) > maxTokenLen {
		return nil, errors.New("not a bob OCI token")
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, errors.New("malformed token")
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("malformed token")
	}
	var c Claims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, errors.New("malformed token")
	}
	if err := k.load(ctx, false); err != nil {
		return nil, err
	}
	k.mu.Lock()
	e, known := k.keys[c.Kid]
	k.mu.Unlock()
	if !known {
		if err := k.load(ctx, true); err != nil {
			return nil, err
		}
		k.mu.Lock()
		e, known = k.keys[c.Kid]
		k.mu.Unlock()
	}
	if !known || !hmac.Equal([]byte(mac(e.key, payload)), []byte(sig)) {
		return nil, errors.New("invalid token signature")
	}
	if now := k.now(); now.Unix() >= c.Exp || now.After(e.notAfter.Add(TokenTTL)) {
		return nil, fmt.Errorf("token expired")
	}
	return &c, nil
}
