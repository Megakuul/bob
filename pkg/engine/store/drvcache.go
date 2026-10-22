package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine/drv"
)

// The derivation import and cache of SPEC R-NIX-007: new drvs are imported in one
// `nix-store --import` stream (export format v1, topologically ordered, no signatures) after one
// batched validity check; the drv cache <home>/cache/drv/ maps a node key to its drv path so that
// unchanged nodes are not recomputed.

// DrvKey is the drv cache key of one IR node: sha256 over the canonical IR node, the resolved
// input drv paths (sorted), the generator version and the bob-builder store path. Every field is
// length-prefixed, so no two different inputs share an encoding.
func DrvKey(canonicalNode []byte, inputDrvs []string, generator, builder string) string {
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	field([]byte("bob-drv-key-v1"))
	field(canonicalNode)
	in := append([]string(nil), inputDrvs...)
	sort.Strings(in)
	field([]byte(fmt.Sprint(len(in))))
	for _, p := range in {
		field([]byte(p))
	}
	field([]byte(generator))
	field([]byte(builder))
	return hex.EncodeToString(h.Sum(nil))
}

// DrvCache is the drv cache below <home>/cache/drv: one file <key[:2]>/<key> per entry holding
// the drv path. Entries are written by atomic rename, so concurrent bob commands need no lock;
// an entry only says what the drv path is, so the caller still checks that it is valid.
type DrvCache struct{ dir string }

// DrvCache returns the store's drv cache.
func (s *Store) DrvCache() *DrvCache { return &DrvCache{dir: filepath.Join(s.home, "cache", "drv")} }

func (c *DrvCache) file(key string) (string, error) {
	if len(key) != 64 {
		return "", fmt.Errorf("store: drv cache key %q", key)
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", fmt.Errorf("store: drv cache key %q", key)
	}
	return filepath.Join(c.dir, key[:2], key), nil
}

// Get returns the drv path recorded for key. A missing, unreadable or malformed entry is a miss.
func (c *DrvCache) Get(key string) (string, bool) {
	f, err := c.file(key)
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(f)
	if err != nil || len(b) > 4096 {
		return "", false
	}
	p := strings.TrimSuffix(string(b), "\n")
	if !isStorePath(p) || !strings.HasSuffix(p, ".drv") {
		return "", false
	}
	return p, true
}

// Put records drvPath for key.
func (c *DrvCache) Put(key, drvPath string) error {
	if !isStorePath(drvPath) || !strings.HasSuffix(drvPath, ".drv") {
		return fmt.Errorf("store: drv cache: %q is not a derivation path", drvPath)
	}
	f, err := c.file(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return accessError(err)
	}
	if err := writeIfChanged(f, []byte(drvPath+"\n"), 0o644); err != nil {
		return accessError(err)
	}
	return nil
}

// ImportDrvs makes the finalised derivations valid in the store: one batched validity check,
// then the missing ones (in topological order) in one `nix-store --import` stream. Input
// derivations outside drvs must already be valid. It returns how many drvs were imported (the
// `drv.imported` count).
func (s *Store) ImportDrvs(ctx context.Context, drvs []*drv.Derivation) (int, error) {
	items := make(map[string]ExportItem, len(drvs))
	var paths []string
	for _, d := range drvs {
		it, err := DrvExport(d)
		if err != nil {
			return 0, err
		}
		if _, dup := items[it.Path]; dup {
			continue
		}
		items[it.Path] = it
		paths = append(paths, it.Path)
	}
	sort.Strings(paths)
	invalid, err := s.Invalid(ctx, paths)
	if err != nil {
		return 0, err
	}
	if len(invalid) == 0 {
		return 0, nil
	}
	missing := make(map[string]ExportItem, len(invalid))
	for _, p := range invalid {
		it, ok := items[p]
		if !ok {
			return 0, fmt.Errorf("store: the store reported an unknown path %s", p)
		}
		missing[p] = it
	}
	ordered, err := topoOrder(missing)
	if err != nil {
		return 0, err
	}
	if err := s.Import(ctx, ordered); err != nil {
		return 0, err
	}
	return len(ordered), nil
}

// topoOrder orders items so that every item comes after the items it references; ties are
// broken by path, so the stream is deterministic.
func topoOrder(items map[string]ExportItem) ([]ExportItem, error) {
	indeg := make(map[string]int, len(items))
	users := map[string][]string{}
	for p, it := range items {
		indeg[p] += 0
		for _, r := range it.References {
			if _, ok := items[r]; ok && r != p {
				indeg[p]++
				users[r] = append(users[r], p)
			}
		}
	}
	var ready []string
	for p, n := range indeg {
		if n == 0 {
			ready = append(ready, p)
		}
	}
	sort.Strings(ready)
	out := make([]ExportItem, 0, len(items))
	for len(ready) > 0 {
		p := ready[0]
		ready = ready[1:]
		out = append(out, items[p])
		var next []string
		for _, u := range users[p] {
			if indeg[u]--; indeg[u] == 0 {
				next = append(next, u)
			}
		}
		if len(next) > 0 {
			ready = append(ready, next...)
			sort.Strings(ready)
		}
	}
	if len(out) != len(items) {
		return nil, errors.New("store: derivations reference each other in a cycle")
	}
	return out, nil
}
