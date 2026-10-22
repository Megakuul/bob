package fleetsrv

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	fleetv1 "github.com/megakuul/bob/internal/gen/bob/fleet/v1"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/tests/store/env"
)

func TestParamsFromEnv(t *testing.T) {
	p, err := ParamsFromEnv(func(k string) string {
		return map[string]string{"BOB_FLEET_POLL_INTERVAL": "2s", "BOB_FLEET_JOB_SHARDS": "8"}[k]
	})
	if err != nil || p.PollInterval != 2*time.Second || p.JobShards != 8 || p.LeaseTTL != 60*time.Second {
		t.Fatalf("got %+v, %v", p, err)
	}
	if _, err := ParamsFromEnv(func(k string) string {
		if k == "BOB_FLEET_HEARTBEAT" {
			return "soon"
		}
		return ""
	}); err == nil {
		t.Fatalf("a malformed duration was accepted")
	}
}

// TestFleet: two orchestrators that start at once end up with one signing key; a worker
// registers, the registrar approves it, ListWorkers shows it active (widx only), and a worker
// without the lease gets no checkout credential.
func TestFleet(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control, fl := be.Open(t, "control"), be.Open(t, "fleet")
	if control == nil || fl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fs := make([]*Fleet, 2)
	errs := make(chan error, 2)
	for i := range fs {
		go func() {
			var err error
			fs[i], err = New(ctx, Config{Name: []string{"o1", "o2"}[i], Control: control, Fleet: fl, Params: DefaultParams()})
			errs <- err
		}()
	}
	for range fs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if fs[0].Keys.Kid != fs[1].Keys.Kid {
		t.Fatalf("orchestrators sign with different keys: %s, %s", fs[0].Keys.Kid, fs[1].Keys.Kid)
	}
	again, err := New(ctx, Config{Name: "o3", Control: control, Fleet: fl})
	if err != nil || again.Keys.Kid != fs[0].Keys.Kid {
		t.Fatalf("a later orchestrator: %v, kid %v", err, again)
	}
	cfg, err := store.Get(ctx, fl, store.FleetConfigKey())
	if err != nil || cfg.Params.PollInterval != 30*time.Second {
		t.Fatalf("fleet/cfg params not published: %+v %v", cfg, err)
	}

	id, err := worker.NewIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id.Profile = store.WorkerProfile{Platform: "aarch64-linux"}
	if _, err := worker.Register(ctx, fl, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := fs[1].Registrar.Pass(ctx); err != nil || res.Approved != 1 {
		t.Fatalf("registrar pass: %+v %v", res, err)
	}
	svc := &FleetService{F: fs[0]}
	lr, err := svc.ListWorkers(ctx, connect.NewRequest(&fleetv1.ListWorkersRequest{}))
	if err != nil || len(lr.Msg.Workers) != 1 || lr.Msg.Workers[0].Id != id.ID() || lr.Msg.Workers[0].State != fleetv1.WorkerState_WORKER_STATE_ACTIVE {
		t.Fatalf("ListWorkers: %v %v", lr, err)
	}
	if _, err := svc.DrainWorker(ctx, connect.NewRequest(&fleetv1.DrainWorkerRequest{Id: id.ID()})); err != nil {
		t.Fatal(err)
	}
	lr, _ = svc.ListWorkers(ctx, connect.NewRequest(&fleetv1.ListWorkersRequest{States: []fleetv1.WorkerState{fleetv1.WorkerState_WORKER_STATE_DRAINING}}))
	if len(lr.Msg.Workers) != 1 {
		t.Fatalf("drained worker not listed as draining: %v", lr.Msg)
	}

	// A job owned by somebody else: no credential.
	if _, err := fs[0].AddClass(ctx, store.Class{Platform: "aarch64-linux"}); err != nil {
		t.Fatal(err)
	}
	class, _ := store.ClassID(store.Class{Platform: "aarch64-linux"})
	if _, _, err := fs[0].Enqueue(ctx, class, orchestrator.Job{ID: "01jjob00000000000000000001", Build: "b1"}); err != nil {
		t.Fatal(err)
	}
	wc := &WorkerControl{F: fs[0]}
	_, err = wc.checkout(ctx, id.ID(), "01jjob00000000000000000001", 1)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied || ce.Message() != "worker holds no lease on job 01jjob00000000000000000001" {
		t.Fatalf("GetCheckoutCredential without the lease: %v", err)
	}
}
