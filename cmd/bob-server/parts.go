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
	authParts(env, &parts) // identity needs parts.Authz (rbacParts)
	controllerParts(env, &parts)
	logParts(env, &parts) // I-B7: LogService, lake-maintenance; the returned LiveLogs goes to Attach
	if _, err := fleetParts(ctx, env, &parts); err != nil {
		return parts, err
	}
	return parts, nil
}

// workerParts wires the worker loop (internal/worker) into a worker process.
func workerParts(env *server.Env) (server.WorkerParts, error) {
	return workerLoop(env)
}
