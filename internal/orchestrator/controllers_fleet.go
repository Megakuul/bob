package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// rotateEvery is the registrar's token rotation sweep period over widx (§05.11).
const rotateEvery = 60 * time.Second

// RegistrarController runs reg as the registrar controller (2 s, lease ctl/registrar; the
// registrar keeps its own cursor in ctl/registrar.cu): approve registrations every pass, the
// token rotation sweep every 60 s.
func RegistrarController(reg *Registrar, fleet *store.Bucket, owner string, skew time.Duration, now func() time.Time) *Controller {
	var rotated time.Time
	return &Controller{ID: CtlRegistrar, Fleet: fleet, Owner: owner, Interval: 2 * time.Second, Skew: skew, Now: now,
		Pass: func(ctx context.Context, r *Run) error {
			_, err := reg.Pass(ctx)
			if r.Now.Sub(rotated) >= rotateEvery {
				_, rerr := reg.Rotate(ctx)
				if rerr == nil {
					rotated = r.Now
				}
				err = errors.Join(err, rerr)
			}
			return err
		}}
}

// ReconcilerController runs rec as the job-reconciler controller (10 s, lease
// ctl/job-reconciler). The runner holds the lease, so rec.UseLease is switched off.
func ReconcilerController(rec *Reconciler, owner string, skew time.Duration, now func() time.Time) *Controller {
	rec.UseLease = false
	return &Controller{ID: CtlJobReconciler, Fleet: rec.Fleet, Owner: owner, Interval: leaseInterval, Skew: skew, Now: now,
		Pass: func(ctx context.Context, _ *Run) error {
			_, err := rec.Pass(ctx)
			return err
		}}
}

// ForgeSweeperController runs the forge-sweeper (60 s, lease ctl/forge-sweeper): revoke the
// expired per-job forge tokens of ftok/* (D62, R-FLEET-006).
func ForgeSweeperController(co *Checkouts, fleet *store.Bucket, owner string, skew time.Duration, now func() time.Time) *Controller {
	return &Controller{ID: CtlForgeSweeper, Fleet: fleet, Owner: owner, Interval: time.Minute, Skew: skew, Now: now,
		Pass: func(ctx context.Context, _ *Run) error {
			_, err := co.Sweep(ctx)
			return err
		}}
}
