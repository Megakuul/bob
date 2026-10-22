package fleetsrv

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/fleet"
	fleetv1 "github.com/megakuul/bob/internal/gen/bob/fleet/v1"
	"github.com/megakuul/bob/internal/gen/bob/fleet/v1/fleetv1connect"
	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/gen/bob/worker/v1/workerv1connect"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// FleetService serves bob.fleet.v1.FleetService over the fleet bucket: lists read the widx
// shards only (GET, no LIST; SPEC §05.5 "Fleet: workers"), the admin RPCs CAS worker/<wid>.
type FleetService struct {
	fleetv1connect.UnimplementedFleetServiceHandler
	F *Fleet
}

var stateNames = map[string]fleetv1.WorkerState{
	"active": fleetv1.WorkerState_WORKER_STATE_ACTIVE, "draining": fleetv1.WorkerState_WORKER_STATE_DRAINING,
	"stale": fleetv1.WorkerState_WORKER_STATE_STALE, "revoked": fleetv1.WorkerState_WORKER_STATE_REVOKED,
}

// state is a worker's state as the API shows it: an active or draining worker whose last
// heartbeat seen in widx is older than fleet.gone_after is stale (§05.6.3).
func (s *FleetService) state(e store.WorkerIndexEntry) fleetv1.WorkerState {
	st := stateNames[e.State]
	if (st == fleetv1.WorkerState_WORKER_STATE_ACTIVE || st == fleetv1.WorkerState_WORKER_STATE_DRAINING) &&
		!e.Heartbeat.IsZero() && s.F.cfg.Now().Sub(e.Heartbeat) > s.F.cfg.Params.GoneAfter {
		return fleetv1.WorkerState_WORKER_STATE_STALE
	}
	return st
}

func (s *FleetService) summary(wid string, e store.WorkerIndexEntry) *fleetv1.Worker {
	w := &fleetv1.Worker{Id: wid, State: s.state(e), Platform: e.Platform}
	if !e.Heartbeat.IsZero() {
		w.LastHeartbeat = timestamppb.New(e.Heartbeat)
	}
	return w
}

// ListWorkers implements FleetService.
func (s *FleetService) ListWorkers(ctx context.Context, req *connect.Request[fleetv1.ListWorkersRequest]) (*connect.Response[fleetv1.ListWorkersResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.PageSize)
	if err != nil {
		return nil, err
	}
	pos, err := api.DecodePageToken("workers", m.PageToken)
	if err != nil {
		return nil, err
	}
	all, err := store.WorkerIndex(s.F.cfg.Fleet, s.F.cfg.Params.WorkerShards).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(all))
	for wid, e := range all {
		if m.Platform != "" && e.Platform != m.Platform {
			continue
		}
		if m.Query != "" && !strings.Contains(wid, m.Query) {
			continue
		}
		if len(m.States) > 0 && !slices.Contains(m.States, s.state(e)) {
			continue
		}
		ids = append(ids, wid)
	}
	slices.Sort(ids)
	res := &fleetv1.ListWorkersResponse{}
	for i := int(min(pos, uint64(len(ids)))); i < len(ids) && len(res.Workers) < size; i++ {
		res.Workers = append(res.Workers, s.summary(ids[i], all[ids[i]]))
		if len(res.Workers) == size && i+1 < len(ids) {
			res.NextPageToken = api.EncodePageToken("workers", uint64(i+1))
		}
	}
	return connect.NewResponse(res), nil
}

// worker reads worker/<wid> and its heartbeat into the API form.
func (s *FleetService) worker(ctx context.Context, wid string) (*fleetv1.Worker, error) {
	if _, err := store.Segment(wid); err != nil {
		return nil, api.NotFound("worker")
	}
	rec, err := store.Get(ctx, s.F.cfg.Fleet, &store.WorkerRecord{Wid: wid})
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("worker")
	}
	if err != nil {
		return nil, err
	}
	e := store.WorkerIndexEntry{Platform: rec.Platform, State: rec.State}
	hb, err := store.Get(ctx, s.F.cfg.Fleet, &store.Heartbeat{Wid: wid})
	if err == nil && fleet.VerifyHeartbeat(rec.SigPub, hb) {
		e.Heartbeat = hb.At
	}
	w := s.summary(wid, e)
	w.Capabilities = rec.Caps
	if len(rec.Labels) > 0 {
		w.Labels = map[string]string{}
		for _, l := range rec.Labels {
			k, v, _ := strings.Cut(l, "=")
			w.Labels[k] = v
		}
	}
	if hb != nil {
		for _, j := range hb.Jobs {
			w.JobIds = append(w.JobIds, j.Job)
		}
	}
	return w, nil
}

// GetWorker implements FleetService.
func (s *FleetService) GetWorker(ctx context.Context, req *connect.Request[fleetv1.GetWorkerRequest]) (*connect.Response[fleetv1.GetWorkerResponse], error) {
	w, err := s.worker(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&fleetv1.GetWorkerResponse{Worker: w}), nil
}

// setState moves worker wid from one of from to state to (worker/<wid> and its widx entry).
func (s *FleetService) setState(ctx context.Context, wid, to string, from ...string) error {
	if _, err := store.Segment(wid); err != nil {
		return api.NotFound("worker")
	}
	_, err := store.CAS(ctx, s.F.cfg.Fleet, &store.WorkerRecord{Wid: wid}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		if cur.State == to {
			return nil, store.ErrNoChange
		}
		if !slices.Contains(from, cur.State) {
			return nil, api.FailedPrecondition("worker " + wid + " is " + cur.State)
		}
		n := *cur
		n.State = to
		return &n, nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return api.NotFound("worker")
	}
	if err != nil {
		return err
	}
	return s.index(ctx, wid, to)
}

// index updates the state of wid's widx entry.
func (s *FleetService) index(ctx context.Context, wid, state string) error {
	idx := store.WorkerIndex(s.F.cfg.Fleet, s.F.cfg.Params.WorkerShards)
	all, err := idx.All(ctx)
	if err != nil {
		return err
	}
	e := all[wid]
	e.State = state
	return idx.Add(ctx, wid, e)
}

// DrainWorker implements FleetService.
func (s *FleetService) DrainWorker(ctx context.Context, req *connect.Request[fleetv1.DrainWorkerRequest]) (*connect.Response[fleetv1.DrainWorkerResponse], error) {
	if err := s.setState(ctx, req.Msg.Id, "draining", "active", "stale"); err != nil {
		return nil, err
	}
	_ = s.F.Hub.Drain(req.Msg.Id) // a hint; the worker also sees its record
	w, err := s.worker(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&fleetv1.DrainWorkerResponse{Worker: w}), nil
}

// UndrainWorker implements FleetService.
func (s *FleetService) UndrainWorker(ctx context.Context, req *connect.Request[fleetv1.UndrainWorkerRequest]) (*connect.Response[fleetv1.UndrainWorkerResponse], error) {
	if err := s.setState(ctx, req.Msg.Id, "active", "draining"); err != nil {
		return nil, err
	}
	w, err := s.worker(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&fleetv1.UndrainWorkerResponse{Worker: w}), nil
}

// RevokeWorker implements FleetService (ADR 0013: generation + 1, effective ≤ 10 s).
func (s *FleetService) RevokeWorker(ctx context.Context, req *connect.Request[fleetv1.RevokeWorkerRequest]) (*connect.Response[fleetv1.RevokeWorkerResponse], error) {
	wid := req.Msg.Id
	if _, err := store.Segment(wid); err != nil {
		return nil, api.NotFound("worker")
	}
	err := s.F.Registrar.Revoke(ctx, wid, req.Msg.Reason)
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("worker")
	}
	if err != nil {
		return nil, err
	}
	if err := s.index(ctx, wid, "revoked"); err != nil {
		return nil, err
	}
	w, err := s.worker(ctx, wid)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&fleetv1.RevokeWorkerResponse{Worker: w}), nil
}

// WorkerControl serves bob.worker.v1.WorkerControlService (KIND_WORKER: the api guard has
// authenticated the worker token and its proof of possession).
type WorkerControl struct {
	workerv1connect.UnimplementedWorkerControlServiceHandler
	F *Fleet
}

// GetCheckoutCredential implements WorkerControlService: a checkout credential for the job the
// calling worker holds (owner + epoch), sealed to its KEM key (R-FLEET-006).
func (w *WorkerControl) GetCheckoutCredential(ctx context.Context, req *connect.Request[workerv1.GetCheckoutCredentialRequest]) (*connect.Response[workerv1.GetCheckoutCredentialResponse], error) {
	res, err := w.checkout(ctx, api.WorkerFrom(ctx), req.Msg.JobId, req.Msg.Epoch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

// checkout is GetCheckoutCredential for the authenticated worker wid.
func (w *WorkerControl) checkout(ctx context.Context, wid, jobID string, epoch int64) (*workerv1.GetCheckoutCredentialResponse, error) {
	if _, err := store.Segment(jobID); err != nil {
		return nil, api.InvalidArgument("job_id", "invalid job id")
	}
	co, err := w.F.Checkouts.Get(ctx, wid, jobID, epoch)
	if err != nil {
		var de *diag.Error
		switch {
		case strings.HasPrefix(err.Error(), "permission_denied: "):
			return nil, api.Forbidden(strings.TrimPrefix(err.Error(), "permission_denied: "))
		case errors.As(err, &de) && de.Code == 9008:
			return nil, api.Unavailable("checkout credentials are not available yet: " + de.Error())
		}
		return nil, err
	}
	rec, err := store.Get(ctx, w.F.cfg.Fleet, &store.WorkerRecord{Wid: wid})
	if err != nil {
		return nil, err
	}
	suite, err := fleet.ParseSuite(rec.Suite)
	if err != nil {
		return nil, err
	}
	sealed, err := fleet.SealCheckout(suite, rec.KEMPub, wid, jobID, epoch, fleet.CheckoutPlain{
		CloneURL: co.CloneURL, Username: co.Username, Token: co.Token, Expires: co.Expires.Unix()})
	if err != nil {
		return nil, err
	}
	return &workerv1.GetCheckoutCredentialResponse{SealedCheckout: sealed, ExpiresUnix: co.Expires.Unix()}, nil
}

// Attach implements WorkerControlService: the worker's control channel. The first message is
// Hello; the stream then carries wake-ups, cancel and drain requests to the worker and slot
// states and job events from it. Hints only: the bucket stays the truth.
func (w *WorkerControl) Attach(ctx context.Context, st *connect.BidiStream[workerv1.AttachRequest, workerv1.AttachResponse]) error {
	wid := api.WorkerFrom(ctx)
	first, err := st.Receive()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return api.InvalidArgument("hello", "the first Attach message must be Hello")
	}
	if hello.WorkerId != "" && hello.WorkerId != wid {
		return api.Forbidden("Hello names another worker than the token")
	}
	cfg, _ := store.Get(ctx, w.F.cfg.Fleet, store.FleetConfigKey())
	gen := int64(0)
	if cfg != nil {
		gen = cfg.Gen
	}
	conn := w.F.Hub.Attach(wid)
	defer conn.Detach()
	conn.SetSlots(int(hello.SlotsFree))
	if err := st.Send(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_Welcome{Welcome: &workerv1.Welcome{OrchestratorId: w.F.cfg.Name, FleetConfigGen: gen}}}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// connect streams allow Receive and Send to run concurrently.
	recvDone := safe.Go(func() error {
		defer cancel()
		for {
			m, err := st.Receive()
			if err != nil {
				return err
			}
			switch x := m.Msg.(type) {
			case *workerv1.AttachRequest_Slots:
				conn.SetSlots(int(x.Slots.SlotsFree))
			case *workerv1.AttachRequest_JobEvent:
				_ = conn.JobEvent(ctx, x.JobEvent.JobId, x.JobEvent.Epoch, x.JobEvent.Kind.String())
			}
		}
	})
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		var out *workerv1.AttachResponse
		select {
		case <-ctx.Done():
			// The worker closed its side (io.EOF) or the server is stopping: a normal end.
			<-recvDone
			return nil
		case <-ping.C:
			out = &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_Ping{Ping: &workerv1.Ping{UnixMs: time.Now().UnixMilli()}}}
		case line := <-conn.Recv():
			out = attachMessage(line)
		}
		if out == nil {
			continue
		}
		if err := st.Send(out); err != nil {
			return err
		}
	}
}

// attachMessage turns a Hub line ("wake <class> <slot> <job>", "cancel <job>", "drain",
// "subscribe <job>") into its Attach message.
func attachMessage(line string) *workerv1.AttachResponse {
	f := strings.Fields(line)
	if len(f) == 0 {
		return nil
	}
	switch {
	case f[0] == "wake" && len(f) == 4:
		slot, err := strconv.ParseUint(f[2], 10, 64)
		if err != nil {
			return nil
		}
		return &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_Wake{Wake: &workerv1.Wake{ClassId: f[1], Slot: slot, JobId: f[3]}}}
	case f[0] == "cancel" && len(f) == 2:
		return &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_CancelJob{CancelJob: &workerv1.CancelJob{JobId: f[1]}}}
	case f[0] == "drain":
		return &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_Drain{Drain: &workerv1.Drain{Drain: true}}}
	case f[0] == "subscribe" && len(f) == 2:
		return &workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveSubscribe{LiveSubscribe: &workerv1.LiveSubscribe{
			JobId: f[1], ExpiresUnixMs: time.Now().Add(15 * time.Second).UnixMilli()}}}
	}
	return nil
}
