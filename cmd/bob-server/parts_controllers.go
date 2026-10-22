package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store/sweep"
)

// controllerSet is what the HA milestone (M14.3/M14.4) contributes to an orchestrator: the
// membership (ListOrchestrators reads it), the signing keys, and the settings the other
// leader-only controllers share. Fleet adds the fleet controllers once their parts exist.
type controllerSet struct {
	env        *server.Env
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
	cs := &controllerSet{env: env, Skew: setting(env, "BOB_FLEET_SKEW", 5*time.Second), JobShards: intSetting(env, "BOB_FLEET_JOB_SHARDS", 16)}
	owner, ctl, fl := env.Config.Name, env.Storage.Control, env.Storage.Fleet
	cs.Membership = &orchestrator.Membership{Fleet: fl, ID: owner, Endpoint: endpoint(env.Config), Version: server.Version(),
		Now: env.Now, Log: env.Log}
	cs.Keys = &orchestrator.Keys{Control: ctl, Fleet: fl, Now: env.Now}
	repair := &orchestrator.IndexRepair{Control: ctl, Fleet: fl, JobShards: cs.JobShards, Log: env.Log}
	retention := &orchestrator.Retention{Control: ctl, Fleet: fl, Log: env.Log,
		Logs: setting(env, "BOB_RETENTION_LOGS", 0), Analytics: setting(env, "BOB_RETENTION_ANALYTICS", 0),
		Builds: setting(env, "BOB_RETENTION_BUILDS", 0), Cache: setting(env, "BOB_RETENTION_CACHE", 0)}
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

// Fleet returns the fleet controllers over the given parts (nil parts are skipped): the
// registrar, the job-reconciler and the forge-sweeper, under their leases.
func (cs *controllerSet) Fleet(reg *orchestrator.Registrar, rec *orchestrator.Reconciler, co *orchestrator.Checkouts) []server.Component {
	if cs == nil {
		return nil
	}
	env := cs.env
	owner, fl := env.Config.Name, env.Storage.Fleet
	var out []server.Component
	if reg != nil {
		out = append(out, orchestrator.RegistrarController(reg, fl, owner, cs.Skew, env.Now))
	}
	if rec != nil {
		out = append(out, orchestrator.ReconcilerController(rec, owner, cs.Skew, env.Now))
	}
	if co != nil {
		out = append(out, orchestrator.ForgeSweeperController(co, fl, owner, cs.Skew, env.Now))
	}
	return out
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
