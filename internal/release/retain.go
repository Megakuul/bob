package release

import (
	"context"
	"errors"

	"github.com/megakuul/bob/internal/store"
)

// Retained returns what cache GC must keep for releases (R-STORE-150): the store hashes of
// the bob-served closures, the OCI digests of the images and the rel/ asset keys of every
// release that is not deleted (published, publishing, pending or failed). Releases are found
// through the registry-wide index ridx, which every release record enters when it is created.
// A deleted release's objects become ordinary cache objects subject to GC.
func (s *Releases) Retained(ctx context.Context) (paths, blobs, assets map[string]bool, err error) {
	paths, blobs, assets = map[string]bool{}, map[string]bool{}, map[string]bool{}
	all, err := s.entries(ctx, "")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, l := range all {
		if l.e.State == store.ReleaseDeleted {
			continue
		}
		rec, err := store.Get(ctx, s.c.Control, store.ReleaseRecordKey(l.project, l.e.N))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if rec.State.Value() == store.ReleaseDeleted {
			continue
		}
		for _, h := range rec.Paths.Value() {
			paths[h] = true
		}
		for _, d := range rec.Blobs.Value() {
			blobs[d] = true
		}
		for _, a := range rec.Assets.Value() {
			assets[assetKey(l.project, l.e.N, a.File)] = true
		}
	}
	return paths, blobs, assets, nil
}
