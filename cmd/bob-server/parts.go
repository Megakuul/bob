package main

import (
	"context"

	"github.com/megakuul/bob/internal/server"
)

// parts.go is the single composition point of bob-server (owner: I-B6). Each feature wires
// itself in a file parts_<feature>.go and adds one call here.

// orchestratorParts wires the feature packages into an orchestrator process: fleet
// controllers and services, identity, RBAC, worker authentication.
func orchestratorParts(ctx context.Context, env *server.Env) (server.OrchestratorParts, error) {
	var parts server.OrchestratorParts
	rbacParts(env, &parts)
	authParts(env, &parts)             // identity needs parts.Authz (rbacParts)
	cacheParts(ctx, env, &parts)       // /nix/ cache, promotion, signing keys (needs identity)
	cs := controllerParts(env, &parts) // membership, key, index-repair, retention, staging sweep
	live := logParts(env, &parts)      // LogService, lake-maintenance; LiveLogs feeds Attach
	f, err := fleetParts(ctx, env, &parts, cs, live)
	if err != nil {
		return parts, err
	}
	fw := forgeParts(env, &parts)                                   // forges, /hooks/, checkout minter, notifications (after fleetParts)
	pw := projectParts(ctx, env, &parts, fw)                        // ProjectService, settings, cache and list RPCs (I-B12)
	if err := buildParts(ctx, env, &parts, f, fw, pw); err != nil { // build controller, BuildService, JobService (I-B11)
		return parts, err
	}
	return parts, nil
}

// workerParts wires the worker loop (internal/worker) into a worker process.
func workerParts(env *server.Env) (server.WorkerParts, error) {
	wp, err := workerLoop(env)
	return workerJobs(env, wp, err) // sources and staging of the job runner (I-B11)
}
