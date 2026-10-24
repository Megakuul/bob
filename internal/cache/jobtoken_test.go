package cache_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/auth"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// TestJobDownloadTokenScope: a job's download token (GetCheckoutCredentialResponse.
// download_token) substitutes the private paths of its own project through the real /nix/
// handler and nothing of another project's; it has no permission anywhere else; revoked, it
// is refused (401); its handle cannot revoke a token that is not a job's.
func TestJobDownloadTokenScope(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	t.Setenv("AWS_ACCESS_KEY_ID", rustfs.RootAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", rustfs.RootSecretKey)
	t.Setenv("AWS_REGION", "us-east-1")
	control, err := store.Open(ctx, store.BucketConfig{Endpoint: s.URL, Region: "us-east-1", Name: "bob-control", Role: "bob-control"})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := cache.OpenObjects(ctx, s3client.Options{Endpoint: s.URL, Region: "us-east-1"}, "bob-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	authz, err := rbac.New(control)
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.New(auth.Config{Control: control, RBAC: authz})
	if err != nil {
		t.Fatal(err)
	}
	vis := cache.NewVisStore(obj, cache.NixVisPrefix, nil)
	h := cache.NewHandler(cache.Config{Objects: obj, Vis: vis, Principal: id.HTTPPrincipal,
		Allowed: func(ctx context.Context, p *api.Principal, projects []string) (bool, error) {
			sc, err := authz.Scope(ctx, p, cache.GetPathMethod)
			if err != nil {
				return false, err
			}
			for _, pr := range projects {
				if sc.Contains(pr) {
					return true, nil
				}
			}
			return false, nil
		}})
	srv := httptest.NewServer(h)
	defer srv.Close()

	own, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for hash, project := range map[string]string{own: "p1", other: "p2"} {
		if _, err := obj.Put(ctx, cache.NarInfoKey(hash), []byte("StorePath: /nix/store/"+hash+"-x\n"), "text/x-nix-narinfo", store.Cond{}); err != nil {
			t.Fatal(err)
		}
		if err := vis.AddProject(ctx, hash, project); err != nil {
			t.Fatal(err)
		}
	}

	tok, handle, err := id.IssueJobDownload(ctx, "b-1.j0001", "p1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, auth.PrefixDownload) || handle == "" || strings.Contains(handle, tok) {
		t.Fatalf("token %q handle %q: want a bob_dt_ token and a handle that is not the token", tok, handle)
	}
	get := func(hash, password string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/nix/"+hash+".narinfo", nil)
		if password != "" {
			req.SetBasicAuth("bob", password)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := get(own, tok); c != http.StatusOK {
		t.Errorf("own project's private path with the job token: %d, want 200", c)
	}
	if c := get(other, tok); c != http.StatusNotFound {
		t.Errorf("another project's private path with the job token: %d, want 404", c)
	}
	if c := get(own, ""); c != http.StatusNotFound {
		t.Errorf("private path without a token: %d, want 404", c)
	}

	// No permission anywhere: no bindings, download scope (the API refuses it), no RBAC access.
	p, err := id.Authenticate(ctx, api.Credential{Bearer: tok})
	if err != nil {
		t.Fatal(err)
	}
	if p.Scope != "download" || len(p.Bindings) != 0 || p.CacheProject != "p1" {
		t.Errorf("job principal %+v: want scope download, no bindings, cache project p1", p)
	}
	for _, project := range []string{"p1", "p2", "*"} {
		if acc, err := authz.Access(ctx, p, cache.GetPathMethod, project); err != nil || acc == api.Allowed {
			t.Errorf("RBAC access of the job principal on %s: %v %v, want none", project, acc, err)
		}
	}

	// A handle names only job tokens.
	user := store.TokenKey(auth.PrefixDownload + "usertoken")
	user.Owner, user.Kind, user.Scope = store.Set[string]("user:u1"), store.Set[string]("dt"), store.Set[string]("download")
	if err := store.Create(ctx, control, user); err != nil {
		t.Fatal(err)
	}
	if err := id.RevokeJobDownload(ctx, store.TokenKey(auth.PrefixDownload+"usertoken").Hash.Value()); err == nil {
		t.Errorf("RevokeJobDownload deleted a user's token")
	}

	if err := id.RevokeJobDownload(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if c := get(own, tok); c != http.StatusUnauthorized {
		t.Errorf("revoked job token: %d, want 401", c)
	}
	if err := id.RevokeJobDownload(ctx, handle); err != nil {
		t.Errorf("revoking twice: %v, want nil (absent is fine)", err)
	}
}
