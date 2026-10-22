package gen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/output/oci/internal/ocispec"
)

// Defaults of ImageOpts (SPEC §04.5).
const (
	defaultMaxLayers = 100
	defaultUser      = "65534:65534"
	defaultWorkDir   = "/"
	caBundle         = "/etc/ssl/certs/ca-bundle.crt"       // in nixpkgs' cacert output
	caFile           = "/etc/ssl/certs/ca-certificates.crt" // in the image (R-OUT-012)
	zoneInfoDir      = "/share/zoneinfo"                    // in nixpkgs' tzdata output
	zoneInfo         = "/usr/share/zoneinfo"                // in the image
)

// Release is what a release adds to an image it publishes (R-OUT-022): the version enters only
// the tags, the labels and the image's own derivation, never its layers.
type Release struct {
	Version string
	Tags    []string
	Labels  map[string]string
}

// imagePlan is the planning of one image node.
type imagePlan struct {
	ctx  context.Context
	req  engine.PlanRequest
	jobs *engine.Jobs
	byID map[string]*ir.Node
	n    *ir.Node
	spec ocispec.Image
	own  map[string]engine.Licence // paths bob built
}

// PlanImage plans the image node n: one layer derivation per store path (or shared group) of
// each platform's closure and one image derivation (R-OUT-009, R-OUT-011). With rel set the
// image is the one a release publishes: tags, version label and a derivation of its own, with
// the same layers.
func PlanImage(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, n *ir.Node, rel *Release) (engine.Resolved, error) {
	ip := &imagePlan{ctx: ctx, req: req, jobs: jobs, n: n, byID: map[string]*ir.Node{}}
	if req.Graph != nil {
		for _, x := range req.Graph.Nodes {
			ip.byID[x.ID] = x
		}
	}
	if err := json.Unmarshal(n.Spec, &ip.spec); err != nil {
		return engine.Resolved{}, fmt.Errorf("%s: image spec: %w", n.Label, err)
	}
	ip.own = engine.OwnLicences(req.Graph, req.Inputs)
	return ip.plan(rel)
}

// dep returns dependency i of the image: its node and planned outputs.
func (ip *imagePlan) dep(i int) (*ir.Node, engine.Resolved, error) {
	if i < 0 || i >= len(ip.n.Deps) {
		return nil, engine.Resolved{}, fmt.Errorf("%s: dependency index %d out of range", ip.n.Label, i)
	}
	id := ip.n.Deps[i]
	rv, ok := ip.req.Inputs[id]
	if !ok {
		return nil, engine.Resolved{}, fmt.Errorf("%s: dependency %s is not planned", ip.n.Label, id)
	}
	return ip.byID[id], rv, nil
}

// platformRoots are the roots of one platform.
type platformRoots struct {
	plat    bob.Platform
	program engine.Resolved
	roots   []engine.PlanOutput // program outputs, then the extras', in order, without duplicates
}

func (ip *imagePlan) plan(rel *Release) (engine.Resolved, error) {
	var plats []platformRoots
	var all []engine.PlanOutput
	for _, sp := range ip.spec.Platforms {
		pr := platformRoots{plat: bob.Platform(sp.Platform)}
		if pr.plat.NixSystem() == "" {
			return engine.Resolved{}, fmt.Errorf("%s: unknown platform %q", ip.n.Label, sp.Platform)
		}
		seen := map[string]bool{}
		for k, i := range append([]int{sp.Program}, sp.Extra...) {
			dn, rv, err := ip.dep(i)
			if err != nil {
				return engine.Resolved{}, err
			}
			if k == 0 {
				pr.program = rv
			}
			for _, o := range engine.PrimaryOutputs(dn, rv) {
				if !seen[o.Path] {
					seen[o.Path] = true
					pr.roots = append(pr.roots, o)
				}
			}
		}
		plats = append(plats, pr)
		all = append(all, pr.roots...)
	}
	var caOut, tzOut string
	for _, d := range []struct {
		i   int
		out *string
	}{{ip.spec.CACert, &caOut}, {ip.spec.TZData, &tzOut}} {
		if d.i < 0 {
			continue
		}
		dn, rv, err := ip.dep(d.i)
		if err != nil {
			return engine.Resolved{}, err
		}
		outs := engine.PrimaryOutputs(dn, rv)
		if len(outs) == 0 {
			return engine.Resolved{}, fmt.Errorf("%s: %s has no output", ip.n.Label, dn.Label)
		}
		*d.out = outs[0].Path
		all = append(all, outs[0])
	}
	closer, ok := ip.req.Store.(engine.PlanCloser)
	if !ok {
		return engine.Resolved{}, fmt.Errorf("%s: images need the store while planning (engine.PlanCloser)", ip.n.Label)
	}
	refs, err := closer.Closure(ip.ctx, all)
	if errors.Is(err, engine.ErrClosureBuild) {
		return ip.unplannable(all, err)
	}
	if err != nil {
		return engine.Resolved{}, err
	}
	args := imageArgs{Manifests: []manifest{}}
	layerDrvs := map[string][]string{}
	for _, pr := range plats {
		m, err := ip.platform(pr, refs, caOut, tzOut, rel, layerDrvs)
		if err != nil {
			return engine.Resolved{}, err
		}
		args.Manifests = append(args.Manifests, m)
	}
	name := drv.Name("oci-image", ip.spec.Name)
	if rel != nil {
		name = drv.Name("oci-image", ip.spec.Name, rel.Version)
		if len(rel.Tags) > 0 {
			args.Tag = rel.Tags[0]
		}
	}
	dp, out, err := ip.jobs.Add(engine.Job{Name: name, Kind: "oci-image", Label: ip.n.Label, Args: args, Drvs: layerDrvs})
	if err != nil {
		return engine.Resolved{}, err
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, nil
}

// platform plans the layers of one platform and returns its manifest arguments.
func (ip *imagePlan) platform(pr platformRoots, refs map[string][]string, caOut, tzOut string, rel *Release,
	layerDrvs map[string][]string) (manifest, error) {
	var rootPaths []string
	for _, o := range pr.roots {
		rootPaths = append(rootPaths, o.Path)
	}
	extraRoots := slices.DeleteFunc([]string{caOut, tzOut}, func(s string) bool { return s == "" })
	closure := engine.ClosureOf(refs, append(slices.Clone(rootPaths), extraRoots...)...)
	maxLayers := ip.spec.MaxLayers
	if maxLayers == 0 {
		maxLayers = defaultMaxLayers
	}
	groups, err := layerGroups(refs, closure, rootPaths, maxLayers)
	if err != nil {
		return manifest{}, diag.New(1020, "label", ip.n.Label, "option", "MaxLayers", "reason", err.Error())
	}
	var nixPaths []string
	for _, p := range closure {
		if _, mine := ip.own[p]; !mine {
			nixPaths = append(nixPaths, p)
		}
	}
	pruning, err := engine.Prune(ip.req.Store, nixPaths, ip.spec.Prune)
	if err != nil {
		return manifest{}, err
	}
	if err := engine.CheckPruned(ip.req.Store, ip.n.Label, closure, pruning); err != nil {
		return manifest{}, err
	}
	m := manifest{Architecture: pr.plat.GOARCH()}
	for _, g := range groups {
		var skip []string
		for _, p := range g {
			skip = append(skip, pruning.Skip[p]...)
		}
		sort.Strings(skip)
		dp, out, err := ip.jobs.Add(engine.Job{Name: layerName(g), Kind: "oci-layer", Label: ip.n.Label,
			Args: layerArgs{Format: layerFormat, Paths: g, Skip: nonNil(skip)}, Srcs: g, Discard: true})
		if err != nil {
			return manifest{}, err
		}
		layerDrvs[dp] = []string{"out"}
		m.Layers = append(m.Layers, out)
	}
	lic := engine.Licences(ip.ctx, ip.req.Nixpkgs, closure, ip.own)
	lj, err := json.Marshal(lic)
	if err != nil {
		return manifest{}, err
	}
	m.Custom = custom{Licences: string(lj) + "\n"}
	if caOut != "" {
		m.Custom.CACert = caOut + caBundle
	}
	if tzOut != "" {
		m.Custom.ZoneInfo = tzOut + zoneInfoDir
	}
	var rootLic []engine.Licence
	for _, l := range lic {
		if slices.Contains(rootPaths, l.Path) {
			rootLic = append(rootLic, l)
		}
	}
	m.Config = ip.config(pr, rootPaths, caOut != "", tzOut != "", engine.SPDXExpression(rootLic), rel)
	return m, nil
}

// config is the image configuration of one platform (R-OUT-012 defaults, options over them).
func (ip *imagePlan) config(pr platformRoots, roots []string, ca, tz bool, licences string, rel *Release) config {
	s := ip.spec
	var bins []string
	for _, r := range roots {
		if st, err := os.Stat(ip.req.Store.HostPath(r) + "/bin"); err == nil && st.IsDir() {
			bins = append(bins, r+"/bin")
		}
	}
	env := map[string]string{"PATH": strings.Join(append(bins, "/usr/bin", "/bin"), ":")}
	if ca {
		env["SSL_CERT_FILE"] = caFile
	}
	if tz {
		env["TZDIR"] = zoneInfo
	}
	maps.Copy(env, s.Env)
	var envList []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		envList = append(envList, k+"="+env[k])
	}
	c := config{User: s.User, WorkingDir: s.WorkDir, Env: envList, Entrypoint: nonNil(s.Entrypoint), Cmd: nonNil(s.Cmd),
		ExposedPorts: []string{}, Labels: map[string]string{}}
	if c.User == "" {
		c.User = defaultUser
	}
	if c.WorkingDir == "" {
		c.WorkingDir = defaultWorkDir
	}
	if len(c.Entrypoint) == 0 && pr.program.MainProgram != "" {
		if out := pr.program.Outputs["out"]; out != "" {
			c.Entrypoint = []string{path.Join(out, pr.program.MainProgram)}
		}
	}
	ports := slices.Clone(s.Ports)
	slices.Sort(ports)
	for _, p := range slices.Compact(ports) {
		c.ExposedPorts = append(c.ExposedPorts, strconv.Itoa(p)+"/tcp")
	}
	maps.Copy(c.Labels, s.Labels)
	c.Labels["org.opencontainers.image.licenses"] = licences
	if rel != nil {
		maps.Copy(c.Labels, rel.Labels)
	}
	return c
}

// layerGroups splits a platform's closure into layers (R-OUT-009): non-root paths by
// (popularity desc, path asc), the first L-1 one layer each and the rest one shared layer,
// then one layer per root in root order; L = maxLayers - 1 - |roots| (the customisation layer
// is the last one).
func layerGroups(refs map[string][]string, closure, roots []string, maxLayers int) ([][]string, error) {
	if maxLayers < 2 || maxLayers > 125 {
		return nil, fmt.Errorf("must be between 2 and 125 (got %d)", maxLayers)
	}
	l := maxLayers - 1 - len(roots)
	if l < 1 {
		return nil, fmt.Errorf("%d roots need MaxLayers ≥ %d", len(roots), len(roots)+2)
	}
	pop := popularity(refs, closure)
	isRoot := map[string]bool{}
	for _, r := range roots {
		isRoot[r] = true
	}
	var rest []string
	for _, p := range closure {
		if !isRoot[p] {
			rest = append(rest, p)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if pop[rest[i]] != pop[rest[j]] {
			return pop[rest[i]] > pop[rest[j]]
		}
		return rest[i] < rest[j]
	})
	var groups [][]string
	singles := min(l-1, len(rest))
	for _, p := range rest[:singles] {
		groups = append(groups, []string{p})
	}
	if shared := slices.Clone(rest[singles:]); len(shared) > 0 {
		sort.Strings(shared)
		groups = append(groups, shared)
	}
	for _, r := range roots {
		groups = append(groups, []string{r})
	}
	return groups, nil
}

// popularity counts, for each path of closure, the closure paths whose closure contains it.
func popularity(refs map[string][]string, closure []string) map[string]int {
	pop := map[string]int{}
	for _, q := range closure {
		for _, p := range engine.ClosureOf(refs, q) {
			pop[p]++
		}
	}
	return pop
}

// layerName is oci-layer-<storehash8>-<name> of a one-path layer; a shared layer is named after
// the hash of its paths.
func layerName(g []string) string {
	if len(g) == 1 {
		base := path.Base(g[0])
		if len(base) > 33 && base[32] == '-' {
			return drv.Name("oci-layer", base[:8], base[33:])
		}
		return drv.Name("oci-layer", base)
	}
	sum := sha256.Sum256([]byte(strings.Join(g, "\n")))
	return drv.Name("oci-layer", hex.EncodeToString(sum[:4]), "shared")
}

// unplannable plans an image derivation that depends on the image's roots when they could not
// be built while planning: the build stage then builds the roots and reports their failure;
// should the image derivation itself ever run, it fails with the planning error.
func (ip *imagePlan) unplannable(all []engine.PlanOutput, _ error) (engine.Resolved, error) {
	ds := map[string][]string{}
	var srcs []string
	for _, o := range all {
		if o.Drv == "" {
			srcs = append(srcs, o.Path)
			continue
		}
		ds[o.Drv] = append(ds[o.Drv], o.Name)
	}
	dp, out, err := ip.jobs.Add(engine.Job{Name: drv.Name("oci-image", ip.spec.Name), Kind: "oci-image", Label: ip.n.Label,
		Args: imageArgs{Manifests: []manifest{}, Error: "the programs of the image could not be built while planning"}, Drvs: ds, Srcs: srcs})
	if err != nil {
		return engine.Resolved{}, err
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
