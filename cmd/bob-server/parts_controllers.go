package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/overview"
	"github.com/megakuul/bob/internal/release"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/store/sweep"
)

// controllerSet is what the HA milestone (M14.3/M14.4) contributes to an orchestrator: the
// membership (ListOrchestrators reads it), the signing keys, and the settings the other
// leader-only controllers share. The fleet controllers plug in with
// orchestrator.RegistrarController, ReconcilerController and ForgeSweeperController and
// these settings.
type controllerSet struct {
	Membership *orchestrator.Membership
	Keys       *orchestrator.Keys
	Skew       time.Duration
	JobShards  int
}

// controllerParts adds the controllers of SPEC §05.11 that this milestone owns: membership
// (heartbeat for every orchestrator, pruning under ctl/membership), key, index-repair,
// retention and the staging sweep. Every controller runs on every orchestrator and works only
// while it holds ctl/<name> (orchestrator.Controller).
func controllerParts(env *server.Env, parts *server.OrchestratorParts) *controllerSet {
	if env.Storage == nil || env.Storage.Control == nil || env.Storage.Fleet == nil {
		return nil // Open never lets this happen for an orchestrator
	}
	cs := &controllerSet{Skew: setting(env, "BOB_FLEET_SKEW", 5*time.Second), JobShards: intSetting(env, "BOB_FLEET_JOB_SHARDS", 16)}
	owner, ctl, fl := env.Config.Name, env.Storage.Control, env.Storage.Fleet
	cs.Membership = &orchestrator.Membership{Fleet: fl, ID: owner, Endpoint: endpoint(env.Config), Version: server.Version(),
		Now: env.Now, Log: env.Log}
	cs.Keys = &orchestrator.Keys{Control: ctl, Fleet: fl, Now: env.Now, Log: env.Log}
	repair := &orchestrator.IndexRepair{Control: ctl, Fleet: fl, JobShards: cs.JobShards, Log: env.Log}
	retention := &orchestrator.Retention{Control: ctl, Fleet: fl, Log: env.Log,
		Logs: setting(env, "BOB_RETENTION_LOGS", 0), Analytics: setting(env, "BOB_RETENTION_ANALYTICS", 0),
		Builds: setting(env, "BOB_RETENTION_BUILDS", 0), Cache: setting(env, "BOB_RETENTION_CACHE", 0),
		CacheSetting: cacheSetting(ctl)}
	if gc, err := cacheGC(env); err != nil {
		env.Log.Error("cache GC not available", "err", err.Error())
	} else {
		retention.CacheGC = gc
	}
	parts.Controllers = append(parts.Controllers,
		cs.Membership,
		cs.Membership.Controller(cs.Skew),
		cs.Keys.Controller(owner, cs.Skew),
		repair.Controller(owner, cs.Skew, env.Now),
		retention.Controller(owner, cs.Skew, env.Now),
	)
	st := env.Storage.Config
	if sw, err := sweep.NewStaging(context.Background(), st.Endpoint, st.Region, st.Artifacts); err != nil {
		env.Log.Error("staging sweep not available", "err", err.Error())
	} else {
		parts.Controllers = append(parts.Controllers, (&orchestrator.StagingSweep{Sweep: sw, Log: env.Log}).Controller(fl, owner, cs.Skew, env.Now))
	}
	return cs
}

// cacheGC is the retention controller's cache GC over the artifacts bucket; releases that are
// not deleted are found through release.Releases.Retained.
func cacheGC(env *server.Env) (*orchestrator.CacheGC, error) {
	st := env.Storage.Config
	obj, err := store.OpenObjects(context.Background(), s3client.Options{Endpoint: st.Endpoint, Region: st.Region}, st.Artifacts)
	if err != nil {
		return nil, err
	}
	nixVis := cache.NewVisStore(obj, cache.NixVisPrefix, env.Now)
	rel, err := release.New(release.Config{Control: env.Storage.Control, Objects: obj, NixVis: nixVis,
		OCIVis: cache.NewVisStore(obj, cache.OCIVisPrefix, env.Now), Now: env.Now, Log: env.Log})
	if err != nil {
		return nil, err
	}
	return &orchestrator.CacheGC{Control: env.Storage.Control, Objects: obj, Vis: nixVis, Now: env.Now, Log: env.Log,
		Retained: func(ctx context.Context) (map[string]bool, error) {
			paths, _, _, err := rel.Retained(ctx)
			return paths, err
		}}, nil
}

// cacheSetting is retention.cache_unreferenced_days of the server settings (SettingsService)
// when an admin set it, an explicit 0 included; unset, the retention.cache configuration
// (BOB_RETENTION_CACHE, default 90 d) applies.
func cacheSetting(ctl *store.Bucket) func(ctx context.Context) (time.Duration, bool) {
	settings := &overview.Settings{Control: ctl}
	var c store.Cache[overview.SettingsRecord]
	return func(ctx context.Context) (time.Duration, bool) {
		rec, _, err := store.GetCached(ctx, ctl, &c, &overview.SettingsRecord{Name: store.ID("server")})
		if err != nil {
			return 0, false // absent or unreadable: the configuration
		}
		set := (rec.CacheNow != nil && rec.CacheNow.Value()) || (rec.CacheDays != nil && rec.CacheDays.Value() > 0)
		if !set {
			return 0, false
		}
		return time.Duration(settings.CacheDays(ctx)) * 24 * time.Hour, true
	}
}

// endpoint is the URL workers attach to: server.base_url, else the listen address (with the
// host name when it binds every interface).
func endpoint(c server.Config) string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return "http://" + c.Listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if h, err := os.Hostname(); err == nil {
			host = h
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// setting reads a duration setting: a Go duration or a number of days ("30d"); an invalid
// value is logged and the default kept.
func setting(env *server.Env, key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(env.Getenv(key))
	if v == "" {
		return def
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	} else if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	env.Log.Warn("invalid setting ignored", "key", key, "value", v, "default", def.String())
	return def
}

func intSetting(env *server.Env, key string, def int) int {
	v := strings.TrimSpace(env.Getenv(key))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 256 {
		return n
	}
	env.Log.Warn("invalid setting ignored", "key", key, "value", v, "default", def)
	return def
}
