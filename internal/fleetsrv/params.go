// Package fleetsrv is the fleet side of a bob-server orchestrator (SPEC §05.6–§05.11): the
// orchestrator signing key, the fleet controllers of internal/orchestrator run as server
// components (registrar, job-reconciler, forge-sweeper), the Attach hub and wake-up dispatch,
// and the FleetService and WorkerControlService handlers.
package fleetsrv

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// DefaultParams are the fleet.* and log.* defaults of SPEC §05.11.1.
func DefaultParams() store.FleetParams {
	return store.FleetParams{
		PollInterval: 30 * time.Second, Heartbeat: 15 * time.Second, LeaseTTL: 60 * time.Second,
		Skew: 5 * time.Second, ReslotAfter: 60 * time.Second, GoneAfter: 5 * time.Minute,
		MaxAttempts: 3, JobShards: 16, WorkerShards: 4,
		FlushInterval: 30 * time.Second, LogShards: 4, ChunkInterval: 250 * time.Millisecond,
	}
}

// ParamsFromEnv reads the fleet.* and log.* settings (BOB_FLEET_POLL_INTERVAL, …) over the
// defaults. A malformed value is an error naming the variable.
func ParamsFromEnv(getenv func(string) string) (store.FleetParams, error) {
	p := DefaultParams()
	dur := func(key string, dst *time.Duration) error {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("%s must be a duration between 0 and 24h (e.g. 30s), got %q", key, v)
		}
		*dst = d
		return nil
	}
	num := func(key string, dst *int, hi int) error {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > hi {
			return fmt.Errorf("%s must be a number from 1 to %d, got %q", key, hi, v)
		}
		*dst = n
		return nil
	}
	for _, err := range []error{
		dur("BOB_FLEET_POLL_INTERVAL", &p.PollInterval),
		dur("BOB_FLEET_HEARTBEAT", &p.Heartbeat),
		dur("BOB_FLEET_LEASE_TTL", &p.LeaseTTL),
		dur("BOB_FLEET_SKEW", &p.Skew),
		dur("BOB_FLEET_RESLOT_AFTER", &p.ReslotAfter),
		dur("BOB_FLEET_GONE_AFTER", &p.GoneAfter),
		num("BOB_FLEET_MAX_ATTEMPTS", &p.MaxAttempts, 100),
		num("BOB_FLEET_JOB_SHARDS", &p.JobShards, 256),
		num("BOB_FLEET_WORKER_SHARDS", &p.WorkerShards, 256),
		dur("BOB_LOG_FLUSH_INTERVAL", &p.FlushInterval),
		num("BOB_LOG_SHARDS", &p.LogShards, 256),
		dur("BOB_LIVE_LOG_CHUNK_INTERVAL", &p.ChunkInterval),
	} {
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

// Merge fills the zero fields of p from def (a FleetConfig written by an older orchestrator
// may lack newer parameters).
func Merge(p, def store.FleetParams) store.FleetParams {
	d := func(v *time.Duration, w time.Duration) {
		if *v <= 0 {
			*v = w
		}
	}
	n := func(v *int, w int) {
		if *v <= 0 {
			*v = w
		}
	}
	d(&p.PollInterval, def.PollInterval)
	d(&p.Heartbeat, def.Heartbeat)
	d(&p.LeaseTTL, def.LeaseTTL)
	d(&p.Skew, def.Skew)
	d(&p.ReslotAfter, def.ReslotAfter)
	d(&p.GoneAfter, def.GoneAfter)
	n(&p.MaxAttempts, def.MaxAttempts)
	n(&p.JobShards, def.JobShards)
	n(&p.WorkerShards, def.WorkerShards)
	d(&p.FlushInterval, def.FlushInterval)
	n(&p.LogShards, def.LogShards)
	d(&p.ChunkInterval, def.ChunkInterval)
	return p
}
