package registry_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/megakuul/bob/internal/gen/bob/registry/v1"
	"github.com/megakuul/bob/internal/registry"
)

// TestTagVisibilitySharedDigest: two projects build the same image, so their tags point at
// one digest and share one visibility document. p1's public release must not make p2's
// private release tag or p1's build tag visible to callers who cannot see the project
// (A151); manifest GET, tags/list, the token grant and RegistryService.ListTags agree.
func TestTagVisibilitySharedDigest(t *testing.T) {
	e := newEnv(t)
	im := newImage(t, "same fixture")
	if err := e.reg.PromoteImages(e.ctx, e.stage("p1", "b-01A", "p1/app", im)); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.PromoteImages(e.ctx, e.stage("p2", "b-01B", "p2/app", im)); err != nil {
		t.Fatal(err)
	}
	doc, err := e.vis.Get(e.ctx, strings.TrimPrefix(digestOf(im.index), "sha256:"))
	if err != nil || doc == nil || !slices.Equal(doc.Projects, []string{"p1", "p2"}) {
		t.Fatalf("shared visibility document = %+v, %v; want projects p1, p2", doc, err)
	}
	for _, b := range [][]byte{im.index, im.manifest, im.config, im.layer} {
		if err := e.vis.SetPublic(e.ctx, strings.TrimPrefix(digestOf(b), "sha256:"), "p1.0000000001", true); err != nil {
			t.Fatal(err)
		}
	}
	media := registry.MediaTypeOf(im.index)
	if err := e.reg.Repos.SetReleaseTag(e.ctx, "p1/app", "1.0.0", digestOf(im.index), media, "p1.0000000001", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Repos.SetReleaseTag(e.ctx, "p2/app", "1.0.0", digestOf(im.index), media, "p2.0000000001", 1); err != nil {
		t.Fatal(err)
	}

	tagsOf := func(repo, tok string) []string {
		t.Helper()
		code, _, body := e.get("/v2/"+repo+"/tags/list", tok)
		if code != 200 {
			return nil
		}
		var tl struct{ Tags []string }
		if err := json.Unmarshal(body, &tl); err != nil {
			t.Fatalf("tags/list %s: %s", repo, body)
		}
		return tl.Tags
	}
	missingLike := func(repo, tok, tag string) {
		t.Helper()
		c1, h1, b1 := e.get("/v2/"+repo+"/manifests/"+tag, tok)
		c2, h2, b2 := e.get("/v2/"+repo+"/manifests/no-such-tag", tok)
		if c1 == 200 || c1 != c2 || !bytes.Equal(b1, b2) || h1.Get("WWW-Authenticate") != h2.Get("WWW-Authenticate") {
			t.Errorf("%s:%s = %d %s, missing tag = %d %s; want the same refusal", repo, tag, c1, b1, c2, b2)
		}
	}

	// anonymous
	anon1, _ := e.token("p1/app", "")
	if code, hdr, body := e.get("/v2/p1/app/manifests/1.0.0", anon1); code != 200 || !bytes.Equal(body, im.index) ||
		!strings.HasPrefix(hdr.Get("Cache-Control"), "public") {
		t.Errorf("anonymous p1 release tag: %d %q", code, hdr.Get("Cache-Control"))
	}
	missingLike("p1/app", anon1, "b-01A") // p1's build tag: project only
	if got := tagsOf("p1/app", anon1); !slices.Equal(got, []string{"1.0.0", "latest"}) {
		t.Errorf("anonymous p1 tags = %v, want [1.0.0 latest]", got)
	}
	anon2, _ := e.token("p2/app", "")
	if registryCanPull(t, anon2, "p2/app") {
		t.Errorf("anonymous token was granted pull on p2/app")
	}
	missingLike("p2/app", anon2, "1.0.0")
	// a digest reference stays object-based: the public index is served anywhere
	if code, _, _ := e.get("/v2/p1/app/manifests/"+digestOf(im.index), anon1); code != 200 {
		t.Errorf("anonymous public digest: %d", code)
	}

	// a principal of p1 (another project for p2/app)
	p1on2, _ := e.token("p2/app", "tok-p1")
	if registryCanPull(t, p1on2, "p2/app") {
		t.Errorf("p1's token was granted pull on p2/app")
	}
	missingLike("p2/app", p1on2, "1.0.0")
	p1on1, _ := e.token("p1/app", "tok-p1")
	if got := tagsOf("p1/app", p1on1); !slices.Equal(got, []string{"1.0.0", "b-01A", "latest"}) {
		t.Errorf("p1 tags of p1/app = %v", got)
	}

	// the owner project p2
	mine, _ := e.token("p2/app", "tok-p2")
	if code, hdr, body := e.get("/v2/p2/app/manifests/1.0.0", mine); code != 200 || !bytes.Equal(body, im.index) ||
		!strings.HasPrefix(hdr.Get("Cache-Control"), "private") {
		t.Errorf("p2 private release tag: %d %q", code, hdr.Get("Cache-Control"))
	}
	if code, _, _ := e.get("/v2/p2/app/manifests/b-01B", mine); code != 200 {
		t.Errorf("p2 build tag: %d", code)
	}
	if got := tagsOf("p2/app", mine); !slices.Equal(got, []string{"1.0.0", "b-01B", "latest"}) {
		t.Errorf("p2 tags of p2/app = %v", got)
	}

	// RegistryService.ListTags (anonymous call): same rule
	svc := &registry.Service{R: e.reg, Scope: scoper{}}
	if _, err := svc.ListTags(e.ctx, connect.NewRequest(&registryv1.ListTagsRequest{Repository: "p2/app"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("anonymous ListTags p2/app: %v, want NotFound", err)
	}
	res, err := svc.ListTags(e.ctx, connect.NewRequest(&registryv1.ListTagsRequest{Repository: "p1/app"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range res.Msg.GetTags() {
		got = append(got, tg.GetTag())
		if !tg.GetPublic() {
			t.Errorf("anonymous ListTags p1/app: tag %s not public", tg.GetTag())
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"1.0.0", "latest"}) {
		t.Errorf("anonymous ListTags p1/app = %v", got)
	}
}

// registryCanPull reports whether the (unverified) payload of tok grants pull on repo.
func registryCanPull(t *testing.T, tok, repo string) bool {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has no payload", tok)
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Acc map[string][]string }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return slices.Contains(c.Acc[repo], "pull")
}
