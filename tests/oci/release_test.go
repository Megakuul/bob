package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

func TestRREG004Releases(t *testing.T) {
	parity.Covers(t, "R-REG-004")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	dir := harness.Fixture(t, "acme")
	e.Push(dir, "demo", "main")
	versions := []string{"1.4.0", "1.4.2", "1.5.0", "1.6.0-rc.1"}
	rels := map[string]regenv.Release{}
	for _, v := range versions {
		e.Tag(dir, "demo", "v"+v)
		rels[v] = e.WaitRelease("acme", "api", v, 20*time.Minute)
	}

	// release acme/api@1.4.2: closure roots per platform, an AppImage asset and an OCI tag
	rel := rels["1.4.2"]
	if len(rel.Closures) == 0 {
		t.Fatalf("release has no closures")
	}
	for _, c := range rel.Closures {
		if c.Platform == "" || len(c.Roots) == 0 {
			t.Errorf("closure %+v has no platform or no roots", c)
		}
	}
	if len(rel.Images) == 0 || rel.Images[0].Tag != "1.4.2" {
		t.Errorf("release images %+v, want an OCI tag 1.4.2", rel.Images)
	}
	var file, sum string
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.File, ".AppImage") {
			file, sum = a.File, a.Sha256
		}
	}
	if file == "" {
		t.Fatalf("release has no AppImage asset: %+v", rel.Assets)
	}

	// the public asset downloads anonymously and hashes to Asset.sha256, with the §6.8 headers
	e.SetVisibility(rel.ID, "PUBLIC")
	url := e.API + "/r/acme/api/1.4.2/" + file
	var code int
	var hdr http.Header
	var body []byte
	for deadline := time.Now().Add(35 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		if code, hdr, body = e.Get(url, "", nil); code == http.StatusOK || time.Now().After(deadline) {
			break
		}
	}
	if code != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, code)
	}
	h := sha256.Sum256(body)
	if got := hex.EncodeToString(h[:]); got != sum {
		t.Errorf("sha256 of the download %s, Asset.sha256 %s", got, sum)
	}
	if hdr.Get("ETag") != `"`+sum+`"` || hdr.Get("Content-Type") != "application/octet-stream" ||
		hdr.Get("Content-Disposition") != `attachment; filename="`+file+`"` ||
		hdr.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("download headers %v, want ETag, octet-stream, attachment and public immutable Cache-Control", hdr)
	}
	latest := e.API + "/r/acme/api/latest/" + file
	noRedirect := *e.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if r, err := noRedirect.Get(latest); err != nil || r.StatusCode != http.StatusFound || r.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("GET /latest/<file>: %v %v, want 302 with Cache-Control: no-cache", r, err)
	}

	// a second run of the same tag fails with the "already exists" message
	var rel1 regenv.Build
	for _, b := range e.Builds("acme") {
		for _, p := range b.Pipelines {
			if p.ReleaseID == rel.ID {
				rel1 = b
			}
		}
	}
	if rel1.ID == "" {
		t.Fatalf("no build published release %s", rel.ID)
	}
	var rerun struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/RerunBuild", map[string]any{"id": rel1.ID}, &rerun)
	b := e.WaitBuild(rerun.Build.ID, 10*time.Minute, regenv.Terminal)
	want := "release acme/api@1.4.2 already exists; releases are immutable"
	if b.State != "BUILD_STATE_FAILED" || len(b.Errors) == 0 || !strings.Contains(b.Errors[0].Message, want) {
		t.Errorf("rerun of the tag build: state %s errors %+v, want FAILED with %q", b.State, b.Errors, want)
	}

	// ResolveRelease with 1.4.0, 1.4.2, 1.5.0 and 1.6.0-rc.1 present
	platform := rel.Closures[0].Platform
	resolve := func(constraint string) (string, *regenv.RPCError) {
		var r struct{ Release regenv.Release }
		err := e.Call(e.Admin, "bob.release.v1.ReleaseService/ResolveRelease", map[string]any{
			"projectId": "acme", "name": "api", "constraint": constraint, "platform": platform}, &r)
		return r.Release.Version, err
	}
	for constraint, want := range map[string]string{"1.4": "1.4.2", "": "1.5.0", "1.6.0-rc.1": "1.6.0-rc.1", "1.4.0": "1.4.0"} {
		if got, err := resolve(constraint); err != nil || got != want {
			t.Errorf("ResolveRelease(%q) = %q, %v; want %s", constraint, got, err, want)
		}
	}
	if got, err := resolve("1.6"); err == nil {
		t.Errorf("ResolveRelease(\"1.6\") = %q; a pre-release resolves only by exact match", got)
	}
	e.AssertNoList()
}
