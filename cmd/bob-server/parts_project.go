package main

import (
	"context"
	"errors"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleetsrv"
	"github.com/megakuul/bob/internal/gen/bob/settings/v1/settingsv1connect"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/overview"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
)

// projectWiring is what the project parts hand to the build parts: the Projects provider of
// the build controller and the hooks that keep the pipelines cache.
type projectWiring struct {
	P     *project.Projects
	scope project.Scoper
}

// projectParts wires projects (SPEC §6.5.6): ProjectService over the forge integrations' repo
// links, the projects index repair (in the index-repair pass), and the read RPCs of the UI
// that need no build controller: GetSettings/UpdateSettings (keeping the key RPCs of the cache
// parts), CacheService and the release and registry lists until M16.3/M16.4. It runs after
// forgeParts; buildParts takes the result (I-B12).
func projectParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts, fw *forgeWiring) *projectWiring {
	if env.Storage == nil || env.Storage.Control == nil {
		env.Log.Error("projects not available: the control bucket is missing")
		return nil
	}
	cfg := project.Config{Control: env.Storage.Control, BaseURL: env.Config.BaseURL, Now: env.Now, Log: env.Log}
	if fw != nil && fw.Registry != nil {
		cfg.Links = fw.Registry
	}
	p, err := project.New(cfg)
	if err != nil {
		env.Log.Error("projects not available", "err", err.Error())
		return nil
	}
	w := &projectWiring{P: p}
	if a, ok := parts.Authz.(*rbac.Authorizer); ok && a != nil {
		w.scope = a
	}
	parts.Services.Project = &project.Service{P: p, Scope: w.scope}

	keys := parts.Services.Settings
	if keys == nil {
		keys = settingsv1connect.UnimplementedSettingsServiceHandler{}
	}
	settings := &overview.Settings{SettingsServiceHandler: keys, Control: env.Storage.Control, BaseURL: env.Config.BaseURL}
	parts.Services.Settings = settings
	cs := &overview.Cache{Scope: w.scope, Retention: settings.CacheDays}
	sc := env.Storage.Config
	if obj, err := cache.OpenObjects(ctx, s3client.Options{Endpoint: sc.Endpoint, Region: sc.Region}, sc.Artifacts); err == nil {
		cs.Objects, cs.Vis = obj, cache.NewVisStore(obj, cache.NixVisPrefix, env.Now)
	} else {
		env.Log.Error("GetPath not available: artifacts bucket", "err", err.Error())
	}
	parts.Services.Cache = cs
	if parts.Services.Release == nil {
		parts.Services.Release = overview.Releases{}
	}
	if parts.Services.Registry == nil {
		parts.Services.Registry = overview.Registry{}
	}

	for _, c := range parts.Controllers {
		if ctl, ok := c.(*orchestrator.Controller); ok && ctl.ID == orchestrator.CtlIndexRepair {
			pass := ctl.Pass
			ctl.Pass = func(ctx context.Context, r *orchestrator.Run) error {
				return errors.Join(pass(ctx, r), p.Repair(ctx))
			}
		}
	}
	return w
}

// projects is the Projects provider of the build controller (AnyProject without projects).
func (w *projectWiring) projects() buildctl.Projects {
	if w == nil {
		return buildctl.AnyProject{}
	}
	return w.P
}

// hooks sets the build controller's hooks that keep the pipelines cache.
func (w *projectWiring) hooks(env *server.Env, cfg *buildctl.Config) {
	if w == nil {
		return
	}
	cfg.Evaluated = func(ctx context.Context, rec *store.BuildRecord, r *jobspec.Report) {
		if err := w.P.Evaluated(ctx, rec, r); err != nil {
			env.Log.Warn("projects: pipelines cache not updated", "build", rec.ID.Value(), "err", err.Error())
		}
	}
	finished := cfg.Finished
	cfg.Finished = func(ctx context.Context, rec *store.BuildRecord) {
		if err := w.P.Finished(ctx, rec); err != nil {
			env.Log.Warn("projects: pipelines cache not updated", "build", rec.ID.Value(), "err", err.Error())
		}
		if finished != nil {
			finished(ctx, rec)
		}
	}
}

// withBuilds wires what reads builds: AnalyticsService.
func (w *projectWiring) withBuilds(env *server.Env, parts *server.OrchestratorParts, b *buildctl.Builds) {
	params, err := fleetsrv.ParamsFromEnv(env.Getenv)
	if err != nil {
		params = store.FleetParams{}
	}
	params = fleetsrv.Merge(params, fleetsrv.DefaultParams())
	a := &overview.Analytics{Builds: b, Control: env.Storage.Control, Fleet: env.Storage.Fleet,
		WorkerShards: params.WorkerShards, GoneAfter: params.GoneAfter, Now: env.Now}
	if w != nil {
		a.Scope = w.scope
	}
	parts.Services.Analytics = a
}
