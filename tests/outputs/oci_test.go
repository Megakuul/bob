package outputs

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
)

const (
	mtIndex    = "application/vnd.oci.image.index.v1+json"
	mtManifest = "application/vnd.oci.image.manifest.v1+json"
	mtLayerGz  = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// ociWorld is the polyglot fixture plus the ocitest overlay images.
func ociWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, "polyglot")
	w.overlay(ociOverlay())
	return w
}

// pullImage serves the layout through a loopback registry and pulls the image with rootless
// podman (bobtest); it returns the client and the pulled reference.
func pullImage(t *testing.T, l layout) (*clients.Client, string) {
	t.Helper()
	host := serveLayout(t, l, "bobtest/img", "t")
	p := clients.Podman(t)
	p.InsecureRegistry(host)
	ref := host + "/bobtest/img:t"
	if r := p.Run("pull", "--tls-verify=false", ref); r.Exit != 0 {
		t.Fatalf("podman pull %s: exit %d\n%s%s", ref, r.Exit, r.Stdout, r.Stderr)
	}
	return p, ref
}

// runIn runs a command of the image (entrypoint replaced when entry is not empty).
func runIn(p *clients.Client, ref, entry string, env []string, mounts []string, args ...string) harness.Result {
	a := []string{"run", "--rm", "--network=host", "--cgroups=disabled"}
	if entry != "" {
		a = append(a, "--entrypoint", entry)
	}
	for _, e := range env {
		a = append(a, "-e", e)
	}
	for _, m := range mounts {
		a = append(a, "-v", m)
	}
	return p.Run(append(append(a, ref), args...)...)
}

// R-OUT-002: the image is an OCI layout with one image index over amd64 and arm64 (amd64 first),
// crane validates it, and rootless podman runs the host-platform image: golden line, uid 65534.
func TestROUT002ImageFromClosure(t *testing.T) {
	parity.Covers(t, "R-OUT-002")
	w := ociWorld(t)
	out, ok := w.buildOne("//ocitest:tools")
	if !ok {
		return
	}
	l := openLayout(t, out)
	idx, _ := l.imageIndex()
	if idx.MediaType != mtIndex {
		t.Errorf("index media type %q, want %q", idx.MediaType, mtIndex)
	}
	var plats []string
	for _, d := range idx.Manifests {
		if d.Platform == nil {
			t.Errorf("index entry %s has no platform", d.Digest)
			continue
		}
		plats = append(plats, d.Platform.OS+"/"+d.Platform.Architecture)
		if d.MediaType != mtManifest {
			t.Errorf("manifest %s has media type %q, want %q", d.Digest, d.MediaType, mtManifest)
		}
	}
	if got, want := fmt.Sprint(plats), "[linux/amd64 linux/arm64]"; got != want {
		t.Errorf("platforms %s, want %s (amd64 first)", got, want)
	}

	host := serveLayout(t, l, "bobtest/img", "t")
	crane := pinnedBin(t, "crane", "crane")
	for _, plat := range []string{"linux/amd64", "linux/arm64"} {
		r := run(t, 5*time.Minute, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, crane, "validate", "--remote", host+"/bobtest/img:t", "--insecure", "--platform", plat)
		if r.Exit != 0 || !strings.Contains(r.Combined(), "PASS") {
			t.Errorf("crane validate --remote (%s): exit %d\n%s", plat, r.Exit, r.Combined())
		}
	}

	p, ref := pullImage(t, l)
	if r := runIn(p, ref, "", nil, nil); r.Exit != 0 || !strings.Contains(r.Stdout, apiLine) {
		t.Errorf("podman run printed (exit %d) %q%s, want the golden line", r.Exit, r.Stdout, r.Stderr)
	}
	r := runIn(p, ref, "id", nil, nil)
	if got := strings.TrimSpace(r.Stdout); r.Exit != 0 || !strings.HasPrefix(got, "uid=65534(app) gid=65534(app)") {
		t.Errorf("id printed (exit %d) %q%s, want uid=65534(app) gid=65534(app)", r.Exit, got, r.Stderr)
	}
}

// api42 replaces one string of the api source, as the "v2" of R-OUT-009.
func (w *world) changeAPI() {
	w.t.Helper()
	p := filepath.Join(w.dir, "cmd", "api", "main.go")
	src := string(readFile(w.t, p))
	if !strings.Contains(src, `"hello "`) {
		w.t.Fatalf("harness: fixture api no longer contains \"hello \"")
	}
	w.overlay(map[string]string{"cmd/api/main.go": strings.Replace(src, `"hello "`, `"hullo "`, 1)})
}

// R-OUT-009: layer algorithm. The fixture api image has 14 layers; changing one string of api
// changes exactly one layer blob (plus config and manifest) and ≤ 16 % of the image bytes;
// MaxLayers 4 gives 4 layers with the root alone in layer 3.
func TestROUT009LayerAlgorithm(t *testing.T) {
	parity.Covers(t, "R-OUT-009")
	w := ociWorld(t)
	api, ok := w.buildOne("//cmd/api:api")
	if !ok {
		return
	}
	out1, ok := w.buildOne("//deploy:api-image")
	if !ok {
		return
	}
	arch := ociArch(hostPlatform(t))
	l1 := openLayout(t, out1)
	m1, _, c1 := l1.manifestFor(arch)
	if len(m1.Layers) != 14 {
		t.Errorf("fixture api image has %d layers, want 14", len(m1.Layers))
	}
	if len(c1.RootFS.DiffIDs) != len(m1.Layers) {
		t.Errorf("config has %d diff_ids for %d layers", len(c1.RootFS.DiffIDs), len(m1.Layers))
	}
	n := len(m1.Layers)
	if n >= 3 {
		// order: one layer per popular path, then the root, then the customisation layer
		if tops := storeTops(l1.layerEntries(m1, 0)); len(tops) != 1 || !strings.Contains(tops[0], "-glibc-") {
			t.Errorf("layer 0 holds %v, want glibc alone (most popular path)", tops)
		}
		if tops := storeTops(l1.layerEntries(m1, n-2)); len(tops) != 1 || tops[0] != filepath.Base(api) {
			t.Errorf("root layer holds %v, want %s alone", tops, filepath.Base(api))
		}
		if _, ok := tarFind(l1.layerEntries(m1, n-1), "etc/passwd"); !ok {
			t.Errorf("last layer is not the customisation layer (no etc/passwd)")
		}
		for i := 0; i < n-2; i++ {
			if tops := storeTops(l1.layerEntries(m1, i)); len(tops) != 1 {
				t.Errorf("layer %d holds %d store paths, want one path per layer below the layer limit", i, len(tops))
			}
		}
	}

	w.changeAPI()
	out2, ok := w.buildOne("//deploy:api-image")
	if !ok {
		return
	}
	l2 := openLayout(t, out2)
	m2, d2, _ := l2.manifestFor(arch)
	_, d1, _ := l1.manifestFor(arch)
	if d1.Digest == d2.Digest {
		t.Errorf("v2 has the same manifest as v1")
	}
	if len(m2.Layers) != len(m1.Layers) {
		t.Fatalf("v2 has %d layers, v1 %d", len(m2.Layers), len(m1.Layers))
	}
	old := map[string]bool{}
	var total, fresh int64
	for _, d := range m1.Layers {
		old[d.Digest] = true
	}
	changed := 0
	for _, d := range m2.Layers {
		total += d.Size
		if !old[d.Digest] {
			changed++
			fresh += d.Size
		}
	}
	if changed != 1 {
		t.Errorf("v2 differs from v1 in %d layer blobs, want exactly 1", changed)
	}
	if total > 0 && float64(fresh) > 0.16*float64(total) {
		t.Errorf("v2 adds %d of %d compressed bytes (%.1f %%), want ≤ 16 %%", fresh, total, 100*float64(fresh)/float64(total))
	}

	four, ok := w.buildOne("//ocitest:four")
	if !ok {
		return
	}
	l4 := openLayout(t, four)
	m4, _, _ := l4.manifestFor(arch)
	if len(m4.Layers) != 4 {
		t.Fatalf("MaxLayers 4 gave %d layers", len(m4.Layers))
	}
	if tops := storeTops(l4.layerEntries(m4, 2)); len(tops) != 1 || tops[0] != filepath.Base(api) {
		t.Errorf("layer 3 holds %v, want the root alone", tops)
	}
	if tops := storeTops(l4.layerEntries(m4, 1)); len(tops) < 2 {
		t.Errorf("layer 2 holds %v, want the shared layer of all remaining paths", tops)
	}
}

// R-OUT-010: reproducibility. Two fresh stores give the identical index digest; tar, gzip and
// config follow the byte rules; golden: the index digest of the frozen fixture.
func TestROUT010Reproducibility(t *testing.T) {
	parity.Covers(t, "R-OUT-010")
	w := newWorld(t, "polyglot")
	out1, ok := w.buildOne("//deploy:api-image")
	if !ok {
		return
	}
	out2, ok := w.freshStore().buildOne("//deploy:api-image")
	if !ok {
		return
	}
	l1, l2 := openLayout(t, out1), openLayout(t, out2)
	_, d1 := l1.imageIndex()
	_, d2 := l2.imageIndex()
	if d1.Digest != d2.Digest {
		t.Errorf("two builds in fresh stores give index digests %s and %s", d1.Digest, d2.Digest)
	}
	golden.Assert(t, "tests/goldens/outputs/oci-index-digest.txt", []byte(d1.Digest+"\n"), golden.Reqs("R-OUT-010"))

	for _, arch := range []string{"amd64", "arm64"} {
		m, _, _ := l1.manifestFor(arch)
		raw := decode[map[string]any](t, l1.blob(l1.configDigest(arch)))
		if _, has := raw["created"]; has {
			t.Errorf("%s config has a created field", arch)
		}
		_, _, cfg := l1.manifestFor(arch)
		for i, h := range cfg.History {
			if h.Created != "1970-01-01T00:00:01Z" {
				t.Errorf("%s history[%d].created = %q, want 1970-01-01T00:00:01Z", arch, i, h.Created)
			}
		}
		for i, ld := range m.Layers {
			if ld.MediaType != mtLayerGz {
				t.Errorf("%s layer %d media type %q", arch, i, ld.MediaType)
			}
			b := l1.blob(ld.Digest)
			what := fmt.Sprintf("%s layer %d", arch, i)
			gzipHeaderOK(t, what, b)
			es := readTar(t, b)
			checkTarRules(t, what, es)
			for _, dir := range []string{"nix", "nix/store"} {
				e, ok := tarFind(es, dir)
				if !ok || e.Hdr.Typeflag != '5' || e.Hdr.Mode&0o7777 != 0o555 || e.Hdr.Uid != 0 {
					t.Errorf("%s: directory %s is %+v (found %v), want mode 0555 uid 0", what, dir, e.Hdr, ok)
				}
			}
		}
	}
}

// configDigest returns the config blob digest of the manifest for arch.
func (l layout) configDigest(arch string) string {
	m, _, _ := l.manifestFor(arch)
	return m.Config.Digest
}

// R-OUT-011: one derivation per layer (oci-layer-<hash8>-<name>) with structured attrs and
// unsafeDiscardReferences; layer outputs reference nothing; the image output's closure is the
// image and its layers; skopeo follows the layer symlinks; changing only api rebuilds one layer
// derivation plus the image derivation.
func TestROUT011ImageLayoutInTheStore(t *testing.T) {
	parity.Covers(t, "R-OUT-011")
	w := newWorld(t, "polyglot")
	out, ok := w.buildOne("//deploy:api-image")
	if !ok {
		return
	}
	all, ok := w.bundleDrvs()
	if !ok {
		return
	}
	layers := bundleDrvsOf(all, "oci-layer")
	if len(layers) == 0 {
		t.Fatalf("no oci-layer derivation")
	}
	for _, d := range layers {
		if !strings.HasPrefix(d.D.Name, "oci-layer-") || len(d.D.Name) < len("oci-layer-")+9 || d.D.Name[len("oci-layer-")+8] != '-' {
			t.Errorf("layer derivation %q is not named oci-layer-<storehash8>-<name>", d.D.Name)
		}
		udr, _ := d.D.Structured["unsafeDiscardReferences"].(map[string]any)
		if udr["out"] != true {
			t.Errorf("%s: structured attrs unsafeDiscardReferences.out = %v, want true", d.D.Name, d.D.Structured["unsafeDiscardReferences"])
		}
		if len(d.D.Outputs) == 0 || d.D.Outputs[0].Name != "out" {
			t.Errorf("%s: outputs %v", d.D.Name, d.D.Outputs)
			continue
		}
		path := filepath.Join(w.home, "nix", "store", filepath.Base(d.D.Outputs[0].Path))
		lj := decode[struct {
			DiffID string   `json:"diffID"`
			Digest string   `json:"digest"`
			Size   int64    `json:"size"`
			Paths  []string `json:"paths"`
		}](t, readFile(t, filepath.Join(path, "layer.json")))
		if lj.DiffID == "" || lj.Digest == "" || lj.Size <= 0 || len(lj.Paths) == 0 {
			t.Errorf("%s: layer.json incomplete: %+v", d.D.Name, lj)
		}
		for _, ref := range nixRefs(t, w.home, d.D.Outputs[0].Path) {
			if ref != d.D.Outputs[0].Path {
				t.Errorf("%s: layer output references %s; nix-store -q --references must be empty", d.D.Name, ref)
			}
		}
	}
	closure := nixQuery(t, w.home, "-r", storePathOf(out))
	n := 0
	for _, line := range strings.Fields(closure) {
		n++
		if strings.Contains(line, "-glibc-") && !strings.Contains(line, "oci-layer") {
			t.Errorf("image closure contains %s; it must hold the image and its layers only", line)
		}
	}
	if n != 1+len(layers) {
		t.Errorf("image closure has %d paths, want the image plus its %d layers", n, len(layers))
	}

	skopeo := pinnedBin(t, "skopeo", "skopeo")
	dst := t.TempDir()
	r := run(t, 5*time.Minute, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, skopeo, "copy", "--all", "oci:"+out, "oci:"+dst+"/copy")
	if r.Exit != 0 {
		t.Errorf("skopeo copy --all oci:<out> oci:<copy>: exit %d\n%s", r.Exit, r.Combined())
	}
	if _, d := openLayout(t, filepath.Join(dst, "copy")).imageIndex(); d.Digest != func() string { _, o := openLayout(t, out).imageIndex(); return o.Digest }() {
		t.Errorf("skopeo copy changed the index digest")
	}

	// Changing only api rebuilds exactly one layer derivation plus the image derivation.
	before := map[string]bool{}
	for _, d := range all {
		before[d.File] = true
	}
	w.changeAPI()
	if _, ok := w.buildOne("//deploy:api-image"); !ok {
		return
	}
	after, ok := w.bundleDrvs()
	if !ok {
		return
	}
	newLayer, newImage := 0, 0
	for _, d := range after {
		if before[d.File] {
			continue
		}
		switch d.Cmd {
		case "oci-layer":
			newLayer++
		case "oci-image":
			newImage++
		}
	}
	if newLayer != 1 || newImage != 1 {
		t.Errorf("changing only api created %d layer and %d image derivations, want 1 and 1", newLayer, newImage)
	}
}

// nixQuery runs `nix path-info --store <home> <args>` with the pinned nix and returns stdout.
func nixQuery(t *testing.T, home string, args ...string) string {
	t.Helper()
	nix := pinnedBin(t, "nix", "nix")
	r := run(t, 5*time.Minute, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}, nix,
		append([]string{"--extra-experimental-features", "nix-command", "--store", home, "path-info"}, args...)...)
	if r.Exit != 0 {
		t.Fatalf("nix path-info %v: exit %d\n%s", args, r.Exit, r.Combined())
	}
	return r.Stdout
}

// nixRefs lists the references of a store path of the bob home (nix path-info --json).
func nixRefs(t *testing.T, home, path string) []string {
	t.Helper()
	var info map[string]struct {
		References []string `json:"references"`
	}
	raw := nixQuery(t, home, "--json", path)
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("nix path-info --json: %v\n%s", err, raw)
	}
	for _, v := range info {
		return v.References
	}
	t.Fatalf("nix path-info --json listed no path: %s", raw)
	return nil
}

// R-OUT-012: the customisation layer and config defaults, and inside rootless podman an HTTPS
// GET to a TLS server (its certificate becomes the image's CA bundle) and a time-zone lookup.
func TestROUT012CustomisationLayer(t *testing.T) {
	parity.Covers(t, "R-OUT-012")
	w := ociWorld(t)
	api, ok := w.buildOne("//cmd/api:api")
	if !ok {
		return
	}
	out, ok := w.buildOne("//ocitest:tools")
	if !ok {
		return
	}
	l := openLayout(t, out)
	arch := ociArch(hostPlatform(t))
	m, _, cfg := l.manifestFor(arch)
	es := l.layerEntries(m, len(m.Layers)-1)
	if e, ok := tarFind(es, "tmp"); !ok || e.Hdr.Mode&0o7777 != 0o1777 {
		t.Errorf("/tmp is %+v (found %v), want mode 1777", e.Hdr, ok)
	}
	wantFiles := map[string]string{
		"etc/passwd":        "root:x:0:0::/root:/sbin/nologin\napp:x:65534:65534::/tmp:/sbin/nologin\n",
		"etc/group":         "root:x:0:\napp:x:65534:\n",
		"etc/nsswitch.conf": "hosts: files dns",
	}
	for name, want := range wantFiles {
		e, ok := tarFind(es, name)
		if !ok {
			t.Errorf("customisation layer has no %s", name)
			continue
		}
		if got := string(e.Data); strings.TrimSpace(got) != strings.TrimSpace(want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var certTarget string
	for _, name := range []string{"etc/ssl/certs/ca-certificates.crt", "etc/ssl/certs/ca-bundle.crt"} {
		e, ok := tarFind(es, name)
		if !ok || e.Hdr.Typeflag != '2' || !strings.Contains(e.Hdr.Linkname, "-nss-cacert") {
			t.Errorf("%s is %+v (found %v), want a symlink into nss-cacert", name, e.Hdr, ok)
			continue
		}
		certTarget = e.Hdr.Linkname
	}
	if e, ok := tarFind(es, "usr/share/zoneinfo"); !ok || e.Hdr.Typeflag != '2' || !strings.Contains(e.Hdr.Linkname, "-tzdata") {
		t.Errorf("/usr/share/zoneinfo is %+v (found %v), want a symlink into tzdata", e.Hdr, ok)
	}
	if _, ok := tarFind(es, ".bob/licences.json"); !ok {
		t.Errorf("customisation layer has no /.bob/licences.json")
	}
	env := map[string]string{}
	for _, kv := range cfg.Config.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["SSL_CERT_FILE"] != "/etc/ssl/certs/ca-certificates.crt" || env["TZDIR"] != "/usr/share/zoneinfo" {
		t.Errorf("env SSL_CERT_FILE=%q TZDIR=%q", env["SSL_CERT_FILE"], env["TZDIR"])
	}
	if !strings.HasPrefix(env["PATH"], "/nix/store/"+filepath.Base(api)+"/bin:") || !strings.HasSuffix(env["PATH"], ":/usr/bin:/bin") {
		t.Errorf("PATH=%q, want <roots' bin dirs>:/usr/bin:/bin starting with the api's bin", env["PATH"])
	}

	// The TLS server's certificate stands in for the image's CA bundle (bind mount over the
	// symlink target), so the whole chain symlink → SSL_CERT_FILE → client is exercised.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	p, ref := pullImage(t, l)
	if certTarget == "" {
		return
	}
	r := runIn(p, ref, "curl", nil, []string{ca + ":" + certTarget + ":ro"}, "-sS", "-o", "/dev/null", "-w", "%{http_code}", srv.URL)
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "200" {
		t.Errorf("HTTPS GET inside the image: exit %d, status %q\n%s", r.Exit, r.Stdout, r.Stderr)
	}
	r = runIn(p, ref, "date", []string{"TZ=Europe/Zurich"}, nil, "-d", "@0")
	if got := strings.TrimSpace(r.Stdout); r.Exit != 0 || got != "Thu Jan  1 01:00:00 CET 1970" {
		t.Errorf("TZ=Europe/Zurich date -d @0 = %q (exit %d), want Thu Jan  1 01:00:00 CET 1970\n%s", got, r.Exit, r.Stderr)
	}
}

// R-OUT-013: the release image of the fixture is pullable from the server with a
// service-account token and its manifest digest equals the local index digest.
func TestROUT013ImagesInReleasesAndRegistry(t *testing.T) {
	parity.Covers(t, "R-OUT-013")
	url, token, ok := serverURLs(t)
	if !ok {
		return
	}
	w := newWorld(t, "polyglot")
	login := harness.Run(t, "bob", harness.Spec{Dir: w.dir, Env: w.env(), Stdin: token + "\n"}, "login", "--token-stdin", url)
	if harness.Red(t, login) {
		return
	}
	if login.Exit != 0 {
		t.Fatalf("bob login: exit %d\n%s", login.Exit, login.Combined())
	}
	rel, ok := w.buildOne("//deploy:api", "--version", "1.0.0")
	if !ok {
		return
	}
	man := readManifest(t, rel)
	var img *artifact
	for i := range man.Artifacts {
		if man.Artifacts[i].Kind == "oci" {
			img = &man.Artifacts[i]
		}
	}
	if img == nil || len(img.Tags) == 0 {
		t.Fatalf("release manifest has no oci artifact with tags: %+v", man.Artifacts)
	}
	if r := w.bob("build", "--remote", "--version", "1.0.0", "//deploy:api"); !exitIs(t, r, 0) {
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	ref := host + "/" + img.Repo + ":" + img.Tags[0]
	p := clients.Podman(t)
	p.InsecureRegistry(host)
	if r := p.Run("login", "--tls-verify=false", "-u", "bob", "-p", token, host); r.Exit != 0 {
		t.Fatalf("podman login: exit %d\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	if r := p.Run("pull", "--tls-verify=false", ref); r.Exit != 0 {
		t.Errorf("podman pull %s: exit %d\n%s%s", ref, r.Exit, r.Stdout, r.Stderr)
	}
	crane := pinnedBin(t, "crane", "crane")
	dockerCfg := t.TempDir()
	auth := fmt.Sprintf(`{"auths":{%q:{"username":"bob","password":%q}}}`, host, token)
	if err := os.WriteFile(filepath.Join(dockerCfg, "config.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, 5*time.Minute, []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "DOCKER_CONFIG=" + dockerCfg}, crane, "manifest", "--insecure", ref)
	if r.Exit != 0 {
		t.Fatalf("crane manifest %s: exit %d\n%s", ref, r.Exit, r.Stderr)
	}
	if got := digestOf([]byte(r.Stdout)); got != img.Index {
		t.Errorf("registry index digest %s, local build %s", got, img.Index)
	}
}
