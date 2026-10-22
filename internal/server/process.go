package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/megakuul/bob/internal/api"
	bhttp "github.com/megakuul/bob/internal/http"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// ShutdownGrace is how long open requests and components get to finish after a stop signal.
const ShutdownGrace = 10 * time.Second

// Env is what a started process hands to its parts.
type Env struct {
	Config  Config
	Storage *store.Storage // opened and conformance-probed (R-STORE-102, R-SEC-102)
	Now     func() time.Time
	Log     *slog.Logger
	Getenv  func(string) string
	ln      net.Listener // orchestrator: server.listen, bound before the storage checks
}

// HostRefusal is a worker host that cannot run builds safely (BOB-E-7009, 7010). bob-server
// prints "✗ <Msg>" and "hint: <Hint>" and exits with the host class status (7).
type HostRefusal struct {
	Code      diag.Code
	Msg, Hint string
}

func (r *HostRefusal) Error() string { return r.Msg }

// ExitCode is the exit status of the host class (SPEC §02.3: 7007–7010 exit 7).
func (r *HostRefusal) ExitCode() int { return diag.ExitCode([]*diag.Error{diag.New(r.Code)}) }

// Open runs the start-up checks of mode and reads the configuration. A worker first checks
// its host (root, D56; then hostCheck, the build isolation check of R-FLEET-008, when given),
// before it reads any configuration or touches storage. An orchestrator binds server.listen
// before the storage checks: clients that connect while it starts wait in the backlog
// instead of being refused. Then storage conformance and, for an orchestrator with
// storage.worker_probe_credential, the policy self-check run. Refusals are a *HostRefusal,
// *ConfigError or *store.Refusal; the caller prints them and exits.
func Open(ctx context.Context, mode string, getenv func(string) string, log *slog.Logger, hostCheck func() error) (*Env, error) {
	if mode == ModeWorker {
		if os.Geteuid() != 0 {
			return nil, &HostRefusal{Code: 7009, Msg: diag.New(7009).Error(),
				Hint: "run it as a systemd system service or a privileged container"}
		}
		if hostCheck != nil {
			if err := hostCheck(); err != nil {
				return nil, err
			}
		}
	}
	cfg, err := ConfigFromEnv(mode, getenv)
	if err != nil {
		return nil, err
	}
	env := &Env{Config: cfg, Now: Clock(getenv), Log: log, Getenv: getenv}
	if mode == ModeOrchestrator {
		// A busy port is reported only after the storage checks: a refusal (exit 78) says
		// more about a broken deployment than a port clash, and must not be hidden by one.
		env.ln, _ = net.Listen("tcp", cfg.Listen)
	}
	if env.Storage, err = store.OpenStorage(ctx, cfg.Storage, mode); err != nil {
		if env.ln != nil {
			_ = env.ln.Close()
		}
		return nil, err
	}
	if mode == ModeOrchestrator && env.ln == nil {
		if env.ln, err = net.Listen("tcp", cfg.Listen); err != nil {
			return nil, fmt.Errorf("server.listen %s: %w", cfg.Listen, err)
		}
	}
	return env, nil
}

// OrchestratorParts are what the feature packages contribute to an orchestrator. Every field
// is optional; empty parts give an orchestrator that serves the API frame only.
type OrchestratorParts struct {
	Controllers []Component             // registrar, reconciler, build controller, …
	Services    api.Services            // service implementations (nil fields: built-in/Unimplemented)
	Authn       []api.Authenticator     // identity (M15.2); asked before the bootstrap token
	Authz       api.Authorizer          // RBAC (M15.4); nil = admin bindings only
	Resolve     api.Resolver            // build/job/release/repository → project
	Workers     api.WorkerAuthenticator // worker tokens (orchestrator.Registrar)
	Routes      map[string]http.Handler // further HTTP handlers by mux pattern (OAuth, OIDC, …)
	// Bootstrap, when set, makes the bootstrap admin credential (BOB_ADMIN_TOKEN, -tags
	// bobtest only) a real admin user of the identity (R-AUTH-003); without it the token is a
	// static admin@* principal.
	Bootstrap func(ctx context.Context, token string) error
}

// WorkerParts are what internal/worker contributes to a worker process.
type WorkerParts struct {
	Loop Component // the root worker loop; nil until it exists
}

// listener is a critical component serving one HTTP listener.
type listener struct {
	name string
	srv  *bhttp.Server
}

func (l listener) Name() string                  { return l.name }
func (l listener) Critical() bool                { return true }
func (l listener) Run(ctx context.Context) error { return l.srv.Serve(ctx, ShutdownGrace) }

// Version is the module version of this binary ("(devel)" for source builds).
func Version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// Orchestrator serves the API and runs the controllers until ctx is done.
func Orchestrator(ctx context.Context, env *Env, parts OrchestratorParts) error {
	cfg, log := env.Config, env.Log
	authn := api.Chain(parts.Authn)
	if cfg.AdminToken != "" {
		log.Warn("bootstrap admin token (BOB_ADMIN_TOKEN) is active; unset it once real admins exist")
	}
	if cfg.AdminToken != "" && parts.Bootstrap != nil {
		if err := parts.Bootstrap(ctx, cfg.AdminToken); err != nil {
			_ = env.ln.Close()
			return fmt.Errorf("bootstrap admin: %w", err)
		}
	} else if cfg.AdminToken != "" {
		authn = append(authn, &api.StaticToken{Token: cfg.AdminToken, Principal: api.Principal{
			ID: "sa:bootstrap", DisplayName: "bootstrap admin", Scope: "api",
			Bindings: []api.Binding{{Role: "admin", Project: "*"}},
		}})
	}
	a, err := api.New(api.Config{
		Services: parts.Services, Authn: authn, Authz: parts.Authz, Resolve: parts.Resolve,
		Workers: parts.Workers, Log: log,
		Info: api.Info{Name: cfg.Name, BaseURL: cfg.BaseURL, Version: Version()},
	})
	if err != nil {
		_ = env.ln.Close()
		return err
	}
	main := bhttp.New(env.ln, bhttp.Handler(bhttp.Routes{API: a, Patterns: parts.Routes}, log), log)
	comps := []Component{listener{name: "listener " + cfg.Listen, srv: main}}
	if cfg.AdminListen != "off" {
		adm, err := bhttp.Listen(cfg.AdminListen, bhttp.AdminHandler(func() error { return nil }, log), log)
		switch {
		case err == nil:
			comps = append(comps, listener{name: "admin listener " + cfg.AdminListen, srv: adm})
		case env.Getenv("BOB_SERVER_ADMIN_LISTEN") != "":
			return err
		default:
			// The default admin address is busy (e.g. several orchestrators on one host):
			// health and metrics are auxiliary, the API is not.
			log.Warn("admin listener not started", "addr", cfg.AdminListen, "err", err.Error())
		}
	}
	comps = append(comps, parts.Controllers...)
	log.Info("orchestrator started", "name", cfg.Name, "listen", main.Addr().String(), "controllers", len(parts.Controllers))
	err = Run(ctx, log, ShutdownGrace, comps...)
	log.Info("orchestrator stopped", "name", cfg.Name)
	return err
}

// Worker runs the worker loop until ctx is done.
func Worker(ctx context.Context, env *Env, parts WorkerParts) error {
	log := env.Log
	loop := parts.Loop
	if loop == nil {
		// A worker that cannot run jobs must not look healthy (start-up checks did run).
		return diag.NotImplemented("bob-server worker loop")
	}
	log.Info("worker started", "name", env.Config.Name, "slots", env.Config.Slots, "orchestrators", fmt.Sprint(env.Config.OrchestratorAddrs))
	err := Run(ctx, log, ShutdownGrace, loop)
	log.Info("worker stopped", "name", env.Config.Name)
	return err
}
