package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	accessv1 "github.com/megakuul/bob/internal/gen/bob/access/v1"
	"github.com/megakuul/bob/internal/gen/bob/access/v1/accessv1connect"
	analyticsv1 "github.com/megakuul/bob/internal/gen/bob/analytics/v1"
	"github.com/megakuul/bob/internal/gen/bob/analytics/v1/analyticsv1connect"
	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
	"github.com/megakuul/bob/internal/gen/bob/auth/v1/authv1connect"
	buildv1 "github.com/megakuul/bob/internal/gen/bob/build/v1"
	"github.com/megakuul/bob/internal/gen/bob/build/v1/buildv1connect"
	cachev1 "github.com/megakuul/bob/internal/gen/bob/cache/v1"
	"github.com/megakuul/bob/internal/gen/bob/cache/v1/cachev1connect"
	fleetv1 "github.com/megakuul/bob/internal/gen/bob/fleet/v1"
	"github.com/megakuul/bob/internal/gen/bob/fleet/v1/fleetv1connect"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/gen/bob/forge/v1/forgev1connect"
	logv1 "github.com/megakuul/bob/internal/gen/bob/log/v1"
	"github.com/megakuul/bob/internal/gen/bob/log/v1/logv1connect"
	notifyv1 "github.com/megakuul/bob/internal/gen/bob/notify/v1"
	"github.com/megakuul/bob/internal/gen/bob/notify/v1/notifyv1connect"
	projectv1 "github.com/megakuul/bob/internal/gen/bob/project/v1"
	"github.com/megakuul/bob/internal/gen/bob/project/v1/projectv1connect"
	registryv1 "github.com/megakuul/bob/internal/gen/bob/registry/v1"
	"github.com/megakuul/bob/internal/gen/bob/registry/v1/registryv1connect"
	releasev1 "github.com/megakuul/bob/internal/gen/bob/release/v1"
	"github.com/megakuul/bob/internal/gen/bob/release/v1/releasev1connect"
	settingsv1 "github.com/megakuul/bob/internal/gen/bob/settings/v1"
	"github.com/megakuul/bob/internal/gen/bob/settings/v1/settingsv1connect"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
)

// Services are the implementations of the API services. A nil field is served by the
// built-in implementation: AuthService (GetServerInfo, WhoAmI) and RoleService
// (ListPermissionCatalog) have one, every other method answers Unimplemented. Each milestone
// fills its fields (identity M15.2, RBAC M15.4, builds, fleet, …).
type Services struct {
	Auth           authv1connect.AuthServiceHandler
	User           accessv1connect.UserServiceHandler
	Role           accessv1connect.RoleServiceHandler
	ServiceAccount accessv1connect.ServiceAccountServiceHandler
	Oidc           accessv1connect.OidcServiceHandler
	Project        projectv1connect.ProjectServiceHandler
	Build          buildv1connect.BuildServiceHandler
	Job            buildv1connect.JobServiceHandler
	Log            logv1connect.LogServiceHandler
	Fleet          fleetv1connect.FleetServiceHandler
	Cache          cachev1connect.CacheServiceHandler
	Registry       registryv1connect.RegistryServiceHandler
	Release        releasev1connect.ReleaseServiceHandler
	Settings       settingsv1connect.SettingsServiceHandler
	Forge          forgev1connect.ForgeServiceHandler
	Notify         notifyv1connect.NotifyServiceHandler
	Analytics      analyticsv1connect.AnalyticsServiceHandler
	WorkerControl  workerv1connect.WorkerControlServiceHandler
}

// Info is what GetServerInfo and WhoAmI report about this server.
type Info struct {
	Name    string
	BaseURL string // "" when unknown
	Version string
}

// Config configures the API server.
type Config struct {
	Services Services
	Authn    Authenticator       // required
	Authz    Authorizer          // nil = AdminBindings
	Resolve  Resolver            // nil: rules with resolve answer NotFound
	Workers  WorkerAuthenticator // nil: KIND_WORKER methods answer Unauthenticated
	Info     Info
	Log      *slog.Logger // nil = slog.Default()
}

// Server is the connect API of an orchestrator, mounted under /api/ (SPEC §6.2).
type Server struct {
	rules   Rules
	authn   Authenticator
	authz   Authorizer
	resolve Resolver
	workers WorkerAuthenticator
	log     *slog.Logger
	mux     *http.ServeMux
}

func svc(fd protoreflect.FileDescriptor, name protoreflect.Name) protoreflect.ServiceDescriptor {
	return fd.Services().ByName(name)
}

// New builds the API. It refuses (returns an error) when any served method has no valid
// authorization rule (R-AUTH-006).
func New(cfg Config) (*Server, error) {
	if cfg.Authn == nil {
		return nil, errors.New("api: Config.Authn is required")
	}
	s := &Server{
		authn: cfg.Authn, authz: cfg.Authz, resolve: cfg.Resolve, workers: cfg.Workers,
		log: cfg.Log, mux: http.NewServeMux(),
	}
	if s.authz == nil {
		s.authz = AdminBindings{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	sv := cfg.Services
	type mount struct {
		sd protoreflect.ServiceDescriptor
		mk func(...connect.HandlerOption) (string, http.Handler)
	}
	mounts := []mount{
		{svc(authv1.File_bob_auth_v1_auth_proto, "AuthService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return authv1connect.NewAuthServiceHandler(or(sv.Auth, authv1connect.AuthServiceHandler(&builtinAuth{info: cfg.Info})), o...)
		}},
		{svc(accessv1.File_bob_access_v1_access_proto, "UserService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return accessv1connect.NewUserServiceHandler(or(sv.User, accessv1connect.UserServiceHandler(accessv1connect.UnimplementedUserServiceHandler{})), o...)
		}},
		{svc(accessv1.File_bob_access_v1_access_proto, "RoleService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return accessv1connect.NewRoleServiceHandler(or(sv.Role, accessv1connect.RoleServiceHandler(&builtinRole{s: s})), o...)
		}},
		{svc(accessv1.File_bob_access_v1_access_proto, "ServiceAccountService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return accessv1connect.NewServiceAccountServiceHandler(or(sv.ServiceAccount, accessv1connect.ServiceAccountServiceHandler(accessv1connect.UnimplementedServiceAccountServiceHandler{})), o...)
		}},
		{svc(accessv1.File_bob_access_v1_access_proto, "OidcService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return accessv1connect.NewOidcServiceHandler(or(sv.Oidc, accessv1connect.OidcServiceHandler(accessv1connect.UnimplementedOidcServiceHandler{})), o...)
		}},
		{svc(projectv1.File_bob_project_v1_project_proto, "ProjectService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return projectv1connect.NewProjectServiceHandler(or(sv.Project, projectv1connect.ProjectServiceHandler(projectv1connect.UnimplementedProjectServiceHandler{})), o...)
		}},
		{svc(buildv1.File_bob_build_v1_build_proto, "BuildService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return buildv1connect.NewBuildServiceHandler(or(sv.Build, buildv1connect.BuildServiceHandler(buildv1connect.UnimplementedBuildServiceHandler{})), o...)
		}},
		{svc(buildv1.File_bob_build_v1_build_proto, "JobService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return buildv1connect.NewJobServiceHandler(or(sv.Job, buildv1connect.JobServiceHandler(buildv1connect.UnimplementedJobServiceHandler{})), o...)
		}},
		{svc(logv1.File_bob_log_v1_log_proto, "LogService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return logv1connect.NewLogServiceHandler(or(sv.Log, logv1connect.LogServiceHandler(logv1connect.UnimplementedLogServiceHandler{})), o...)
		}},
		{svc(fleetv1.File_bob_fleet_v1_fleet_proto, "FleetService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return fleetv1connect.NewFleetServiceHandler(or(sv.Fleet, fleetv1connect.FleetServiceHandler(fleetv1connect.UnimplementedFleetServiceHandler{})), o...)
		}},
		{svc(cachev1.File_bob_cache_v1_cache_proto, "CacheService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return cachev1connect.NewCacheServiceHandler(or(sv.Cache, cachev1connect.CacheServiceHandler(cachev1connect.UnimplementedCacheServiceHandler{})), o...)
		}},
		{svc(registryv1.File_bob_registry_v1_registry_proto, "RegistryService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return registryv1connect.NewRegistryServiceHandler(or(sv.Registry, registryv1connect.RegistryServiceHandler(registryv1connect.UnimplementedRegistryServiceHandler{})), o...)
		}},
		{svc(releasev1.File_bob_release_v1_release_proto, "ReleaseService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return releasev1connect.NewReleaseServiceHandler(or(sv.Release, releasev1connect.ReleaseServiceHandler(releasev1connect.UnimplementedReleaseServiceHandler{})), o...)
		}},
		{svc(settingsv1.File_bob_settings_v1_settings_proto, "SettingsService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return settingsv1connect.NewSettingsServiceHandler(or(sv.Settings, settingsv1connect.SettingsServiceHandler(settingsv1connect.UnimplementedSettingsServiceHandler{})), o...)
		}},
		{svc(forgev1.File_bob_forge_v1_forge_proto, "ForgeService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return forgev1connect.NewForgeServiceHandler(or(sv.Forge, forgev1connect.ForgeServiceHandler(forgev1connect.UnimplementedForgeServiceHandler{})), o...)
		}},
		{svc(notifyv1.File_bob_notify_v1_notify_proto, "NotifyService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return notifyv1connect.NewNotifyServiceHandler(or(sv.Notify, notifyv1connect.NotifyServiceHandler(notifyv1connect.UnimplementedNotifyServiceHandler{})), o...)
		}},
		{svc(analyticsv1.File_bob_analytics_v1_analytics_proto, "AnalyticsService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return analyticsv1connect.NewAnalyticsServiceHandler(or(sv.Analytics, analyticsv1connect.AnalyticsServiceHandler(analyticsv1connect.UnimplementedAnalyticsServiceHandler{})), o...)
		}},
		{svc(workerv1.File_bob_worker_v1_worker_proto, "WorkerControlService"), func(o ...connect.HandlerOption) (string, http.Handler) {
			return workerv1connect.NewWorkerControlServiceHandler(or(sv.WorkerControl, workerv1connect.WorkerControlServiceHandler(workerv1connect.UnimplementedWorkerControlServiceHandler{})), o...)
		}},
	}
	var sds []protoreflect.ServiceDescriptor
	for _, m := range mounts {
		if m.sd == nil {
			return nil, errors.New("api: a service descriptor is missing from the generated code")
		}
		sds = append(sds, m.sd)
	}
	rules, err := RulesOf(sds...)
	if err != nil {
		return nil, err
	}
	s.rules = rules
	opts := []connect.HandlerOption{
		connect.WithInterceptors(guard{s: s}),
		connect.WithReadMaxBytes(4 << 20),
		connect.WithSendMaxBytes(16 << 20),
	}
	for _, m := range mounts {
		path, h := m.mk(opts...)
		s.mux.Handle(path, h)
	}
	return s, nil
}

func or[T any](v, def T) T {
	if any(v) == nil {
		return def
	}
	return v
}

// Rules is the rule table of every served method.
func (s *Server) Rules() Rules { return s.rules }

// ServeHTTP serves /api/<package>.<Service>/<Method>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rest
	r2.URL.RawPath = ""
	s.mux.ServeHTTP(w, r2)
}
