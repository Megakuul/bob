package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/store"
)

// LakeMaintenance returns the `lake-maintenance` controller (every 30 s, lease
// ctl/lake-maintenance, SPEC §05.11): compaction of the recent log and analytics partitions
// with the R-LOG-100 workarounds. Only the leader compacts, and m is used by nothing else.
func LakeMaintenance(m *logstore.Maintenance, fleet *store.Bucket, owner string, skew time.Duration, now func() time.Time, log *slog.Logger) *Controller {
	return &Controller{ID: CtlLakeMaintenance, Fleet: fleet, Owner: owner, Interval: logstore.MaintenanceInterval,
		Skew: skew, Now: now, Log: log, Pass: func(ctx context.Context, _ *Run) error {
			_, err := m.Pass(ctx)
			return err
		}}
}
