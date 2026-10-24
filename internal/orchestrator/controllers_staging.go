package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/sweep"
)

// StagingSweep is the hourly staging sweep controller (R-STORE-105, R-STORE-101): it installs
// the 7-day lifecycle rule on staging/ once, and lists staging/ to delete objects older than
// 7 days (the only LIST of the artifacts bucket outside the lake sweep).
type StagingSweep struct {
	Sweep *sweep.Staging
	Log   *slog.Logger

	lifecycle bool
}

// Pass runs one sweep.
func (x *StagingSweep) Pass(ctx context.Context, r *Run) error {
	log := x.Log
	if log == nil {
		log = slog.Default()
	}
	if !x.lifecycle {
		applied, err := x.Sweep.EnsureLifecycle(ctx)
		if err != nil {
			log.Warn("staging lifecycle rule not installed", "err", err.Error())
		} else {
			x.lifecycle = true
			log.Info("staging lifecycle rule", "applied", applied)
		}
	}
	n, err := x.Sweep.Sweep(ctx, r.Now, sweep.StagingMaxAge)
	if n > 0 {
		log.Info("staging sweep", "deleted", n)
	}
	return err
}

// Controller returns the staging sweep controller (hourly, lease ctl/staging-sweep).
func (x *StagingSweep) Controller(fleet *store.Bucket, owner string, skew time.Duration, now func() time.Time) *Controller {
	return &Controller{ID: CtlStagingSweep, Fleet: fleet, Owner: owner, Interval: time.Hour, Skew: skew,
		Now: now, Persist: true, Pass: x.Pass, Log: x.Log}
}
