package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/output/oci/bop"
	"github.com/megakuul/bob/pkg/output/oci/internal/ocispec"
)

const (
	builderPath = "/nix/store/0000000000000000000000000000000b-bob-builder"
	rootPath    = "/nix/store/rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr-app"
	aPath       = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-liba-1"
	bPath       = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-libb-1"
	cPath       = "/nix/store/cccccccccccccccccccccccccccccccc-glibc-2.42"
	caPath      = "/nix/store/dddddddddddddddddddddddddddddddd-nss-cacert-3"
	tzPath      = "/nix/store/ffffffffffffffffffffffffffffffff-tzdata-1"
)

// fakeStore is engine.PlanStore and engine.PlanCloser over a directory and a fixed closure.
type fakeStore struct {
	dir  string
	refs map[string][]string
}

func (s fakeStore) HostPath(p string) string { return filepath.Join(s.dir, filepath.Base(p)) }
func (s fakeStore) ImportSources(context.Context, []srcs.Source) ([]string, error) {
	return nil, nil
}
func (s fakeStore) Ensure(context.Context, fetch.Request) (string, error) { return "", nil }
func (s fakeStore) Closure(_ context.Context, outs []engine.PlanOutput) (map[string][]string, error) {
	var roots []string
	for _, o := range outs {
		roots = append(roots, o.Path)
	}
	res := map[string][]string{}
	for _, p := range engine.ClosureOf(s.refs, roots...) {
		res[p] = s.refs[p]
	}
	return res, nil
}

func request(t *testing.T, spec ocispec.Image) (engine.PlanRequest, *ir.Node) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Base(rootPath), "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	img := &ir.Node{ID: "img", Kind: ocispec.KindImage, Label: "//deploy:img", Spec: raw, Deps: []string{"app", "ca", "tz"}}
	app := &ir.Node{ID: "app", Kind: "go.binary", Label: "//cmd/app:app"}
	ca := &ir.Node{ID: "ca", Kind: "nixpkgs.pkg", Label: "nixpkgs:cacert", Nix: &ir.Nix{Attr: "cacert", Outputs: []string{"out"}}}
	tz := &ir.Node{ID: "tz", Kind: "nixpkgs.pkg", Label: "nixpkgs:tzdata", Nix: &ir.Nix{Attr: "tzdata", Outputs: []string{"out"}}}
	req := engine.PlanRequest{
		Graph: &ir.Graph{Nodes: []*ir.Node{img, app, ca, tz}},
		Nodes: []*ir.Node{img},
		Inputs: map[string]engine.Resolved{
			"app": {Outputs: map[string]string{"out": rootPath}, MainProgram: "bin/app"},
			"ca":  {Outputs: map[string]string{"out": caPath}},
			"tz":  {Outputs: map[string]string{"out": tzPath}},
		},
		Builder: builderPath,
		Store: fakeStore{dir: dir, refs: map[string][]string{
			rootPath: {aPath, bPath, cPath}, aPath: {cPath}, bPath: {cPath}, cPath: nil, caPath: nil, tzPath: nil,
		}},
	}
	return req, img
}

func spec(maxLayers int) ocispec.Image {
	return ocispec.Image{Name: "img", Platforms: []ocispec.Platform{{Platform: "linux-arm64", Program: 0, Extra: []int{}}},
		Ports: []int{8080}, MaxLayers: maxLayers, Prune: []string{}, CACert: 1, TZData: 2}
}

// decodeStrict decodes v strictly (unknown fields fail): the generator's jobs must be exactly
// what bob-builder reads.
func decodeStrict(t *testing.T, v any, into any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("job does not decode into %T: %v\n%s", into, err, b)
	}
}

func jobArgs(d *drv.Derivation) any {
	spec, _ := d.Structured["bobSpec"].(map[string]any)
	return spec["args"]
}

// R-OUT-009/R-OUT-011: MaxLayers 4 with one root: the most popular path alone, the rest shared,
// the root alone; each layer its own derivation with unsafeDiscardReferences and only its
// paths as inputs; the image derivation depends on the layers only.
func TestPlanImageLayers(t *testing.T) {
	req, img := request(t, spec(4))
	jobs := engine.NewJobs(req)
	rv, err := PlanImage(context.Background(), req, jobs, img, nil)
	if err != nil {
		t.Fatal(err)
	}
	var layers []*drv.Derivation
	var image *drv.Derivation
	for _, d := range jobs.Drvs {
		switch d.Args[0] {
		case "oci-layer":
			layers = append(layers, d)
		case "oci-image":
			image = d
		}
	}
	if image == nil || len(layers) != 3 {
		t.Fatalf("planned %d layers and image %v", len(layers), image)
	}
	var groups [][]string
	for _, d := range layers {
		var a bop.LayerArgs
		decodeStrict(t, jobArgs(d), &a)
		groups = append(groups, a.Paths)
		if !strings.HasPrefix(d.Name, "oci-layer-") || d.Name[len("oci-layer-")+8] != '-' {
			t.Errorf("layer name %q", d.Name)
		}
		if udr, _ := d.Structured["unsafeDiscardReferences"].(map[string]any); udr["out"] != true {
			t.Errorf("%s: unsafeDiscardReferences %v", d.Name, d.Structured["unsafeDiscardReferences"])
		}
		if len(d.InputDrvs) != 0 || !slices.Equal(d.InputSrcs, slices.Sorted(slices.Values(append(slices.Clone(a.Paths), builderPath)))) {
			t.Errorf("%s: inputs %v %v", d.Name, d.InputDrvs, d.InputSrcs)
		}
	}
	want := [][]string{{cPath}, {aPath, bPath, caPath, tzPath}, {rootPath}}
	if !slices.EqualFunc(groups, want, slices.Equal) {
		t.Errorf("layers %v, want %v", groups, want)
	}
	var a bop.ImageArgs
	decodeStrict(t, jobArgs(image), &a)
	if len(a.Manifests) != 1 || len(a.Manifests[0].Layers) != 3 || a.Tag != "" {
		t.Fatalf("image args %+v", a)
	}
	c := a.Manifests[0].Config
	if c.User != "65534:65534" || c.WorkingDir != "/" || !slices.Equal(c.Entrypoint, []string{rootPath + "/bin/app"}) ||
		!slices.Equal(c.Env, []string{"PATH=" + rootPath + "/bin:/usr/bin:/bin", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt", "TZDIR=/usr/share/zoneinfo"}) ||
		!slices.Equal(c.ExposedPorts, []string{"8080/tcp"}) || c.Labels["org.opencontainers.image.licenses"] != "NOASSERTION" {
		t.Errorf("config %+v", c)
	}
	if a.Manifests[0].Custom.CACert != caPath+"/etc/ssl/certs/ca-bundle.crt" || a.Manifests[0].Custom.ZoneInfo != tzPath+"/share/zoneinfo" {
		t.Errorf("custom %+v", a.Manifests[0].Custom)
	}
	if len(image.InputSrcs) != 1 || len(image.InputDrvs) != 3 {
		t.Errorf("image inputs: drvs %v srcs %v (want the layers and bob-builder only)", image.InputDrvs, image.InputSrcs)
	}
	if rv.Drv == "" || rv.Outputs["out"] == "" {
		t.Errorf("resolved %+v", rv)
	}

	// A release's image: same layers, its own derivation with tag and version label.
	rel, err := PlanImage(context.Background(), req, jobs, img, &Release{Version: "1.0.1", Tags: []string{"1.0.1"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Drv == rv.Drv || len(jobs.Drvs) != 5 {
		t.Fatalf("release image %s (plain %s), %d derivations: want one new image derivation and no new layer", rel.Drv, rv.Drv, len(jobs.Drvs))
	}
	var ra bop.ImageArgs
	decodeStrict(t, jobArgs(jobs.Drvs[4]), &ra)
	if ra.Tag != "1.0.1" || ra.Manifests[0].Config.Labels["org.opencontainers.image.version"] != "1.0.1" ||
		!slices.Equal(ra.Manifests[0].Layers, a.Manifests[0].Layers) {
		t.Errorf("release image args %+v", ra)
	}
}

func TestLayerGroupsLimits(t *testing.T) {
	refs := map[string][]string{rootPath: {aPath}, aPath: nil}
	if _, err := layerGroups(refs, []string{aPath, rootPath}, []string{rootPath}, 126); err == nil {
		t.Errorf("MaxLayers 126 accepted")
	}
	if _, err := layerGroups(refs, []string{aPath, rootPath}, []string{rootPath, aPath}, 3); err == nil ||
		err.Error() != "2 roots need MaxLayers ≥ 4" {
		t.Errorf("too many roots: %v", err)
	}
	g, err := layerGroups(refs, []string{aPath, rootPath}, []string{rootPath}, 100)
	if err != nil || len(g) != 2 {
		t.Errorf("groups %v %v", g, err)
	}
}
