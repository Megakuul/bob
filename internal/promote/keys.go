package promote

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/store"
)

// Signing key states (SPEC §05.4.5, settings.v1.KeyState).
const (
	KeyActive     = "ACTIVE"
	KeyVerifyOnly = "VERIFY_ONLY"
	KeyRetiring   = "RETIRING"
	KeyRetired    = "RETIRED"
)

// SigningKeyRecord is `skey/<keyname>` in the control bucket (workers cannot read it,
// R-REG-002): the Ed25519 seed (create-only, never changes) and its creation time.
type SigningKeyRecord struct {
	Name    store.KeyField             `pk:"skey" cbor:"-"`
	ETag    store.ETagField            `etag:"true" cbor:"-"`
	Seed    store.DataField[[]byte]    `cbor:"sd,omitempty"`
	Created store.DataField[time.Time] `cbor:"cr,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *SigningKeyRecord) Draft() *SigningKeyRecord {
	return &SigningKeyRecord{
		Name:    r.Name,
		ETag:    r.ETag,
		Seed:    store.Carry(r.Seed),
		Created: store.Carry(r.Created),
	}
}

// KeyEntry is one key of the index.
type KeyEntry struct {
	Name    string    `cbor:"n,omitempty"`
	N       int       `cbor:"i,omitempty"`
	Public  string    `cbor:"pub,omitempty"` // "<name>:<base64>"
	State   string    `cbor:"st,omitempty"`
	Created time.Time `cbor:"cr,omitzero"`
}

// SigningKeyIndex is `skeys/all`: every key with its state. The states live here, in one
// CAS-written document, so that a rotation (new ACTIVE, old VERIFY_ONLY) is one atomic write.
type SigningKeyIndex struct {
	Name store.KeyField              `pk:"skeys" cbor:"-"` // "all"
	ETag store.ETagField             `etag:"true" cbor:"-"`
	Keys store.DataField[[]KeyEntry] `cbor:"k,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *SigningKeyIndex) Draft() *SigningKeyIndex {
	return &SigningKeyIndex{
		Name: r.Name,
		ETag: r.ETag,
		Keys: store.Carry(r.Keys),
	}
}

func indexKey() *SigningKeyIndex { return &SigningKeyIndex{Name: store.ID("all")} }

// Entries returns the key entries of a fetched index (none for the empty index of a server
// without keys, which Index returns as a key value).
func (x *SigningKeyIndex) Entries() []KeyEntry {
	if x == nil || x.Keys == nil {
		return nil
	}
	return x.Keys.Value()
}

// Active returns the ACTIVE entry (nil when none).
func (x *SigningKeyIndex) Active() *KeyEntry {
	keys := x.Entries()
	for i := range keys {
		if keys[i].State == KeyActive {
			return &keys[i]
		}
	}
	return nil
}

func (x *SigningKeyIndex) find(name string) int {
	return slices.IndexFunc(x.Entries(), func(e KeyEntry) bool { return e.Name == name })
}

// Errors of key management callers act on.
var (
	ErrKeyNotFound = errors.New("signing key not found")
	ErrKeyActive   = errors.New("the active signing key cannot be retired; create a new key first")
	ErrNoKey       = errors.New("no active signing key")
)

// Keys manages the cache signing keys (R-REG-002, R-REG-153).
type Keys struct {
	Control *store.Bucket
	Prefix  string // key names are <Prefix>-<n> (server_name, default "bob")
	Now     func() time.Time

	idx   store.Cache[SigningKeyIndex]
	mu    sync.Mutex
	seeds map[string]*cache.SecretKey // seeds never change for a name
}

func (k *Keys) now() time.Time {
	if k.Now != nil {
		return k.Now().UTC()
	}
	return time.Now().UTC()
}

func (k *Keys) prefix() string {
	if k.Prefix == "" {
		return "bob"
	}
	return k.Prefix
}

// Index reads skeys/all (conditional GET). Absent is an empty index.
func (k *Keys) Index(ctx context.Context) (*SigningKeyIndex, error) {
	x, _, err := store.GetCached(ctx, k.Control, &k.idx, indexKey())
	if errors.Is(err, store.ErrNotFound) {
		return &SigningKeyIndex{Name: store.ID("all")}, nil
	}
	return x, err
}

// Ensure creates the first key when the server has no ACTIVE key (start-up). Concurrent
// orchestrators agree on the name <prefix>-<n>, so they create one key, not two.
func (k *Keys) Ensure(ctx context.Context) error {
	x, err := k.Index(ctx)
	if err != nil {
		return err
	}
	if x.Active() != nil {
		return nil
	}
	_, err = k.create(ctx, func(cur *SigningKeyIndex) bool { return cur.Active() == nil })
	return err
}

// Create adds a new ACTIVE key; the previous ACTIVE key becomes VERIFY_ONLY.
func (k *Keys) Create(ctx context.Context) (KeyEntry, error) {
	return k.create(ctx, func(*SigningKeyIndex) bool { return true })
}

func nextN(x *SigningKeyIndex) int {
	n := 0
	for _, e := range x.Entries() {
		n = max(n, e.N)
	}
	return n + 1
}

// create writes the seed record skey/<name> first (create-only) and then the index, so an
// indexed key always has its seed. A crash in between leaves a seed record that the next
// create of the same name adopts.
func (k *Keys) create(ctx context.Context, want func(*SigningKeyIndex) bool) (KeyEntry, error) {
	for attempt := 0; attempt < 8; attempt++ {
		x, err := k.Index(ctx)
		if err != nil {
			return KeyEntry{}, err
		}
		if !want(x) {
			if a := x.Active(); a != nil {
				return *a, nil
			}
		}
		n := nextN(x)
		name := k.prefix() + "-" + strconv.Itoa(n)
		sk, err := k.seedRecord(ctx, name)
		if err != nil {
			return KeyEntry{}, err
		}
		entry := KeyEntry{Name: name, N: n, Public: sk.Public(), State: KeyActive, Created: k.now()}
		var out KeyEntry
		_, err = store.CAS(ctx, k.Control, indexKey(), func(cur *SigningKeyIndex) (*SigningKeyIndex, error) {
			if i := cur.find(name); i >= 0 { // a concurrent create of the same name won
				out = cur.Entries()[i]
				return nil, store.ErrNoChange
			}
			if nextN(cur) != n || !want(cur) {
				return nil, errRetryCreate
			}
			keys := slices.Clone(cur.Entries())
			for i := range keys {
				if keys[i].State == KeyActive {
					keys[i].State = KeyVerifyOnly
				}
			}
			out = entry
			return &SigningKeyIndex{Name: store.ID("all"), Keys: store.Set(append(keys, entry))}, nil
		})
		if errors.Is(err, errRetryCreate) {
			continue
		}
		if err != nil {
			return KeyEntry{}, err
		}
		return out, nil
	}
	return KeyEntry{}, errors.New("promote: signing key index changed too often; try again")
}

var errRetryCreate = errors.New("index changed")

// seedRecord creates skey/<name> with a fresh seed, or adopts the one already there.
func (k *Keys) seedRecord(ctx context.Context, name string) (*cache.SecretKey, error) {
	sk, err := cache.GenerateKey(name)
	if err != nil {
		return nil, err
	}
	rec := &SigningKeyRecord{Name: store.ID(name), Seed: store.Set(sk.Seed()), Created: store.Set(k.now())}
	switch err := store.Create(ctx, k.Control, rec); {
	case err == nil:
		k.remember(sk)
		return sk, nil
	case errors.Is(err, store.ErrExists):
		return k.Secret(ctx, name)
	default:
		return nil, err
	}
}

func (k *Keys) remember(sk *cache.SecretKey) {
	k.mu.Lock()
	if k.seeds == nil {
		k.seeds = map[string]*cache.SecretKey{}
	}
	k.seeds[sk.Name] = sk
	k.mu.Unlock()
}

// Secret returns the secret key called name (from skey/<name>; kept once read).
func (k *Keys) Secret(ctx context.Context, name string) (*cache.SecretKey, error) {
	k.mu.Lock()
	sk := k.seeds[name]
	k.mu.Unlock()
	if sk != nil {
		return sk, nil
	}
	rec, err := store.Get(ctx, k.Control, &SigningKeyRecord{Name: store.ID(name)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if sk, err = cache.KeyFromSeed(name, rec.Seed.Value()); err != nil {
		return nil, err
	}
	k.remember(sk)
	return sk, nil
}

// Active returns the ACTIVE secret key.
func (k *Keys) Active(ctx context.Context) (*cache.SecretKey, error) {
	x, err := k.Index(ctx)
	if err != nil {
		return nil, err
	}
	a := x.Active()
	if a == nil {
		return nil, ErrNoKey
	}
	return k.Secret(ctx, a.Name)
}

// PublicKeys are the keys clients trust: every key that is not RETIRED, ACTIVE first.
func (k *Keys) PublicKeys(ctx context.Context) ([]string, error) {
	x, err := k.Index(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	if a := x.Active(); a != nil {
		out = append(out, a.Public)
	}
	for _, e := range x.Entries() {
		if e.State != KeyActive && e.State != KeyRetired {
			out = append(out, e.Public)
		}
	}
	return out, nil
}

// SetState moves key name from one of the states in from to state to (CAS on the index).
func (k *Keys) SetState(ctx context.Context, name, to string, from ...string) (KeyEntry, error) {
	var out KeyEntry
	_, err := store.CAS(ctx, k.Control, indexKey(), func(cur *SigningKeyIndex) (*SigningKeyIndex, error) {
		if cur == nil {
			return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, name)
		}
		i := cur.find(name)
		if i < 0 {
			return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, name)
		}
		out = cur.Keys.Value()[i]
		if out.State == to {
			return nil, store.ErrNoChange
		}
		if out.State == KeyActive && to == KeyRetiring {
			return nil, ErrKeyActive
		}
		if !slices.Contains(from, out.State) {
			return nil, store.ErrNoChange
		}
		keys := slices.Clone(cur.Keys.Value())
		keys[i].State = to
		out = keys[i]
		return &SigningKeyIndex{Name: store.ID("all"), Keys: store.Set(keys)}, nil
	})
	if err != nil {
		return KeyEntry{}, err
	}
	return out, nil
}

// keyNameOf returns the key name of a Sig value "<name>:<base64>".
func keyNameOf(sig string) string {
	n, _, _ := strings.Cut(sig, ":")
	return n
}
