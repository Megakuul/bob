package promote_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/promote"
)

// Regression tests of the I-R2 review (docs/review/m5-m18-reviewer.md): what a compromised
// worker that holds a job can stage, against real RustFS.

func (e *env) run() {
	e.t.Helper()
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) verdict(job, want, log string) {
	e.t.Helper()
	if pm := e.record(job).Promotion.Value(); pm != want {
		e.t.Errorf("%s: promotion = %q, want %q", job, pm, want)
	}
	if log != "" && !strings.Contains(e.logs.String(), log) {
		e.t.Errorf("%s: no log line with %q", job, log)
	}
}

func (e *env) code(p *path, token string) int {
	c, _, _ := e.get("/nix/"+p.hash+".narinfo", token, nil)
	return c
}

// TestPromoteBindsPathsToTheJob (F1): a staged path must be an output of the job's evaluation
// report or in the closure of those outputs; a job without its report promotes nothing.
func TestPromoteBindsPathsToTheJob(t *testing.T) {
	e := newEnv(t)
	a, b, c := chain()
	stray := newPath("bob-victim-lib", map[string]string{"lib.so": "attacker bytes\n"})
	e.outputs["j1"] = []*path{c}
	e.stage("j1", "p1", []*path{c, b, a, stray}, nil)

	a2, b2, c2 := chain()
	e.outputs["j2"] = []*path{c2}
	e.stage("j2", "p1", []*path{c2, b2, a2}, nil)

	x := newPath("bob-x", map[string]string{"x": "x\n"})
	j3 := e.stage("j3", "p1", []*path{x}, nil)
	if err := e.obj.Delete(e.ctx, j3.staging+"eval.json"); err != nil {
		t.Fatal(err)
	}
	e.run()
	e.verdict("j1", promote.Failed, "staged path "+stray.store()+" is neither an output of this job's packages nor in their closure")
	e.verdict("j2", promote.Done, "")
	e.verdict("j3", promote.Failed, "the evaluation report "+j3.staging+"eval.json is missing")
	for _, p := range []*path{stray, a, x} {
		if code := e.code(p, "p1-token"); code != http.StatusNotFound {
			t.Errorf("%s of a rejected job: narinfo HTTP %d, want 404", p.name, code)
		}
	}
}

// TestPromoteCrossProjectConflict (F1): bytes that differ from a path another project
// published never change it, never gain the staging project visibility and are never recorded
// as that project's non_reproducible, whichever project came first; the project's other paths
// are promoted.
func TestPromoteCrossProjectConflict(t *testing.T) {
	e := newEnv(t)
	// victim first: p1 publishes x; a p2 worker stages other bytes for x
	x := newPath("bob-victim-x", map[string]string{"x": "good\n"})
	e.stage("j1", "p1", []*path{x}, nil)
	e.run()
	first := e.narinfo(x)
	evil := &path{name: x.name, hash: x.hash, files: map[string]string{"x": "evil\n"}}
	e.stage("j2", "p2", []*path{evil}, nil)

	// attacker first: p2 publishes y; p1's honest build stages y and z (z references y)
	y := newPath("bob-victim-y", map[string]string{"y": "evil\n"})
	e.stage("j3", "p2", []*path{y}, nil)
	e.run()
	good := &path{name: y.name, hash: y.hash, files: map[string]string{"y": "good\n"}}
	z := newPath("bob-victim-z", map[string]string{"z": "uses @bob-victim-y\n"}, good)
	e.stage("j4", "p1", []*path{z, good}, nil)
	e.run()

	for _, job := range []string{"j2", "j4"} {
		r := e.record(job)
		if r.Promotion.Value() != promote.Done || len(r.NonReproducible.Value()) != 0 {
			t.Errorf("%s: pm %q nr %v; want done without non_reproducible (the path is another project's)", job, r.Promotion.Value(), r.NonReproducible.Value())
		}
	}
	if !strings.Contains(e.logs.String(), "security: path conflict") {
		t.Errorf("no security log line for the conflicts")
	}
	if again := e.narinfo(x); again.NarHash != first.NarHash || again.Sigs[0] != first.Sigs[0] {
		t.Errorf("the victim's publication of x changed")
	}
	for _, c := range []struct {
		p     *path
		token string
		want  int
	}{{x, "p1-token", 200}, {x, "p2-token", 404}, {y, "p2-token", 200}, {y, "p1-token", 404}, {z, "p1-token", 200}} {
		if got := e.code(c.p, c.token); got != c.want {
			t.Errorf("%s with %s: narinfo HTTP %d, want %d", c.p.name, c.token, got, c.want)
		}
	}
}

// TestPromoteUnknownHashCapFails (F4): a NAR with more unknown store hashes than the scan
// records fails verification instead of hiding a served reference behind them.
func TestPromoteUnknownHashCapFails(t *testing.T) {
	e := newEnv(t)
	served := newPath("bob-served", map[string]string{"s": "served\n"})
	e.stage("j0", "p1", []*path{served}, nil)
	e.run()
	var junk strings.Builder
	for i := 0; i < 300; i++ {
		junk.WriteString("/nix/store/" + newPath("junk", nil).hash + "-junk\n")
	}
	junk.WriteString(served.store() + "/s\n")
	hider := newPath("bob-hider", map[string]string{"data": junk.String()})
	e.stage("j1", "p1", []*path{hider}, func(*path) []*path { return nil })
	e.run()
	e.verdict("j1", promote.Failed, "too many to check")
	if code := e.code(hider, "p1-token"); code != http.StatusNotFound {
		t.Errorf("hider narinfo HTTP %d, want 404", code)
	}
}

// TestPromoteSkippedMustBeUpstream (F3): a skipped path counts as the job's closure only with
// a narinfo the upstream cache signed; a worker that lists another project's served path as
// skipped (unsigned, or signed by a key that is not the upstream's) cannot hide it in an
// output that discards references.
func TestPromoteSkippedMustBeUpstream(t *testing.T) {
	e := newEnv(t)
	foreign := newPath("secret-tool", map[string]string{"bin/api": "secret\n"})
	e.stage("j0", "p2", []*path{foreign}, nil)
	e.run()

	image := func(name string, root *path) (img, layer *path) {
		layer = newPath("oci-layer-1a2b3c4d-"+name, map[string]string{"layer.json": `{"paths":["` + name + `"]}` + "\n", "layer.tar.gz": "\x1f\x8b gz"})
		cfg := `{"config":{"Entrypoint":["@` + root.name + `/bin/api"]}}`
		img = newPath("oci-image-"+name+"-image-1.0.0", map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`,
			"index.json": `{"manifests":[]}`, "blobs/sha256/" + strings.Repeat("c", 64): cfg}, layer, root)
		return img, layer
	}
	onlyLayer := func(layer *path) func(*path) []*path {
		return func(p *path) []*path {
			if strings.HasPrefix(p.name, "oci-image-") {
				return []*path{layer}
			}
			return p.refs
		}
	}
	// j1: the foreign path listed as skipped, no upstream narinfo
	img1, layer1 := image("api1", foreign)
	e.noUpstream[foreign] = true
	e.stageKinds("j1", "p1", []*path{img1, layer1}, onlyLayer(layer1),
		map[*path]string{img1: promote.KindOCIImage, layer1: promote.KindOCIImage}, []*path{foreign})

	// j2: the same with an "upstream" narinfo the worker signed itself
	img2, layer2 := image("api2", foreign)
	j2 := e.stageKinds("j2", "p1", []*path{img2, layer2}, onlyLayer(layer2),
		map[*path]string{img2: promote.KindOCIImage, layer2: promote.KindOCIImage}, []*path{foreign})
	own, err := cache.GenerateKey("upstream-test-1")
	if err != nil {
		t.Fatal(err)
	}
	if foreign.nar == nil {
		foreign.nar = e.narOf(foreign)
	}
	info := cache.PathInfo{StorePath: foreign.store(), NarHash: narHash(foreign.nar), NarSize: uint64(len(foreign.nar))}
	e.put(j2.staging+"upstream/"+foreign.hash+".narinfo", []byte(fmt.Sprintf("StorePath: %s\nURL: nar/x.nar.xz\nCompression: xz\nNarHash: %s\nNarSize: %d\nReferences: \nSig: %s\n",
		foreign.store(), info.NarHash, info.NarSize, own.Sign(info))))

	// j3: a genuinely upstream path (signed by the upstream key) inside an AppImage passes
	glibc := newPath("glibc-2.42-100", nil)
	app := newPath("appimage-api-appimage", map[string]string{"api-aarch64.AppImage": appImage(t, glibc.store()+"/lib/ld-linux-aarch64.so.1")})
	e.stageKinds("j3", "p1", []*path{app}, func(*path) []*path { return nil }, map[*path]string{app: "appimage.bundle"}, []*path{glibc})

	e.run()
	foreignMsg := "the oci.image output contains the store hash " + foreign.hash + ", which is not in this job's closure"
	e.verdict("j1", promote.Failed, foreignMsg)
	e.verdict("j2", promote.Failed, foreignMsg)
	e.verdict("j3", promote.Done, "")
	for _, p := range []*path{img1, img2} {
		if code := e.code(p, "p1-token"); code != http.StatusNotFound {
			t.Errorf("%s narinfo HTTP %d, want 404", p.name, code)
		}
	}
}

// appImage is an AppImage-shaped file (ELF magic, AppImage type 2, squashfs) whose zstd
// squashfs holds s as a raw literal.
func appImage(t *testing.T, s string) string {
	t.Helper()
	var sq strings.Builder
	zw, err := zstd.NewWriter(&sq, zstd.WithNoEntropyCompression(true))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zw.Write([]byte(`{"interp":"` + s + `"}` + strings.Repeat("\x00", 64)))
	_ = zw.Close()
	if !strings.Contains(sq.String(), cache.HashOf(s)) {
		t.Fatalf("precondition: zstd did not keep the store hash as a raw literal")
	}
	return "\x7fELF\x02\x01\x01\x00AI\x02" + strings.Repeat("\x00", 53) + "hsqs" + sq.String()
}
