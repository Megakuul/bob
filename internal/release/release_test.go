package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/megakuul/bob/internal/api"
	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/cache"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	releasev1 "github.com/megakuul/bob/internal/gen/bob/release/v1"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/release"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

type scoper struct{}

func (scoper) Scope(_ context.Context, p *api.Principal, _ string) (rbac.Scope, error) {
	if p == nil {
		return rbac.Scope{}, nil
	}
	return rbac.Scope{Projects: []string{strings.TrimPrefix(p.ID, "sa:")}}, nil
}

type env struct {
	t              *testing.T
	ctx            context.Context
	control        *store.Bucket
	obj            *store.Objects
	nixVis, ociVis *cache.VisStore
	reg            *registry.Registry
	rel            *release.Releases
	svc            *release.Service
	srv            *httptest.Server
	builds         int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	t.Setenv("AWS_ACCESS_KEY_ID", rustfs.RootAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", rustfs.RootSecretKey)
	t.Setenv("AWS_REGION", "us-east-1")
	e := &env{t: t, ctx: ctx}
	if e.control, err = store.Open(ctx, store.BucketConfig{Endpoint: s.URL, Region: "us-east-1", Name: "bob-control", Role: "control"}); err != nil {
		t.Fatal(err)
	}
	if e.obj, err = store.OpenObjects(ctx, s3client.Options{Endpoint: s.URL, Region: "us-east-1"}, "bob-artifacts"); err != nil {
		t.Fatal(err)
	}
	e.nixVis = cache.NewVisStore(e.obj, cache.NixVisPrefix, nil)
	e.ociVis = cache.NewVisStore(e.obj, cache.OCIVisPrefix, nil)
	e.reg = registry.New(registry.Config{Control: e.control, Objects: e.obj, Vis: e.ociVis})
	if e.rel, err = release.New(release.Config{Control: e.control, Objects: e.obj, NixVis: e.nixVis, OCIVis: e.ociVis,
		Repos: e.reg.Repos, BaseURL: "https://bob.test"}); err != nil {
		t.Fatal(err)
	}
	e.svc = &release.Service{R: e.rel, Scope: scoper{}}
	principal := func(r *http.Request) (*api.Principal, error) {
		_, pw, ok := r.BasicAuth()
		switch {
		case !ok:
			return nil, nil
		case strings.HasPrefix(pw, "tok-"):
			return &api.Principal{ID: "sa:" + strings.TrimPrefix(pw, "tok-")}, nil
		}
		return nil, errors.New("invalid")
	}
	mux := http.NewServeMux()
	mux.Handle("GET /r/", release.NewDownloads(e.rel, principal, scoper{}, 4))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func storePath(name string) string {
	h := sha256.Sum256([]byte(name))
	return "/nix/store/" + cache.Nix32(h[:20]) + "-" + name
}

// serve publishes a store path in the binary cache as promotion would (NAR, narinfo,
// visibility with project).
func (e *env) serve(project, path string, files map[string]string, refs ...string) {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	var n bytes.Buffer
	if err := nar.Write(&n, dir, nil); err != nil {
		e.t.Fatal(err)
	}
	var z bytes.Buffer
	zw, _ := zstd.NewWriter(&z)
	_, _ = zw.Write(n.Bytes())
	_ = zw.Close()
	h := cache.HashOf(path)
	sum := sha256.Sum256(n.Bytes())
	ni := &cache.NarInfo{StorePath: path, URL: "nar/" + h + ".nar.zst", Compression: "zstd",
		NarHash: "sha256:" + cache.Nix32(sum[:]), NarSize: uint64(n.Len()), References: refs}
	for k, b := range map[string][]byte{cache.NarKey(h): z.Bytes(), cache.NarInfoKey(h): ni.Format()} {
		if _, err := e.obj.Put(e.ctx, k, b, "", store.Cond{}); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.nixVis.AddProject(e.ctx, h, project); err != nil {
		e.t.Fatal(err)
	}
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// release builds and promotes everything release version of acme/api consists of and
// returns the request the build controller would make.
func (e *env) release(version string, public bool) buildctl.ReleaseRequest {
	e.t.Helper()
	e.builds++
	build := "b-01TEST" + strings.ToUpper(strings.NewReplacer(".", "X", "-", "Y").Replace(version)) + string(rune('A'+e.builds))
	job := build + ".j0001"
	staging := "staging/" + build + "/" + job + "/1/"
	apiPath := storePath("cmd-api-" + version)
	lib := storePath("libapi")
	e.serve("acme", lib, map[string]string{"lib": "x"})
	e.serve("acme", apiPath, map[string]string{"api": "binary " + version}, lib)
	// the image (promoted by the registry's Images hook)
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	_ = tw.WriteHeader(&tar.Header{Name: "v", Mode: 0o644, Size: int64(len(version))})
	_, _ = tw.Write([]byte(version))
	_ = tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	cfg, _ := json.Marshal(map[string]any{"os": "linux", "architecture": "arm64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{digestOf(raw.Bytes())}}})
	man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"digest": digestOf(cfg), "size": len(cfg)}, "layers": []any{map[string]any{"digest": digestOf(gz.Bytes()), "size": gz.Len()}}})
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{"digest": digestOf(man), "size": len(man), "platform": map[string]string{"os": "linux", "architecture": "arm64"}}}})
	for _, b := range [][]byte{idx, man, cfg, gz.Bytes()} {
		if _, err := e.obj.Put(e.ctx, staging+"oci/blobs/sha256/"+strings.TrimPrefix(digestOf(b), "sha256:"), b, "", store.Cond{}); err != nil {
			e.t.Fatal(err)
		}
	}
	err := e.reg.PromoteImages(e.ctx, &promote.Job{ID: job, Build: build, Project: "acme", Staging: staging, Complete: &promote.Complete{
		Images: []promote.Image{{Repo: "acme/api", Manifest: digestOf(idx), Blobs: []string{digestOf(man), digestOf(cfg), digestOf(gz.Bytes())}}}}})
	if err != nil {
		e.t.Fatal(err)
	}
	// the staged AppImage (promotion verified it and left it with COMPLETE.json)
	file := "api-" + version + "-aarch64.AppImage"
	asset := []byte("appimage bytes of " + version)
	for k, b := range map[string][]byte{staging + "assets/" + file: asset, staging + "COMPLETE.json": []byte("{}")} {
		if _, err := e.obj.Put(e.ctx, k, b, "", store.Cond{}); err != nil {
			e.t.Fatal(err)
		}
	}
	m := release.Manifest{Label: "//rel:api", Project: "acme", Public: public, Release: "api", Schema: release.Schema, Version: version,
		Artifacts: []release.Artifact{
			{Kind: "closure", Name: "api", Platform: "linux-arm64", StorePath: apiPath, Entrypoints: map[string]string{"api": "bin/api"}},
			{Kind: "appimage", Name: "bundle", Platform: "linux-arm64", StorePath: storePath("bundle-" + version), File: file,
				SHA256: strings.TrimPrefix(digestOf(asset), "sha256:"), Size: int64(len(asset))},
			{Kind: "oci", Name: "image", Platform: "multi", StorePath: storePath("image-" + version), Repo: "acme/api", Tags: []string{version}, Index: digestOf(idx)},
		}}
	body, _ := json.Marshal(m)
	manifest := storePath("release-api-" + version)
	e.serve("acme", manifest, map[string]string{"release.json": string(body)}, apiPath)
	// the build record as the controller has it
	if err := store.Create(e.ctx, e.control, &store.BuildRecord{ID: store.ID(build), Project: store.Set("acme"), Commit: store.Set("c0ffee"),
		State: store.Set("running"), Jobs: store.Set([]store.BuildJob{{ID: job, State: "succeeded", Attempts: 1}})}); err != nil {
		e.t.Fatal(err)
	}
	rec, err := store.Get(e.ctx, e.control, &store.BuildRecord{ID: store.ID(build)})
	if err != nil {
		e.t.Fatal(err)
	}
	return buildctl.ReleaseRequest{Build: rec, Pipeline: "release", Label: "//rel:api", Tag: "v" + version,
		Nodes: []buildctl.Node{{Label: "//rel:api", Platform: "aarch64-linux", Job: job, Outputs: []string{manifest}}}}
}

func (e *env) get(path, token string) (int, http.Header, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.SetBasicAuth("bob", token)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

func (e *env) setVisibility(id string, public bool) {
	e.t.Helper()
	v := commonv1.Visibility_VISIBILITY_PRIVATE
	if public {
		v = commonv1.Visibility_VISIBILITY_PUBLIC
	}
	if _, err := e.svc.UpdateRelease(e.ctx, connect.NewRequest(&releasev1.UpdateReleaseRequest{
		Release: &releasev1.Release{Id: id, Visibility: v}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"visibility"}}})); err != nil {
		e.t.Fatal(err)
	}
}

func TestReleases(t *testing.T) {
	e := newEnv(t)
	req := e.release("1.4.2", false)
	id, err := e.rel.Publish(e.ctx, req)
	if err != nil || id != "acme.0000000001" {
		t.Fatalf("Publish = %q, %v", id, err)
	}
	if again, err := e.rel.Publish(e.ctx, req); err != nil || again != id {
		t.Errorf("second Publish of the same build = %q, %v; want the same release", again, err)
	}
	rec, err := store.Get(e.ctx, e.control, store.ReleaseRecordKey("acme", 1))
	if err != nil || rec.State.Value() != store.ReleasePublished || len(rec.Paths.Value()) != 3 || len(rec.Blobs.Value()) != 4 {
		t.Fatalf("record %v: state %s paths %v blobs %v", err, rec.State.Value(), rec.Paths.Value(), rec.Blobs.Value())
	}
	file := "api-1.4.2-aarch64.AppImage"
	staging := "staging/" + req.Build.ID.Value() + "/" + req.Build.ID.Value() + ".j0001/1/"
	if _, err := e.obj.Head(e.ctx, "rel/acme/0000000001/"+file); err != nil {
		t.Errorf("asset not copied: %v", err)
	}
	for _, k := range []string{staging + "assets/" + file, staging + "COMPLETE.json"} {
		if _, err := e.obj.Head(e.ctx, k); !errors.Is(err, store.ErrObjectNotFound) {
			t.Errorf("staged %s not deleted after the copy: %v", k, err)
		}
	}
	idx, _, _ := e.reg.Repos.Get(e.ctx, "acme/api")
	if idx == nil || idx.Tags["1.4.2"].R != id || idx.Tags["latest"].D != idx.Tags["1.4.2"].D {
		t.Errorf("repository index %+v; want 1.4.2 and latest of %s", idx, id)
	}

	// private: anonymous callers see nothing
	if _, err := e.svc.GetRelease(e.ctx, connect.NewRequest(&releasev1.GetReleaseRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("anonymous GetRelease of a private release: %v", err)
	}
	if code, _, _ := e.get("/r/acme/api/1.4.2/"+file, ""); code != 404 {
		t.Errorf("anonymous private download: %d", code)
	}
	if code, _, body := e.get("/r/acme/api/1.4.2/"+file, "tok-acme"); code != 200 || string(body) != "appimage bytes of 1.4.2" {
		t.Errorf("permitted private download: %d %q", code, body)
	}
	if code, _, _ := e.get("/r/acme/api/1.4.2/"+file, "tok-other"); code != 404 {
		t.Errorf("other project's download: %d", code)
	}
	for _, h := range rec.Paths.Value() {
		if doc, _ := e.nixVis.Get(e.ctx, h); doc == nil || len(doc.Public) != 0 {
			t.Errorf("private release made %s public: %+v", h, doc)
		}
	}

	// public
	e.setVisibility(id, true)
	code, hdr, body := e.get("/r/acme/api/1.4.2/"+file, "")
	sum := sha256.Sum256(body)
	if code != 200 || hex.EncodeToString(sum[:]) != strings.TrimPrefix(digestOf([]byte("appimage bytes of 1.4.2")), "sha256:") ||
		hdr.Get("ETag") != `"`+hex.EncodeToString(sum[:])+`"` || hdr.Get("Content-Type") != "application/octet-stream" ||
		hdr.Get("Content-Disposition") != `attachment; filename="`+file+`"` || hdr.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("public download: %d %v", code, hdr)
	}
	if code, hdr, _ := e.get("/r/acme/api/latest/"+file, ""); code != 302 || hdr.Get("Cache-Control") != "no-cache" || hdr.Get("Location") != "/r/acme/api/1.4.2/"+file {
		t.Errorf("latest: %d %v", code, hdr)
	}
	for _, h := range rec.Paths.Value() {
		if doc, _ := e.nixVis.Get(e.ctx, h); doc == nil || !slices.Equal(doc.Public, []string{id}) {
			t.Errorf("public release: %s not public: %+v", h, doc)
		}
	}
	for _, d := range rec.Blobs.Value() {
		if doc, _ := e.ociVis.Get(e.ctx, d); doc == nil || !slices.Equal(doc.Public, []string{id}) {
			t.Errorf("public release: image object %s not public: %+v", d, doc)
		}
	}
	got, err := e.svc.GetRelease(e.ctx, connect.NewRequest(&releasev1.GetReleaseRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	r := got.Msg.GetRelease()
	if r.GetState() != releasev1.ReleaseState_RELEASE_STATE_PUBLISHED || len(r.GetClosures()) != 1 || r.GetClosures()[0].GetPlatform() != "linux-arm64" ||
		len(r.GetAssets()) != 1 || r.GetAssets()[0].GetUrl() != "https://bob.test/r/acme/api/1.4.2/"+file || len(r.GetImages()) != 1 || r.GetImages()[0].GetTag() != "1.4.2" {
		t.Errorf("GetRelease = %v", r)
	}

	// the same version from another build: immutable
	dup := e.release("1.4.2", false)
	if _, err := e.rel.Publish(e.ctx, dup); err == nil || err.Error() != "release acme/api@1.4.2 already exists; releases are immutable" {
		t.Errorf("duplicate version: %v", err)
	}
	// a tag that does not match the manifest
	bad := e.release("1.4.9", false)
	bad.Tag = "v1.4.8"
	var rf *buildctl.ReleaseFailure
	if _, err := e.rel.Publish(e.ctx, bad); !errors.As(err, &rf) || !strings.Contains(err.Error(), "tag v1.4.8") {
		t.Errorf("tag mismatch: %v", err)
	}

	// ResolveRelease
	for _, v := range []string{"1.4.0", "1.5.0", "1.6.0-rc.1"} {
		rid, err := e.rel.Publish(e.ctx, e.release(v, true))
		if err != nil {
			t.Fatalf("publish %s: %v", v, err)
		}
		if rec, _ := store.Get(e.ctx, e.control, &store.ReleaseRecord{Project: store.ID("acme"), N: store.ID(strings.TrimPrefix(rid, "acme."))}); rec == nil || !rec.Public.Value() {
			t.Errorf("release %s: public in the manifest, not in the record", v)
		}
	}
	for c, want := range map[string]string{"1.4": "1.4.2", "": "1.5.0", "1.6.0-rc.1": "1.6.0-rc.1", "1.4.0": "1.4.0"} {
		res, err := e.svc.ResolveRelease(e.ctx, connect.NewRequest(&releasev1.ResolveReleaseRequest{ProjectId: "acme", Name: "api", Constraint: c, Platform: "linux-arm64"}))
		if err != nil || res.Msg.GetRelease().GetVersion() != want || res.Msg.GetClosure() == nil || res.Msg.GetSubstituters()[0] != "https://bob.test/nix" {
			t.Errorf("ResolveRelease(%q) = %v, %v; want %s", c, res, err, want)
		}
	}
	if _, err := e.svc.ResolveRelease(e.ctx, connect.NewRequest(&releasev1.ResolveReleaseRequest{ProjectId: "acme", Name: "api", Constraint: "1.6"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("ResolveRelease(1.6): %v; a pre-release resolves only exactly", err)
	}
	list, err := e.svc.ListReleases(e.ctx, connect.NewRequest(&releasev1.ListReleasesRequest{ProjectId: "acme", Name: "api"}))
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, r := range list.Msg.GetReleases() {
		versions = append(versions, r.GetVersion())
	}
	if !slices.Equal(versions, []string{"1.6.0-rc.1", "1.5.0", "1.4.2", "1.4.0"}) {
		t.Errorf("ListReleases = %v; want newest version first, failed releases hidden from anonymous callers", versions)
	}
	if idx, _, _ := e.reg.Repos.Get(e.ctx, "acme/api"); idx.Tags["latest"].R != "acme.0000000003" {
		t.Errorf("latest = %+v; want 1.5.0 (acme.0000000003, pre-releases never)", idx.Tags["latest"])
	}

	// DeleteRelease: tombstone, documents, tags, assets; the version is never reused
	if live, err := store.HasLiveReleases(e.ctx, e.control, "acme"); err != nil || !live {
		t.Errorf("HasLiveReleases = %v, %v", live, err)
	}
	projects, err := project.New(project.Config{Control: e.control})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Create(e.ctx, project.Spec{ID: "acme", DisplayName: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := projects.Delete(e.ctx, "acme", ""); !errors.Is(err, project.ErrReleases) {
		t.Errorf("DeleteProject with releases: %v, want ErrReleases (R-STORE-007)", err)
	}
	if _, err := e.svc.DeleteRelease(e.ctx, connect.NewRequest(&releasev1.DeleteReleaseRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetRelease(e.ctx, connect.NewRequest(&releasev1.GetReleaseRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("GetRelease after delete: %v", err)
	}
	if _, err := e.obj.Head(e.ctx, "rel/acme/0000000001/"+file); !errors.Is(err, store.ErrObjectNotFound) {
		t.Errorf("asset kept after delete: %v", err)
	}
	if idx, _, _ := e.reg.Repos.Get(e.ctx, "acme/api"); idx.Tags["1.4.2"].D != "" {
		t.Errorf("tag 1.4.2 kept after delete")
	}
	if doc, _ := e.nixVis.Get(e.ctx, cache.HashOf(storePath("cmd-api-1.4.2"))); doc == nil || len(doc.Public) != 0 {
		t.Errorf("visibility after delete: %+v", doc)
	}
	if _, err := e.rel.Publish(e.ctx, e.release("1.4.2", false)); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("re-publish of a deleted version: %v", err)
	}
}

// TestDeleteProjectAfterReleases: once every release is deleted the project can be deleted.
func TestDeleteProjectAfterReleases(t *testing.T) {
	e := newEnv(t)
	id, err := e.rel.Publish(e.ctx, e.release("1.0.0", false))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.New(project.Config{Control: e.control})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Create(e.ctx, project.Spec{ID: "acme", DisplayName: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := projects.Delete(e.ctx, "acme", ""); !errors.Is(err, project.ErrReleases) {
		t.Fatalf("DeleteProject with a release: %v", err)
	}
	paths, blobs, assets, err := e.rel.Retained(e.ctx)
	if err != nil || len(paths) != 3 || len(blobs) != 4 || !assets["rel/acme/0000000001/api-1.0.0-aarch64.AppImage"] {
		t.Errorf("Retained = %v %v %v, %v", paths, blobs, assets, err)
	}
	if _, err := e.svc.DeleteRelease(e.ctx, connect.NewRequest(&releasev1.DeleteReleaseRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if paths, blobs, assets, err := e.rel.Retained(e.ctx); err != nil || len(paths)+len(blobs)+len(assets) != 0 {
		t.Errorf("Retained after DeleteRelease = %v %v %v, %v", paths, blobs, assets, err)
	}
	if err := projects.Delete(e.ctx, "acme", ""); err != nil {
		t.Errorf("DeleteProject after DeleteRelease: %v", err)
	}
}
