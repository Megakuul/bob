package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/engine/nar"
)

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// fakeStore builds store paths under <dir>/store/store and returns their PathInfos.
type fakeStore struct {
	t     *testing.T
	root  string
	paths []PathInfo
}

func (f *fakeStore) path(name string, files map[string]string) (string, string) {
	sp := "/nix/store/" + strings.Repeat(string("0123456789abcdfghijklmnpqrsvwxyz"[len(f.paths)]), 32) + "-" + name
	dir := filepath.Join(f.root, strings.TrimPrefix(sp, "/nix/store/"))
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if t, ok := strings.CutPrefix(body, "->"); ok {
			if err := os.Symlink(t, p); err != nil {
				f.t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	sum, size, err := nar.Hash(dir, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.paths = append(f.paths, PathInfo{Path: sp, NarHash: "sha256:" + cache.Nix32(sum[:]), NarSize: uint64(size)})
	return sp, dir
}

// image writes an OCI layout like pkg/output/oci (layer blob = relative link into the layer's
// store path) and returns its store path, the index digest and all object digests.
func (f *fakeStore) image(name, layer string) (string, string, []string) {
	layerPath, _ := f.path("oci-layer-"+name, map[string]string{"layer.tar.gz": layer})
	cfg := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digest(cfg), "size": len(cfg)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": digest([]byte(layer)), "size": len(layer)}}})
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": digest(man), "size": len(man)}}})
	top, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"digest": digest(idx)}}})
	hx := func(b []byte) string { return "blobs/sha256/" + strings.TrimPrefix(digest(b), "sha256:") }
	sp, _ := f.path(name, map[string]string{
		"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": string(top),
		hx(idx): string(idx), hx(man): string(man), hx(cfg): string(cfg),
		hx([]byte(layer)): "->../../../" + strings.TrimPrefix(layerPath, "/nix/store/") + "/layer.tar.gz",
	})
	return sp, digest(idx), []string{digest(idx), digest(man), digest(cfg), digest([]byte(layer))}
}

func stageJob(t *testing.T, dir string, res RunResult) (*memArtifacts, Outcome) {
	t.Helper()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(work, workOut), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	_ = os.WriteFile(filepath.Join(work, workOut, resultFile), b, 0o644)
	art := &memArtifacts{m: map[string][]byte{}}
	p := &Process{store: filepath.Join(dir, "store")}
	p.UseArtifacts(art, "")
	spec := store.JobSpec{V: 1, JobID: "b.j0001", BuildID: "bid", Project: "acme", Staging: "staging/bid/b.j0001/"}
	o := Outcome{Status: "ok"}
	p.stage(context.Background(), Claim{JobID: "b.j0001", Attempt: 1}, spec, work, &o)
	return art, o
}

func TestStageImagesAndReleaseAssets(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{t: t, root: filepath.Join(dir, "store", "store")}
	plain, plainIdx, plainObjs := f.image("api-image", "layer-a")
	relImg, relIdx, relObjs := f.image("api-image-1.4.2", "layer-a")
	app := "appimage bytes"
	appPath, _ := f.path("api-appimage", map[string]string{"api-appimage-aarch64.AppImage": app})
	file := "api-appimage-1.4.2-aarch64.AppImage"
	m := map[string]any{"schema": "bob.release/v1", "project": "", "release": "api", "label": "//deploy:api", "version": "1.4.2",
		"artifacts": []any{
			map[string]any{"kind": "appimage", "name": "api-appimage", "platform": "linux-arm64", "storePath": appPath, "file": file,
				"sha256": strings.TrimPrefix(digest([]byte(app)), "sha256:"), "size": len(app)},
			map[string]any{"kind": "oci", "name": "api-image", "platform": "multi", "storePath": relImg, "repo": "api", "index": relIdx, "tags": []string{"1.4.2"}},
		}}
	mb, _ := json.Marshal(m)
	relPath, _ := f.path("release-api-1.4.2", map[string]string{"release.json": string(mb)})
	res := RunResult{Paths: f.paths, Outputs: []BuiltOutput{{Label: "//deploy:api", Path: relPath}, {Label: "//deploy:api-image", Path: plain}}}
	art, o := stageJob(t, dir, res)
	if o.Status != "ok" || o.Complete == "" {
		t.Fatalf("outcome %+v", o)
	}
	c, err := promote.ParseComplete(art.m[o.Complete], "b.j0001", "bid", "acme")
	if err != nil {
		t.Fatalf("COMPLETE.json %s: %v", art.m[o.Complete], err)
	}
	staging := "staging/bid/b.j0001/1/"
	want := map[string]promote.Image{
		"acme/api":       {Repo: "acme/api", Tag: "1.4.2", Manifest: relIdx, MediaType: "application/vnd.oci.image.index.v1+json"},
		"acme/api-image": {Repo: "acme/api-image", Manifest: plainIdx, MediaType: "application/vnd.oci.image.index.v1+json"},
	}
	objs := map[string][]string{"acme/api": relObjs, "acme/api-image": plainObjs}
	if len(c.Images) != 2 {
		t.Fatalf("images %+v", c.Images)
	}
	for _, im := range c.Images {
		w := want[im.Repo]
		rest := slices.Sorted(slices.Values(objs[im.Repo][1:]))
		if im.Tag != w.Tag || im.Manifest != w.Manifest || im.MediaType != w.MediaType || !slices.Equal(im.Blobs, rest) {
			t.Errorf("image %+v, want %+v with blobs %v", im, w, rest)
		}
		for _, d := range objs[im.Repo] {
			b, ok := art.m[staging+"oci/blobs/sha256/"+strings.TrimPrefix(d, "sha256:")]
			if !ok || digest(b) != d {
				t.Errorf("blob %s not staged or wrong (%d bytes)", d, len(b))
			}
		}
	}
	if len(c.Assets) != 1 || c.Assets[0].File != file || c.Assets[0].Size != int64(len(app)) || string(art.m[staging+"assets/"+file]) != app {
		t.Errorf("assets %+v", c.Assets)
	}
}

func TestStageOutputsRefusesHostileLayouts(t *testing.T) {
	for name, mutate := range map[string]func(f *fakeStore, idx string, objs []string) (string, []PathInfo){
		// a blob that links to a host file: never uploaded
		"link out of the store": func(f *fakeStore, sp string, objs []string) (string, []PathInfo) {
			dir := filepath.Join(f.root, strings.TrimPrefix(sp, "/nix/store/"))
			l := filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(objs[3], "sha256:"))
			_ = os.Remove(l)
			_ = os.Symlink("../../../../../../../../etc/hostname", l)
			return sp, f.paths
		},
		// a layer store path the job did not build (not in the closure)
		"link outside the closure": func(f *fakeStore, sp string, _ []string) (string, []PathInfo) {
			return sp, slices.DeleteFunc(slices.Clone(f.paths), func(p PathInfo) bool { return strings.Contains(p.Path, "oci-layer") })
		},
		// a blob whose bytes are not its digest
		"digest mismatch": func(f *fakeStore, sp string, objs []string) (string, []PathInfo) {
			l := filepath.Join(f.root, strings.TrimPrefix(f.paths[0].Path, "/nix/store/"), "layer.tar.gz")
			_ = os.WriteFile(l, []byte("other bytes"), 0o644)
			return sp, f.paths
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := &fakeStore{t: t, root: filepath.Join(dir, "store", "store")}
			sp, _, objs := f.image("img", "layer-a")
			sp, paths := mutate(f, sp, objs)
			art, o := stageJob(t, dir, RunResult{Paths: paths, Outputs: []BuiltOutput{{Label: "//x:img", Path: sp}}})
			if o.Status != "failed" || o.Complete != "" || !strings.Contains(o.Error, "staging images and release assets") {
				t.Fatalf("outcome %+v", o)
			}
			for k, b := range art.m {
				if strings.Contains(k, "COMPLETE.json") || strings.Contains(string(b), "localhost") {
					t.Errorf("staged %s", k)
				}
			}
		})
	}
}
