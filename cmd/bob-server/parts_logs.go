package main

import (
	"context"
	"time"

	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store/s3client"
)

// logParts wires the logs of an orchestrator (SPEC §05.10, M14.1/M14.2): LogService (GetLog,
// FollowLog, SearchLog over the lake and the live pull) and the lake-maintenance controller.
// The returned LiveLogs is the live path that the Attach stream feeds (LiveLogs.Attached,
// LiveLogs.Chunk); nil when the analytics bucket cannot be reached, in which case the log API
// stays unimplemented and everything else runs.
func logParts(env *server.Env, parts *server.OrchestratorParts) *orchestrator.LiveLogs {
	if env.Storage == nil || env.Storage.Control == nil || env.Storage.Fleet == nil {
		return nil
	}
	st := env.Storage.Config
	c, err := s3client.New(context.Background(), s3client.Options{Endpoint: st.Endpoint, Region: st.Region})
	if err != nil {
		env.Log.Error("logs not available", "err", err.Error())
		return nil
	}
	reader := logstore.NewReader(c, st.Analytics)
	live := orchestrator.NewLiveLogs(env.Config.Name, env.Now, env.Log)
	src := logstore.NewSources(reader, live, orchestrator.BuildJobs(env.Storage.Control, env.Storage.Fleet))
	src.Now, src.Log = env.Now, env.Log
	parts.Services.Log = &orchestrator.LogService{Sources: src, Log: env.Log}
	parts.Controllers = append(parts.Controllers, orchestrator.LakeMaintenance(
		logstore.NewMaintenance(c, st.Analytics, env.Now), env.Storage.Fleet, env.Config.Name,
		setting(env, "BOB_FLEET_SKEW", 5*time.Second), env.Now, env.Log))
	return live
}
