package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/auth"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/store/sweep"
)

// promotionInterval is the promotion controller's period (SPEC §05.11: event + 10 s).
const promotionInterval = 10 * time.Second

// cacheParts wires the Nix binary cache (M16.1: /nix/, visibility documents, download proxy)
// and promotion (M16.2: the promotion controller, signing keys, the key RPCs of
// SettingsService). It needs identity and RBAC (authParts runs first).
func cacheParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts) {
	var id *auth.Identity
	for _, a := range parts.Authn {
		if x, ok := a.(*auth.Identity); ok {
			id = x
		}
	}
	authz, _ := parts.Authz.(*rbac.Authorizer)
	if id == nil || authz == nil || env.Storage == nil || env.Storage.Control == nil || env.Storage.Fleet == nil {
		env.Log.Error("binary cache not available: identity, RBAC or storage is missing")
		return
	}
	st := env.Storage.Config
	obj, err := cache.OpenObjects(ctx, s3client.Options{Endpoint: st.Endpoint, Region: st.Region}, st.Artifacts)
	if err != nil {
		env.Log.Error("binary cache not available", "err", err.Error())
		return
	}
	vis := cache.NewVisStore(obj, cache.NixVisPrefix, env.Now)
	h := cache.NewHandler(cache.Config{
		Objects: obj, Vis: vis, Log: env.Log,
		MaxDownloads: intSetting(env, "BOB_MAX_PARALLEL_DOWNLOADS", cache.DefaultMaxDownloads),
		Principal:    id.HTTPPrincipal,
		Allowed: func(ctx context.Context, p *api.Principal, projects []string) (bool, error) {
			sc, err := authz.Scope(ctx, p, cache.GetPathMethod)
			if err != nil {
				return false, err
			}
			return slices.ContainsFunc(projects, sc.Contains), nil
		},
	})
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	delete(parts.Routes, "GET /nix/nix-cache-info") // the stand-in of authParts until this milestone
	parts.Routes["GET /nix/"] = h                   // GET and HEAD

	keys := &promote.Keys{Control: env.Storage.Control, Now: env.Now}
	if err := keys.Ensure(ctx); err != nil {
		env.Log.Warn("no cache signing key yet; the promotion controller retries", "err", err.Error())
	}
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	parts.Services.Settings = &promote.SettingsService{Keys: keys, Retired: poke}

	p, err := promote.New(promote.Config{Name: env.Config.Name, Control: env.Storage.Control, Fleet: env.Storage.Fleet,
		Objects: obj, Vis: vis, Keys: keys, JobShards: intSetting(env, "BOB_FLEET_JOB_SHARDS", 16), Now: env.Now, Log: env.Log})
	if err != nil {
		env.Log.Error("promotion not available", "err", err.Error())
		return
	}
	lister, err := sweep.NewNarInfos(ctx, st.Endpoint, st.Region, st.Artifacts)
	if err != nil {
		env.Log.Error("signing key retirement not available", "err", err.Error())
	}
	pass := func(ctx context.Context, _ *orchestrator.Run) error {
		err := keys.Ensure(ctx)
		if err == nil {
			err = p.Run(ctx)
		}
		var retire error
		if lister != nil {
			retire = p.RetireKeys(ctx, lister)
		}
		return errors.Join(err, retire)
	}
	parts.Controllers = append(parts.Controllers, &orchestrator.Controller{
		ID: orchestrator.CtlPromotion, Fleet: env.Storage.Fleet, Owner: env.Config.Name, Interval: promotionInterval,
		Skew: setting(env, "BOB_FLEET_SKEW", 5*time.Second), Now: env.Now, Pass: pass, Wake: wake, Log: env.Log,
	})
}
