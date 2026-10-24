package release_test

import (
	"encoding/json"
	"strings"
	"testing"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/release"
	"github.com/megakuul/bob/internal/store"
)

// visOnly rewrites the visibility document of id below prefix to list only project (as if
// another project's promotion had published the object).
func (e *env) visOnly(prefix, id, project string) {
	e.t.Helper()
	if _, err := e.obj.Put(e.ctx, prefix+id+".json", []byte(`{"v":1,"projects":["`+project+`"],"public":[]}`), "application/json", store.Cond{}); err != nil {
		e.t.Fatal(err)
	}
}

// fresh is a release store with empty visibility caches (documents are cached for 30 s, and
// visOnly rewrites them behind the cache's back).
func (e *env) fresh() *release.Releases {
	e.t.Helper()
	r, err := release.New(release.Config{Control: e.control, Objects: e.obj, NixVis: cache.NewVisStore(e.obj, cache.NixVisPrefix, nil),
		OCIVis: cache.NewVisStore(e.obj, cache.OCIVisPrefix, nil), Repos: e.reg.Repos, BaseURL: "https://bob.test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// TestReleaseOnlyPublishesTheProjectsOwnPaths is the regression test of I-R2 F1/F5: a release
// fails, and makes nothing public, when its closure reaches a path or image object that is not
// the project's own (published by another project, e.g. after a compromised worker staged it
// first), or a path this build did not reproduce (non_reproducible).
func TestReleaseOnlyPublishesTheProjectsOwnPaths(t *testing.T) {
	e := newEnv(t)
	lib := cache.HashOf(storePath("libapi"))

	req := e.release("2.0.0", true)
	e.visOnly(cache.NixVisPrefix, lib, "other")
	if _, err := e.fresh().Publish(e.ctx, req); err == nil || !strings.Contains(err.Error(), "is not a path of project acme") {
		t.Errorf("release with another project's path in its closure: %v, want a failure naming it", err)
	}
	if doc, err := cache.NewVisStore(e.obj, cache.NixVisPrefix, nil).Get(e.ctx, lib); err != nil || doc == nil || len(doc.Public) != 0 {
		t.Errorf("the foreign path became public: %+v %v", doc, err)
	}

	req = e.release("3.0.0", true) // serves libapi again for acme
	err := store.Change(e.ctx, e.control, &store.BuildRecord{ID: req.Build.ID}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		next := *cur
		next.NonReproducible = store.Set(map[string][]string{"j": {storePath("libapi")}})
		return &next, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Build, err = store.Get(e.ctx, e.control, &store.BuildRecord{ID: req.Build.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fresh().Publish(e.ctx, req); err == nil || !strings.Contains(err.Error(), "non_reproducible") {
		t.Errorf("release with a non_reproducible closure path: %v, want a failure naming it", err)
	}

	req = e.release("4.0.0", true)
	e.visOnly(cache.OCIVisPrefix, e.layerOf(req), "other")
	if _, err := e.fresh().Publish(e.ctx, req); err == nil || !strings.Contains(err.Error(), "is not an object of project acme") {
		t.Errorf("release with another project's image object: %v, want a failure naming it", err)
	}
}

// layerOf returns the hex digest of the layer of acme's image that the build of req tagged.
func (e *env) layerOf(req buildctl.ReleaseRequest) string {
	e.t.Helper()
	idx, _, err := e.reg.Repos.Get(e.ctx, "acme/api")
	if err != nil || idx == nil {
		e.t.Fatalf("repo index: %v", err)
	}
	read := func(d string, v any) {
		body, _, err := e.obj.Read(e.ctx, registry.BlobKey(d), 4<<20)
		if err != nil {
			e.t.Fatal(err)
		}
		if err := json.Unmarshal(body, v); err != nil {
			e.t.Fatal(err)
		}
	}
	var index struct{ Manifests []struct{ Digest string } }
	read(idx.Tags[req.Build.ID.Value()].D, &index)
	var man struct{ Layers []struct{ Digest string } }
	read(index.Manifests[0].Digest, &man)
	return strings.TrimPrefix(man.Layers[0].Digest, "sha256:")
}
