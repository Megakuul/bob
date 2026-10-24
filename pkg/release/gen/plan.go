// Package gen implements engine.Ecosystem for pkg/release (SPEC §04.8).
package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	ocigen "github.com/megakuul/bob/pkg/output/oci/gen"
	"github.com/megakuul/bob/pkg/release/internal/relspec"

	// The release API registers the release.release kind the IR names.
	_ "github.com/megakuul/bob/pkg/release"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem plans releases (SPEC §04.8). It has no lock section and no native files.
type Ecosystem struct{}

// Name is the kind prefix "release".
func (Ecosystem) Name() string { return "release" }

// Lock has nothing to resolve.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// devVersion is the version without bob build --version (R-OUT-022).
const devVersion = "0.0.0-dev"

// Kinds of the artifacts a release lists (their node kinds).
const (
	kindImage    = "oci.image"
	kindAppImage = "appimage.bundle"
	kindTarball  = "tarball.tarball"
)

// args is the job of kind "release" (pkg/release/bop.Args).
type args struct {
	Project   string     `json:"project"`
	Release   string     `json:"release"`
	Label     string     `json:"label"`
	Version   string     `json:"version"`
	Public    bool       `json:"public"`
	Artifacts []artifact `json:"artifacts"`
}

type artifact struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Platform    string            `json:"platform"`
	StorePath   string            `json:"storePath"`
	Entrypoints map[string]string `json:"entrypoints"`
	File        string            `json:"file"`
	Repo        string            `json:"repo"`
	Tags        []string          `json:"tags"`
}

// Plan plans one release derivation per node: it depends on every artifact (the closures per
// platform, the images as the release publishes them, the AppImages and tarballs) and writes
// release.json. The version enters only this derivation and the release's images (tags and
// the version label); the artifacts' packages never see it (R-OUT-022).
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("release planner: internal error: %v", v)
		}
	}()
	byID := map[string]*ir.Node{}
	if req.Graph != nil {
		for _, n := range req.Graph.Nodes {
			byID[n.ID] = n
		}
	}
	jobs := engine.NewJobs(req)
	res.Nodes = map[string]engine.Resolved{}
	var errs []error
	for _, n := range req.Nodes {
		rv, err := plan(ctx, req, jobs, byID, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.Nodes[n.ID] = rv
	}
	if len(errs) > 0 {
		return engine.PlanResult{}, errors.Join(errs...)
	}
	res.Drvs = jobs.Drvs
	return res, nil
}

func plan(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, byID map[string]*ir.Node, n *ir.Node) (engine.Resolved, error) {
	var s relspec.Release
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, fmt.Errorf("%s: release spec: %w", n.Label, err)
	}
	version := req.Version
	if version == "" {
		version = devVersion
	}
	a := args{Project: "", Release: s.Name, Label: n.Label, Version: version, Public: s.Public, Artifacts: []artifact{}}
	drvs := map[string][]string{}
	use := func(rv engine.Resolved, out string) string {
		if rv.Drv != "" {
			drvs[rv.Drv] = append(drvs[rv.Drv], out)
		}
		return rv.Outputs[out]
	}
	dep := func(i int) (*ir.Node, engine.Resolved, error) {
		if i < 0 || i >= len(n.Deps) {
			return nil, engine.Resolved{}, fmt.Errorf("%s: dependency index %d out of range", n.Label, i)
		}
		rv, ok := req.Inputs[n.Deps[i]]
		dn := byID[n.Deps[i]]
		if !ok || dn == nil {
			return nil, engine.Resolved{}, fmt.Errorf("%s: dependency %s is not planned", n.Label, n.Deps[i])
		}
		return dn, rv, nil
	}
	for _, sp := range s.Platforms {
		for _, i := range sp.Closures {
			dn, rv, err := dep(i)
			if err != nil {
				return engine.Resolved{}, err
			}
			outs := engine.PrimaryOutputs(dn, rv)
			if len(outs) == 0 {
				return engine.Resolved{}, fmt.Errorf("%s: closure %s has no output", n.Label, dn.Label)
			}
			name := labelName(dn.Label)
			art := artifact{Name: name, Kind: "closure", Platform: sp.Platform, StorePath: use(rv, outs[0].Name),
				Entrypoints: map[string]string{}, Tags: []string{}}
			if rv.MainProgram != "" {
				art.Entrypoints[name] = rv.MainProgram
			}
			a.Artifacts = append(a.Artifacts, art)
		}
	}
	for _, i := range s.Images {
		dn, _, err := dep(i)
		if err != nil {
			return engine.Resolved{}, err
		}
		if dn.Kind != kindImage {
			return engine.Resolved{}, fmt.Errorf("%s: image %s has kind %s", n.Label, dn.Label, dn.Kind)
		}
		img, err := ocigen.PlanImage(ctx, req, jobs, dn, &ocigen.Release{Version: version, Tags: []string{version},
			Labels: map[string]string{"org.opencontainers.image.version": version}})
		if err != nil {
			return engine.Resolved{}, err
		}
		a.Artifacts = append(a.Artifacts, artifact{Name: labelName(dn.Label), Kind: "oci", Platform: "multi",
			StorePath: use(img, "out"), Entrypoints: map[string]string{}, Repo: repo(a.Project, s.Name, labelName(dn.Label), len(s.Images)),
			Tags: []string{version}})
	}
	for _, f := range []struct {
		idx        []int
		kind, node string
		glob       string
	}{{s.AppImages, "appimage", kindAppImage, "*.AppImage"}, {s.Tarballs, "tarball", kindTarball, "*.tar.gz"}} {
		for _, i := range f.idx {
			dn, rv, err := dep(i)
			if err != nil {
				return engine.Resolved{}, err
			}
			if dn.Kind != f.node {
				return engine.Resolved{}, fmt.Errorf("%s: %s %s has kind %s", n.Label, f.kind, dn.Label, dn.Kind)
			}
			plat, err := engine.NodePlatform(dn)
			if err != nil || plat == "" {
				plat = engine.HostPlatform()
			}
			a.Artifacts = append(a.Artifacts, artifact{Name: labelName(dn.Label), Kind: f.kind, Platform: string(plat),
				StorePath: use(rv, "out"), Entrypoints: map[string]string{}, File: f.glob, Tags: []string{}})
		}
	}
	dp, out, err := jobs.Add(engine.Job{Name: drv.Name("release", s.Name, version), Kind: "release", Label: n.Label,
		Args: a, Drvs: drvs})
	if err != nil {
		return engine.Resolved{}, err
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, nil
}

// labelName is the name part of a label ("//deploy:api" → "api").
func labelName(l string) string {
	if i := strings.LastIndexByte(l, ':'); i >= 0 {
		return l[i+1:]
	}
	return l
}

// repo is the registry repository of a release image: <project>/<release>, or
// <project>/<release>/<image> when the release has several images ("" project: no prefix).
func repo(project, release, image string, images int) string {
	r := release
	if images > 1 {
		r = release + "/" + image
	}
	return path.Join(project, r)
}
