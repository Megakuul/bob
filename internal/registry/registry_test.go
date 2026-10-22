package registry_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// scoper grants GetRepository on project p to principal "sa:p".
type scoper struct{}

func (scoper) Scope(_ context.Context, p *api.Principal, _ string) (rbac.Scope, error) {
	if p == nil {
		return rbac.Scope{}, nil
	}
	if p.ID == "sa:admin" {
		return rbac.Scope{All: true}, nil
	}
	return rbac.Scope{Projects: []string{strings.TrimPrefix(p.ID, "sa:")}}, nil
}

type env struct {
	t       *testing.T
	ctx     context.Context
	control *store.Bucket
	obj     *store.Objects
	vis     *cache.VisStore
	reg     *registry.Registry
	srv     *httptest.Server
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
	e.vis = cache.NewVisStore(e.obj, cache.OCIVisPrefix, nil)
	e.reg = registry.New(registry.Config{Control: e.control, Objects: e.obj, Vis: e.vis, Scope: scoper{},
		Principal: func(r *http.Request) (*api.Principal, error) {
			_, pw, ok := r.BasicAuth()
			switch {
			case !ok:
				return nil, nil
			case strings.HasPrefix(pw, "tok-"):
				return &api.Principal{ID: "sa:" + strings.TrimPrefix(pw, "tok-")}, nil
			}
			return nil, errors.New("invalid token")
		}})
	mux := http.NewServeMux()
	for _, m := range []string{"GET", "PUT", "POST", "PATCH", "DELETE"} {
		mux.Handle(m+" /v2/", e.reg)
	}
	mux.Handle("GET /oci/token", e.reg.TokenHandler())
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// image is a minimal valid single-platform OCI image (one layer with one file) behind an index.
type image struct {
	index, manifest, config, layer []byte
}

func newImage(t *testing.T, text string) image {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	body := []byte(text + "\n")
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	_ = tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	im := image{layer: gz.Bytes()}
	im.config, _ = json.Marshal(map[string]any{"architecture": "arm64", "os": "linux",
		"config": map[string]any{"Cmd": []string{"/hello.txt"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digestOf(raw.Bytes())}}})
	im.manifest, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(im.config), "size": len(im.config)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": digestOf(im.layer), "size": len(im.layer)}}})
	plat := map[string]any{"architecture": "arm64", "os": "linux"}
	if out, err := os.ReadFile("/proc/sys/kernel/arch"); err == nil && strings.TrimSpace(string(out)) == "x86_64" {
		plat["architecture"] = "amd64"
	}
	im.index, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": digestOf(im.manifest),
			"size": len(im.manifest), "platform": plat}}})
	return im
}

// stage writes the image's blobs into a job's staging and returns the job.
func (e *env) stage(project, build, repo string, im image) *promote.Job {
	e.t.Helper()
	j := &promote.Job{ID: build + ".j0001", Build: build, Project: project, Staging: "staging/" + build + "/" + build + ".j0001/1/"}
	var blobs []string
	for _, b := range [][]byte{im.index, im.manifest, im.config, im.layer} {
		if _, err := e.obj.Put(e.ctx, j.Staging+"oci/blobs/sha256/"+strings.TrimPrefix(digestOf(b), "sha256:"), b, "application/octet-stream", store.Cond{}); err != nil {
			e.t.Fatal(err)
		}
		if !bytes.Equal(b, im.index) {
			blobs = append(blobs, digestOf(b))
		}
	}
	j.Complete = &promote.Complete{V: 1, Job: j.ID, Build: build, Project: project,
		Images: []promote.Image{{Repo: repo, Tag: "x", Manifest: digestOf(im.index), MediaType: "application/vnd.oci.image.index.v1+json", Blobs: blobs}}}
	return j
}

func (e *env) get(path, bearer string) (int, http.Header, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return do(e.t, req)
}

func do(t *testing.T, req *http.Request) (int, http.Header, []byte) {
	t.Helper()
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

// token asks /oci/token for pull on repo with the Basic password pw ("" = anonymous).
func (e *env) token(repo, pw string) (string, int) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/oci/token?service=bob&scope=repository:"+repo+":pull", nil)
	if pw != "" {
		req.SetBasicAuth("bob", pw)
	}
	code, _, body := do(e.t, req)
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	tok, _ := raw["token"].(string)
	if code == 200 && (tok == "" || raw["access_token"] != tok || raw["expires_in"] != float64(900) || raw["issued_at"] == nil) {
		e.t.Errorf("token response %s", body)
	}
	return tok, code
}

func TestRegistry(t *testing.T) {
	e := newEnv(t)
	pub, priv := newImage(t, "public"), newImage(t, "private")
	if err := e.reg.PromoteImages(e.ctx, e.stage("p1", "b-01PUB", "p1/app", pub)); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.PromoteImages(e.ctx, e.stage("p2", "b-01PRIV", "p2/app", priv)); err != nil {
		t.Fatal(err)
	}
	// promotion: served blobs, visibility with the project, build tag = build id
	for _, b := range [][]byte{pub.index, pub.manifest, pub.config, pub.layer} {
		if _, err := e.obj.Head(e.ctx, registry.BlobKey(digestOf(b))); err != nil {
			t.Fatalf("blob %s not served: %v", digestOf(b), err)
		}
		doc, err := e.vis.Get(e.ctx, strings.TrimPrefix(digestOf(b), "sha256:"))
		if err != nil || doc == nil || !slices.Equal(doc.Projects, []string{"p1"}) {
			t.Fatalf("visibility of %s = %+v, %v", digestOf(b), doc, err)
		}
	}
	idx, _, err := e.reg.Repos.Get(e.ctx, "p1/app")
	if err != nil || idx == nil || idx.Tags["b-01PUB"].D != digestOf(pub.index) {
		t.Fatalf("repo index %+v %v; want tag b-01PUB", idx, err)
	}
	// a release makes the p1 image public and tags it
	for _, b := range [][]byte{pub.index, pub.manifest, pub.config, pub.layer} {
		if err := e.vis.SetPublic(e.ctx, strings.TrimPrefix(digestOf(b), "sha256:"), "p1.0000000001", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.reg.Repos.SetReleaseTag(e.ctx, "p1/app", "1.0.0", digestOf(pub.index), registry.MediaTypeOf(pub.index), "p1.0000000001", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Repos.SetReleaseTag(e.ctx, "p2/app", "1.0.0", digestOf(priv.index), "", "p2.0000000001", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Repos.SetReleaseTag(e.ctx, "p1/app", "1.0.0", digestOf(priv.index), "", "p1.0000000002", 2); !errors.Is(err, registry.ErrTagExists) {
		t.Errorf("second release tag 1.0.0: %v, want ErrTagExists", err)
	}

	t.Run("ping", func(t *testing.T) {
		code, hdr, _ := e.get("/v2/", "")
		if c := hdr.Get("WWW-Authenticate"); code != 401 || !strings.HasPrefix(c, `Bearer realm="`+e.srv.URL+`/oci/token"`) || !strings.Contains(c, `service="bob"`) {
			t.Errorf("GET /v2/ anonymous: %d %q", code, c)
		}
		tok, _ := e.token("p1/app", "")
		if code, _, body := e.get("/v2/", tok); code != 200 || string(body) != "{}" {
			t.Errorf("GET /v2/ with a token: %d %s", code, body)
		}
	})
	t.Run("anonymous public pull", func(t *testing.T) {
		tok, code := e.token("p1/app", "")
		if code != 200 {
			t.Fatalf("token: %d", code)
		}
		code, hdr, body := e.get("/v2/p1/app/manifests/1.0.0", tok)
		if code != 200 || !bytes.Equal(body, pub.index) || hdr.Get("Docker-Content-Digest") != digestOf(pub.index) ||
			hdr.Get("Content-Type") != "application/vnd.oci.image.index.v1+json" {
			t.Errorf("manifest by tag: %d %v", code, hdr)
		}
		if code, _, body := e.get("/v2/p1/app/manifests/latest", tok); code != 200 || !bytes.Equal(body, pub.index) {
			t.Errorf("latest: %d", code)
		}
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/v2/p1/app/blobs/"+digestOf(pub.layer), nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Range", "bytes=3-9")
		if code, hdr, body := do(t, req); code != 206 || !bytes.Equal(body, pub.layer[3:10]) || hdr.Get("Content-Range") == "" {
			t.Errorf("ranged blob: %d %q", code, hdr.Get("Content-Range"))
		}
		code, _, body = e.get("/v2/p1/app/tags/list", tok)
		var tl struct {
			Name string
			Tags []string
		}
		_ = json.Unmarshal(body, &tl)
		if code != 200 || tl.Name != "p1/app" || !slices.Equal(tl.Tags, []string{"1.0.0", "b-01PUB", "latest"}) {
			t.Errorf("anonymous tags/list: %d %s", code, body)
		}
	})
	t.Run("private", func(t *testing.T) {
		anon, _ := e.token("p2/app", "")
		code, hdr, body := e.get("/v2/p2/app/manifests/1.0.0", anon)
		if code != 401 || !strings.Contains(string(body), "UNAUTHORIZED") || !strings.Contains(hdr.Get("WWW-Authenticate"), `scope="repository:p2/app:pull"`) {
			t.Errorf("anonymous private pull: %d %s", code, body)
		}
		mine, _ := e.token("p2/app", "tok-p2")
		if code, _, body := e.get("/v2/p2/app/manifests/1.0.0", mine); code != 200 || !bytes.Equal(body, priv.index) {
			t.Errorf("p2 pull: %d", code)
		}
		other, _ := e.token("p2/app", "tok-p1")
		c1, h1, b1 := e.get("/v2/p2/app/manifests/1.0.0", other)
		c2, h2, b2 := e.get("/v2/p2/app/manifests/no-such-tag", other)
		if c1 != 401 || c1 != c2 || !bytes.Equal(b1, b2) || strings.ReplaceAll(h1.Get("WWW-Authenticate"), "1.0.0", "") != strings.ReplaceAll(h2.Get("WWW-Authenticate"), "no-such-tag", "") {
			t.Errorf("other project: %d %s vs missing %d %s", c1, b1, c2, b2)
		}
		// a blob of p2 asked through the public repository with p1's token: not visible
		pubTok, _ := e.token("p1/app", "tok-p1")
		if code, _, _ := e.get("/v2/p1/app/blobs/"+digestOf(priv.layer), pubTok); code != 404 {
			t.Errorf("foreign private blob through a visible repository: %d, want 404", code)
		}
		if _, code := e.token("p2/app", "bad"); code != 401 {
			t.Errorf("invalid credentials: %d", code)
		}
	})
	t.Run("pull only", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/v2/x/manifests/y", strings.NewReader("{}"))
		req.SetBasicAuth("bob", "tok-admin")
		code, _, body := do(t, req)
		if code != 405 || !strings.Contains(string(body), `"UNSUPPORTED"`) || !strings.Contains(string(body), "this registry is pull-only") {
			t.Errorf("PUT: %d %s", code, body)
		}
		tok, _ := e.token("p1/app", "")
		parts := strings.Split(tok, ".")
		b, _ := base64.RawURLEncoding.DecodeString(parts[1])
		forged := strings.Replace(string(b), `"acc":{`, `"acc":{"p2/app":["pull"],`, 1)
		ftok := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + parts[2]
		if code, _, _ := e.get("/v2/p2/app/manifests/1.0.0", ftok); code != 401 {
			t.Errorf("forged token: %d", code)
		}
	})
	t.Run("verification", func(t *testing.T) {
		evil := newImage(t, "evil")
		j := e.stage("p1", "b-01EVIL", "p2/app", evil)
		if err := e.reg.PromoteImages(e.ctx, j); err == nil || !strings.Contains(err.Error(), "not a repository of project p1") {
			t.Errorf("foreign repository: %v", err)
		}
		j = e.stage("p1", "b-01EVIL2", "p1/evil", evil)
		_, _ = e.obj.Put(e.ctx, j.Staging+"oci/blobs/sha256/"+strings.TrimPrefix(digestOf(evil.layer), "sha256:"), []byte("tampered"), "", store.Cond{})
		if err := e.reg.PromoteImages(e.ctx, j); err == nil || !strings.Contains(err.Error(), "does not match its digest") {
			t.Errorf("tampered blob: %v", err)
		}
		j = e.stage("p1", "b-01EVIL3", "p1/evil", evil)
		j.Complete.Images[0].Blobs = j.Complete.Images[0].Blobs[:2] // the layer is not listed
		if err := e.reg.PromoteImages(e.ctx, j); err == nil || !strings.Contains(err.Error(), "does not list") {
			t.Errorf("unlisted layer: %v", err)
		}
		// naming another project's served private blob without its bytes gives no visibility
		j = e.stage("p1", "b-01EVIL4", "p1/evil", evil)
		j.Complete.Images[0].Blobs = append(j.Complete.Images[0].Blobs, digestOf(priv.layer))
		_ = e.reg.PromoteImages(e.ctx, j)
		if doc, _ := e.vis.Get(e.ctx, strings.TrimPrefix(digestOf(priv.layer), "sha256:")); doc == nil || slices.Contains(doc.Projects, "p1") {
			t.Errorf("p1 gained visibility of p2's blob: %+v", doc)
		}
	})
	t.Run("podman", func(t *testing.T) {
		if os.Getenv("BOB_SKIP_PODMAN") != "" {
			t.Skip("BOB_SKIP_PODMAN")
		}
		p := clients.Podman(t)
		host := strings.TrimPrefix(e.srv.URL, "http://")
		p.InsecureRegistry(host)
		if r := p.Run("pull", "--tls-verify=false", host+"/p1/app:1.0.0"); r.Exit != 0 {
			t.Fatalf("anonymous podman pull of the public tag: %s", r.Combined())
		}
		if r := p.Run("pull", "--tls-verify=false", host+"/p2/app:1.0.0"); r.Exit == 0 || !strings.Contains(strings.ToLower(r.Combined()), "unauthorized") {
			t.Errorf("anonymous podman pull of a private tag: exit %d %s", r.Exit, r.Combined())
		}
		cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("bob:tok-p2"))}}})
		af := filepath.Join(p.Dir("auth"), "auth.json")
		if err := os.WriteFile(af, cfg, 0o600); err != nil {
			t.Fatal(err)
		}
		p.Own(af)
		if r := p.Run("pull", "--tls-verify=false", "--authfile", af, host+"/p2/app:1.0.0"); r.Exit != 0 {
			t.Errorf("podman pull of the private tag with p2's token: %s", r.Combined())
		}
		if r := p.Run("push", "--tls-verify=false", "--authfile", af, host+"/p1/app:1.0.0", host+"/p2/app:pushed"); r.Exit == 0 {
			t.Errorf("podman push succeeded on a pull-only registry")
		}
	})
}

func TestVersions(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.4.0", "1.4.2", "1.5.0", "2.0.0"}
	for i := 1; i < len(order); i++ {
		a, oka := registry.ParseVersion(order[i-1])
		b, okb := registry.ParseVersion(order[i])
		if !oka || !okb || a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Errorf("%s < %s failed", order[i-1], order[i])
		}
	}
	for _, bad := range []string{"", "1", "1.4", "01.0.0", "1.0.0-", "1.0.0-01", "v1.0.0", "1.0.0-a..b"} {
		if _, ok := registry.ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) ok", bad)
		}
	}
	match := func(c, v string) bool { x, _ := registry.ParseVersion(v); return registry.Matches(c, x) }
	for _, c := range []struct {
		c, v string
		ok   bool
	}{{"", "1.5.0", true}, {"", "1.6.0-rc.1", false}, {"1.4", "1.4.2", true}, {"1.4", "1.5.0", false}, {"1", "1.5.0", true},
		{"1.6.0-rc.1", "1.6.0-rc.1", true}, {"1.6", "1.6.0-rc.1", false}, {"1.4.0", "1.4.0", true}, {"1.4.0", "1.4.2", false}} {
		if got := match(c.c, c.v); got != c.ok {
			t.Errorf("Matches(%q, %s) = %v", c.c, c.v, got)
		}
	}
}
