package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleetsrv"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/integration"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/worker"
)

// buildParts wires the build controller (SPEC §05.6.2, M17.2/M17.3): the leader-only `build`
// controller, BuildService and JobService, job ids resolving to their project through the
// build id they carry, the webhook intake's build creation and build.finished notifications.
// It runs after fleetParts (enqueue) and forgeParts (intake, statuses, notifier).
func buildParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts, f *fleetsrv.Fleet, fw *forgeWiring) error {
	if f == nil || env.Storage == nil || env.Storage.Control == nil {
		return errors.New("builds: the fleet parts or the control bucket are missing")
	}
	params, err := fleetsrv.ParamsFromEnv(env.Getenv)
	if err != nil {
		return &server.ConfigError{Msg: err.Error()}
	}
	params = fleetsrv.Merge(params, fleetsrv.DefaultParams())
	cfg := buildctl.Config{Name: env.Config.Name, Control: env.Storage.Control, Jobs: env.Storage.Fleet, Fleet: f,
		Cancel: f.Hub, MaxAttempts: params.MaxAttempts, JobShards: params.JobShards, AwaitPromotion: true,
		BaseURL: strings.TrimRight(env.Config.BaseURL, "/"), Skew: params.Skew, Now: env.Now, Log: env.Log}
	sc := env.Storage.Config
	if objs, err := cache.OpenObjects(ctx, s3client.Options{Endpoint: sc.Endpoint, Region: sc.Region}, sc.Artifacts); err == nil {
		cfg.Artifacts = objs
	} else {
		env.Log.Error("builds: artifacts bucket unavailable; evaluation reports cannot be read", "err", err.Error())
	}
	if fw != nil && fw.Registry != nil {
		cfg.Statuses = statusSink{reg: fw.Registry, projects: buildctl.AnyProject{}}
	}
	var b *buildctl.Builds
	if fw != nil && fw.Notifier != nil {
		cfg.Finished = func(ctx context.Context, rec *store.BuildRecord) {
			pb, err := b.ToProto(ctx, rec)
			if err == nil {
				err = fw.Notifier.BuildFinished(ctx, rec.Project, pb)
			}
			if err != nil {
				env.Log.Warn("builds: build.finished notification", "build", rec.ID, "err", err.Error())
			}
		}
	}
	if b, err = buildctl.New(cfg); err != nil {
		return err
	}
	parts.Controllers = append(parts.Controllers, b.Controller())
	svc := &buildctl.Service{B: b, Authz: parts.Authz}
	parts.Services.Build, parts.Services.Job = svc, svc
	if r, ok := parts.Resolve.(*rbac.Resolver); ok {
		r.Job = b.JobProject
	}
	if fw != nil && fw.Intake != nil {
		fw.Intake.Builds = intakeBuilds{b}
	}
	return nil
}

// intakeBuilds adapts CreateFromEvent to the intake: an event that starts no build is
// answered like an ignored one.
type intakeBuilds struct{ b *buildctl.Builds }

func (i intakeBuilds) CreateFromEvent(ctx context.Context, project, key string, ev forge.Event) (string, error) {
	id, err := i.b.CreateFromEvent(ctx, project, key, ev)
	if errors.Is(err, buildctl.ErrNoBuild) {
		return "", nil
	}
	return id, err
}

// statusSink writes commit statuses through the integration of the project's linked
// repository (Project.Repo = "<forgeID>/<repoID>"); projects without a link get none.
type statusSink struct {
	reg      *integration.Registry
	projects buildctl.Projects
}

func (s statusSink) SetStatus(ctx context.Context, project string, st forge.Status) error {
	p, err := s.projects.Project(ctx, project)
	if err != nil || p.Repo == "" {
		return nil
	}
	forgeID, repo, ok := strings.Cut(p.Repo, "/")
	id, perr := strconv.ParseInt(repo, 10, 64)
	if !ok || perr != nil {
		return fmt.Errorf("project %s: repository %q", project, p.Repo)
	}
	return s.reg.SetStatus(ctx, forgeID, id, st)
}

// workerJobs gives the worker loop the artifacts bucket (sources, staging) and the public
// cache whose paths are not staged (R-REG-005).
func workerJobs(env *server.Env, wp server.WorkerParts, err error) (server.WorkerParts, error) {
	if err != nil {
		return wp, err
	}
	p, ok := wp.Loop.(*worker.Process)
	if !ok {
		return wp, nil
	}
	sc := env.Storage.Config
	objs, err := cache.OpenObjects(context.Background(), s3client.Options{Endpoint: sc.Endpoint, Region: sc.Region}, sc.Artifacts)
	if err != nil {
		return wp, fmt.Errorf("worker: artifacts bucket: %w", err)
	}
	p.UseArtifacts(objs, "https://cache.nixos.org")
	return wp, nil
}
