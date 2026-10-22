package main

import (
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// orchestratorParts wires the feature packages into an orchestrator process: controllers
// (internal/orchestrator), service implementations, identity, RBAC and worker
// authentication. Each milestone adds its part here.
func orchestratorParts(*server.Env) server.OrchestratorParts {
	return server.OrchestratorParts{}
}

// workerParts wires the worker loop (internal/worker) into a worker process.
func workerParts(*server.Env) server.WorkerParts {
	return server.WorkerParts{}
}

// workerHostCheck is the worker's build isolation check (R-FLEET-008: cgroup v2, Landlock,
// user namespaces; BOB-E-7010 as a *server.HostRefusal). It runs before the configuration is
// read. The worker isolation milestone (M13.7) replaces the stub.
func workerHostCheck() error {
	return diag.NotImplemented("bob-server worker isolation check")
}
