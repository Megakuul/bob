package main

import (
	"context"
	"errors"
	"net/http"

	"google.golang.org/protobuf/proto"

	"github.com/megakuul/bob/internal/auth"
	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/release"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/store/sweep"
)

// registryParts wires the OCI pull registry (M16.3: /v2/, /oci/token, RegistryService, the
// image step of the promotion) and releases (M16.4: ReleaseService, /r/ downloads, the build
// controller's release hook). It runs last: it hooks into the build controller of buildParts
// and gives the promotion
// controller of cacheParts a promoter with the Images hook.
func registryParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts, fw *forgeWiring) {
	var id *auth.Identity
	for _, a := range parts.Authn {
		if x, ok := a.(*auth.Identity); ok {
			id = x
		}
	}
	authz, _ := parts.Authz.(*rbac.Authorizer)
	if id == nil || authz == nil || env.Storage == nil || env.Storage.Control == nil || env.Storage.Fleet == nil {
		env.Log.Error("registry and releases not available: identity, RBAC or storage is missing")
		return
	}
	sc := env.Storage.Config
	obj, err := store.OpenObjects(ctx, s3client.Options{Endpoint: sc.Endpoint, Region: sc.Region}, sc.Artifacts)
	if err != nil {
		env.Log.Error("registry and releases not available: artifacts bucket", "err", err.Error())
		return
	}
	maxDownloads := intSetting(env, "BOB_MAX_PARALLEL_DOWNLOADS", cache.DefaultMaxDownloads)
	ociVis := cache.NewVisStore(obj, cache.OCIVisPrefix, env.Now)
	nixVis := cache.NewVisStore(obj, cache.NixVisPrefix, env.Now)
	reg := registry.New(registry.Config{Control: env.Storage.Control, Objects: obj, Vis: ociVis, Principal: id.HTTPPrincipal,
		Scope: authz, BaseURL: env.Config.BaseURL, MaxDownloads: maxDownloads, Now: env.Now, Log: env.Log})
	if err := reg.Keys.Ensure(ctx); err != nil {
		env.Log.Warn("no OCI token key yet; the token endpoint creates one", "err", err.Error())
	}
	if parts.Routes == nil {
		parts.Routes = map[string]http.Handler{}
	}
	// One pattern per method: "/v2/" itself is the pending 501 prefix of internal/http.
	for _, m := range []string{"GET", "PUT", "POST", "PATCH", "DELETE", "OPTIONS"} {
		parts.Routes[m+" /v2/"] = reg // GET also serves HEAD; the others answer 405 UNSUPPORTED
	}
	parts.Routes["GET /oci/token"] = reg.TokenHandler()
	parts.Services.Registry = &registry.Service{R: reg, Scope: authz}

	keys := &promote.Keys{Control: env.Storage.Control, Now: env.Now}
	rc := release.Config{Control: env.Storage.Control, Objects: obj, NixVis: nixVis, OCIVis: ociVis, Repos: reg.Repos,
		BaseURL: env.Config.BaseURL, PublicKeys: keys.PublicKeys, Now: env.Now, Log: env.Log}
	if fw != nil && fw.Notifier != nil {
		rc.Published = func(ctx context.Context, project string, rel proto.Message) error {
			return fw.Notifier.ReleasePublished(ctx, project, rel)
		}
	}
	rel, err := release.New(rc)
	if err != nil {
		env.Log.Error("releases not available", "err", err.Error())
		return
	}
	parts.Services.Release = &release.Service{R: rel, Scope: authz}
	parts.Routes["GET /r/"] = release.NewDownloads(rel, id.HTTPPrincipal, authz, maxDownloads)
	if svc, ok := parts.Services.Build.(*buildctl.Service); ok && svc.B != nil {
		svc.B.SetReleaser(rel)
	} else {
		env.Log.Error("release pipelines publish nothing: the build controller is missing")
	}
	imagePromotion(ctx, env, parts, obj, nixVis, keys, reg)
}

// imagePromotion replaces the pass of the promotion controller (cacheParts) with the same
// pass over a promoter that also promotes staged OCI images (§6.6.4 step 4). The promoter of
// cacheParts has no Images hook and would mark such jobs promoted with their images left in
// staging.
func imagePromotion(ctx context.Context, env *server.Env, parts *server.OrchestratorParts, obj *store.Objects, vis *cache.VisStore, keys *promote.Keys, reg *registry.Registry) {
	var ctl *orchestrator.Controller
	for _, c := range parts.Controllers {
		if x, ok := c.(*orchestrator.Controller); ok && x.ID == orchestrator.CtlPromotion {
			ctl = x
		}
	}
	if ctl == nil {
		env.Log.Error("image promotion not available: no promotion controller")
		return
	}
	p, err := promote.New(promote.Config{Name: env.Config.Name, Control: env.Storage.Control, Fleet: env.Storage.Fleet,
		Objects: obj, Vis: vis, Keys: keys, JobShards: intSetting(env, "BOB_FLEET_JOB_SHARDS", 16), Now: env.Now, Log: env.Log,
		Images: reg.PromoteImages})
	if err != nil {
		env.Log.Error("image promotion not available", "err", err.Error())
		return
	}
	st := env.Storage.Config
	lister, err := sweep.NewNarInfos(ctx, st.Endpoint, st.Region, st.Artifacts)
	if err != nil {
		env.Log.Error("signing key retirement not available", "err", err.Error())
	}
	ctl.Pass = func(ctx context.Context, _ *orchestrator.Run) error {
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
}
