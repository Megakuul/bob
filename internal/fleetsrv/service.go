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
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// FleetService serves bob.fleet.v1.FleetService over the fleet bucket: lists read the widx
// shards only (GET, no LIST; SPEC §05.5 "Fleet: workers"), the admin RPCs CAS worker/<wid>.
type FleetService struct {
	fleetv1connect.UnimplementedFleetServiceHandler
	F *Fleet
	// Members lists the orchestrators (the membership controller, M14.3); nil: none listed.
	Members *orchestrator.Membership
}

// ListOrchestrators implements FleetService: the members of fleet/cfg.m with their heartbeat
// and the controller leases they hold.
func (s *FleetService) ListOrchestrators(ctx context.Context, _ *connect.Request[fleetv1.ListOrchestratorsRequest]) (*connect.Response[fleetv1.ListOrchestratorsResponse], error) {
	res := &fleetv1.ListOrchestratorsResponse{}
	if s.Members == nil {
		return connect.NewResponse(res), nil
	}
	ms, err := s.Members.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		o := &fleetv1.Orchestrator{Id: m.ID, Endpoint: m.Endpoint, Version: m.Version, LeaderOf: m.LeaderOf}
		if !m.Started.IsZero() {
			o.StartedAt = timestamppb.New(m.Started)
		}
		if !m.Heartbeat.IsZero() {
			o.LastHeartbeat = timestamppb.New(m.Heartbeat)
		}
		res.Orchestrators = append(res.Orchestrators, o)
	}
	return connect.NewResponse(res), nil
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
	rec, err := store.Get(ctx, s.F.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("worker")
	}
	if err != nil {
		return nil, err
	}
	e := store.WorkerIndexEntry{Platform: rec.Platform.Value(), State: rec.State.Value()}
	hb, err := store.Get(ctx, s.F.cfg.Fleet, &store.Heartbeat{Wid: store.ID(wid)})
	if err == nil && fleet.VerifyHeartbeat(rec.SigPub.Value(), hb) {
		e.Heartbeat = hb.At.Value()
	}
	w := s.summary(wid, e)
	w.Capabilities = rec.Caps.Value()
	if len(rec.Labels.Value()) > 0 {
		w.Labels = map[string]string{}
		for _, l := range rec.Labels.Value() {
			k, v, _ := strings.Cut(l, "=")
			w.Labels[k] = v
		}
	}
	if hb != nil {
		for _, j := range hb.Jobs.Value() {
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
	err := store.Change(ctx, s.F.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)}, func(cur *store.WorkerRecord) (*store.WorkerRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		if cur.State.Value() == to {
			return nil, store.ErrNoChange
		}
		if !slices.Contains(from, cur.State.Value()) {
			return nil, api.FailedPrecondition("worker " + wid + " is " + cur.State.Value())
		}
		n := *cur
		n.State = store.Set(to)
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
	// Live is the live log path (M14.2): it subscribes to a worker's job logs over its
	// Attach stream and takes the LiveChunks; nil: no live logs.
	Live *orchestrator.LiveLogs
	// Key is the orchestrator's pinned key (R-SRV-102): Attach answers the worker's nonce with
	// a proof of it in the response header; nil: no proof, and workers refuse the stream.
	Key *fleet.ServerKey
}

// GetCheckoutCredential implements WorkerControlService: for the job the calling worker holds
// (owner + epoch), a checkout credential sealed to its KEM key (R-FLEET-006; jobs with a
// repository) and the job's cache download token (§05.9.1; every job of a project).
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
	lease, err := w.F.Checkouts.Lease(ctx, wid, jobID, epoch)
	if err != nil {
		return nil, checkoutError(err)
	}
	res := &workerv1.GetCheckoutCredentialResponse{DownloadToken: lease.DownloadToken}
	if !lease.DownloadExpires.IsZero() {
		res.ExpiresUnix = lease.DownloadExpires.Unix()
	}
	if lease.Spec.Repo == "" {
		return res, nil // uploaded source: the worker reads the snapshot itself
	}
	co, err := w.F.Checkouts.Get(ctx, wid, jobID, epoch)
	if err != nil {
		return nil, checkoutError(err)
	}
	rec, err := store.Get(ctx, w.F.cfg.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil {
		return nil, err
	}
	suite, err := fleet.ParseSuite(rec.Suite.Value())
	if err != nil {
		return nil, err
	}
	res.SealedCheckout, err = fleet.SealCheckout(suite, rec.KEMPub.Value(), wid, jobID, epoch, fleet.CheckoutPlain{
		CloneURL: co.CloneURL, Username: co.Username, Token: co.Token, Expires: co.Expires.Unix()})
	if err != nil {
		return nil, err
	}
	res.ExpiresUnix = co.Expires.Unix()
	return res, nil
}

// checkoutError maps the lease refusal and an unavailable forge to the API error model.
func checkoutError(err error) error {
	var de *diag.Error
	switch {
	case strings.HasPrefix(err.Error(), "permission_denied: "):
		return api.Forbidden(strings.TrimPrefix(err.Error(), "permission_denied: "))
	case errors.As(err, &de) && de.Code == 9008:
		return api.Unavailable("checkout credentials are not available yet: " + de.Error())
	}
	return err
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
		gen = cfg.Gen.Value()
	}
	conn := w.F.Hub.Attach(wid)
	defer conn.Detach()
	conn.SetSlots(int(hello.SlotsFree))
	liveOut := make(chan *workerv1.AttachResponse, 64)
	if w.Live != nil {
		detach := w.Live.Attached(wid, func(m *workerv1.AttachResponse) bool {
			select {
			case liveOut <- m:
				return true
			default:
				return false
			}
		})
		defer detach()
	}
	if nonce := st.RequestHeader().Get(fleet.AttachNonceHeader); w.Key != nil && nonce != "" {
		proof, err := w.Key.AttachProof(nonce)
		if err != nil {
			return api.InvalidArgument("nonce", err.Error())
		}
		st.ResponseHeader().Set(fleet.AttachProofHeader, proof) // sent with Welcome
	}
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
			case *workerv1.AttachRequest_LiveChunk:
				if w.Live != nil {
					_ = w.Live.Chunk(wid, x.LiveChunk) // LiveLogs logs and counts rejected chunks
				}
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
		case m := <-liveOut:
			out = m
		}
		if out == nil {
			continue
		}
		if err := st.Send(out); err != nil {
			return err
		}
	}
}

// attachMessage turns a Hub line ("wake <class> <slot> <job>", "cancel <job>", "drain") into
// its Attach message. Live log subscriptions come from LiveLogs, not from the Hub.
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
	}
	return nil
}
