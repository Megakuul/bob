package main

import (
	"context"
	"errors"

	"github.com/megakuul/bob/internal/fleetsrv"
	"github.com/megakuul/bob/internal/isolation"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// fleetParts wires the fleet side of an orchestrator (SPEC §05.6–§05.11): the signing key,
// the registrar (pass every 2 s, token rotation every 60 s), the job-reconciler (10 s), the
// forge-sweeper (60 s), worker authentication, FleetService and WorkerControlService (Attach
// feeding the hub the dispatcher wakes workers over; GetCheckoutCredential). The returned
// Fleet is what the build controller enqueues jobs through.
func fleetParts(ctx context.Context, env *server.Env, parts *server.OrchestratorParts) (*fleetsrv.Fleet, error) {
	params, err := fleetsrv.ParamsFromEnv(env.Getenv)
	if err != nil {
		return nil, &server.ConfigError{Msg: err.Error()}
	}
	f, err := fleetsrv.New(ctx, fleetsrv.Config{Name: env.Config.Name, Control: env.Storage.Control, Fleet: env.Storage.Fleet,
		Params: params, Now: env.Now, Log: env.Log})
	if err != nil {
		return nil, err
	}
	for _, c := range f.Components() {
		parts.Controllers = append(parts.Controllers, c)
	}
	parts.Workers = f.Registrar
	parts.Services.Fleet = &fleetsrv.FleetService{F: f}
	parts.Services.WorkerControl = &fleetsrv.WorkerControl{F: f}
	return f, nil
}

// workerLoop is the worker process: registration, the claim loop and the job containers.
func workerLoop(env *server.Env) (server.WorkerParts, error) {
	params, err := fleetsrv.ParamsFromEnv(env.Getenv)
	if err != nil {
		return server.WorkerParts{}, &server.ConfigError{Msg: err.Error()}
	}
	p, err := worker.NewProcess(worker.ProcessConfig{
		Fleet: env.Storage.Fleet, Name: env.Config.Name, StateDir: env.Config.StateDir, Slots: env.Config.Slots,
		Params: func(pr store.FleetParams) store.FleetParams { return fleetsrv.Merge(pr, params) },
		Now:    env.Now, Log: env.Log,
	})
	if err != nil {
		return server.WorkerParts{}, err
	}
	return server.WorkerParts{Loop: p}, nil
}

// workerHostCheck is the worker's build isolation check (R-FLEET-008: cgroup v2 with a usable
// bob-worker.slice, Landlock ABI ≥ 4, the uid range of Nix's auto-allocate-uids). It runs
// before the configuration is read; a failure is BOB-E-7010 (exit 7).
func workerHostCheck() error {
	err := isolation.Check()
	var r *isolation.Refusal
	if errors.As(err, &r) {
		d := diag.New(7010, "reason", r.Reason)
		return &server.HostRefusal{Code: 7010, Msg: d.Error(), Hint: d.Hint() + " (SPEC §05.8: cgroup v2, Landlock ABI ≥ 4, Nix auto-allocate-uids)"}
	}
	return err
}
