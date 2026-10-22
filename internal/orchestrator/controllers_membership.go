package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Membership timings of R-SRV-102.
const (
	MemberBeat  = 10 * time.Second // orch/<id> heartbeat
	MemberStale = 30 * time.Second // a member whose heartbeat is older is removed
	// memberPrune is the pass period of the membership leader: heartbeat age > 30 s plus one
	// pass period must stay inside the 40 s removal bound.
	memberPrune = 5 * time.Second
)

// Membership is orchestrator membership and discovery (R-SRV-102, §05.11): every orchestrator
// publishes orch/<id> every 10 s and adds itself to fleet/cfg.m; the membership leader removes
// members whose heartbeat is older than 30 s.
//
// Write order (R-SRV-101): joining adds the fleet/cfg.m entry (the index) before the first
// orch/<id> write (the record); removal deletes orch/<id> before the entry. Readers skip
// members without a record.
type Membership struct {
	Fleet    *store.Bucket
	ID       string // orchestrator id
	Endpoint string // URL workers attach to
	SPKIPin  string // TLS SPKI hash workers pin ("" without TLS)
	Version  string
	Now      func() time.Time
	Log      *slog.Logger

	started time.Time
	cfg     store.Cache[store.FleetConfig]
	ctl     store.Cache[store.ControllerLease]
}

func (m *Membership) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Membership) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// Name implements server.Component for the heartbeat part (every orchestrator).
func (m *Membership) Name() string { return "membership heartbeat" }

// Run implements server.Component: join, then heartbeat every 10 s and re-join when removed
// (e.g. after a pause longer than 30 s); leave on shutdown.
func (m *Membership) Run(ctx context.Context) error {
	if m.Fleet == nil || m.ID == "" {
		return errors.New("orchestrator: membership needs the fleet bucket and an id")
	}
	if m.started.IsZero() {
		m.started = m.now()
	}
	defer m.leave()
	for {
		if err := m.beat(ctx); err != nil && ctx.Err() == nil {
			m.log().Warn("membership heartbeat failed", "orchestrator", m.ID, "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(MemberBeat):
		}
	}
}

// beat makes sure this orchestrator is a member (CAS on fleet/cfg only when it is missing or
// its endpoint changed; 1 conditional GET otherwise) and writes orch/<id>.
func (m *Membership) beat(ctx context.Context) error {
	now := m.now()
	fc, _, err := store.GetCached(ctx, m.Fleet, &m.cfg, store.FleetConfigKey())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if fc == nil || !m.listed(fc) {
		if err := m.join(ctx, now); err != nil {
			return err
		}
	}
	return store.PutOwned(ctx, m.Fleet, &store.OrchRecord{ID: store.ID(m.ID), Endpoint: store.Set(m.Endpoint), SPKIPin: store.Set(m.SPKIPin),
		Heartbeat: store.Set(now), Started: store.Set(m.started), Version: store.Set(m.Version)})
}

func (m *Membership) listed(fc *store.FleetConfig) bool {
	return slices.ContainsFunc(fc.Members.Value(), func(x store.FleetMember) bool {
		return x.Orch == m.ID && x.Endpoint == m.Endpoint && x.SPKIPin == m.SPKIPin
	})
}

// join adds (or updates) this orchestrator's entry of fleet/cfg.m.
func (m *Membership) join(ctx context.Context, now time.Time) error {
	_, err := store.CAS(ctx, m.Fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		next := store.FleetConfigKey()
		var members []store.FleetMember
		var gen int64
		if cur != nil {
			if m.listed(cur) {
				return nil, store.ErrNoChange
			}
			*next = *cur
			members, gen = cur.Members.Value(), cur.Gen.Value()
		}
		members = slices.DeleteFunc(slices.Clone(members), func(x store.FleetMember) bool { return x.Orch == m.ID })
		next.Members = store.Set(append(members, store.FleetMember{Orch: m.ID, Endpoint: m.Endpoint, SPKIPin: m.SPKIPin, Since: now}))
		next.Gen = store.Set(gen + 1)
		return next, nil
	})
	if err == nil {
		m.log().Info("orchestrator joined fleet/cfg", "orchestrator", m.ID, "endpoint", m.Endpoint)
	}
	return err
}

// leave removes this orchestrator at a graceful shutdown: orch/<id> first, then its entry.
func (m *Membership) leave() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.remove(ctx, m.ID, nil); err != nil {
		m.log().Warn("orchestrator did not leave fleet/cfg", "orchestrator", m.ID, "err", err.Error())
	}
}

// remove deletes orch/<id> (only while stale says so, when given) and then the member entry.
func (m *Membership) remove(ctx context.Context, id string, stale func(*store.OrchRecord) bool) error {
	_, err := store.CAS(ctx, m.Fleet, &store.OrchRecord{ID: store.ID(id)}, func(cur *store.OrchRecord) (*store.OrchRecord, error) {
		if cur != nil && stale != nil && !stale(cur) {
			return nil, errAlive
		}
		return nil, nil
	})
	if errors.Is(err, errAlive) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = store.CAS(ctx, m.Fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		if cur == nil || !slices.ContainsFunc(cur.Members.Value(), func(x store.FleetMember) bool { return x.Orch == id }) {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Members = store.Set(slices.DeleteFunc(slices.Clone(cur.Members.Value()), func(x store.FleetMember) bool { return x.Orch == id }))
		next.Gen = store.Set(next.Gen.Value() + 1)
		return &next, nil
	})
	return err
}

var errAlive = errors.New("member is alive")

// Prune is the membership leader's pass: members whose heartbeat is older than MemberStale
// (or that never wrote one within MemberStale of joining) are removed.
func (m *Membership) Prune(ctx context.Context, r *Run) error {
	fc, _, err := store.GetCached(ctx, m.Fleet, &m.cfg, store.FleetConfigKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, mem := range fc.Members.Value() {
		if mem.Orch == m.ID {
			continue
		}
		rec, err := store.Get(ctx, m.Fleet, &store.OrchRecord{ID: store.ID(mem.Orch)})
		switch {
		case errors.Is(err, store.ErrNotFound):
			if r.Now.Sub(mem.Since) <= MemberStale {
				continue // joined, record not yet written (intent first)
			}
		case err != nil:
			errs = append(errs, err)
			continue
		case r.Now.Sub(rec.Heartbeat.Value()) <= MemberStale:
			continue
		}
		stale := func(cur *store.OrchRecord) bool { return r.Now.Sub(cur.Heartbeat.Value()) > MemberStale }
		if err := m.remove(ctx, mem.Orch, stale); err != nil {
			errs = append(errs, fmt.Errorf("remove member %s: %w", mem.Orch, err))
			continue
		}
		m.log().Info("orchestrator removed from fleet/cfg (heartbeat stale)", "member", mem.Orch, "by", m.ID)
	}
	return errors.Join(errs...)
}

// Controller returns the membership leader controller (lease ctl/membership).
func (m *Membership) Controller(skew time.Duration) *Controller {
	return &Controller{ID: CtlMembership, Fleet: m.Fleet, Owner: m.ID, Interval: MemberBeat, Every: memberPrune,
		Skew: skew, Now: m.Now, Pass: m.Prune, Log: m.Log}
}

// OrchestratorInfo is one member as ListOrchestrators shows it.
type OrchestratorInfo struct {
	ID        string
	Endpoint  string
	Version   string
	Started   time.Time
	Heartbeat time.Time
	LeaderOf  []string
}

// List returns the members of fleet/cfg.m with their orch/<id> record and the controller
// leases they hold (§6 UI cost: 1 + members requests, plus one conditional GET per
// controller lease). Members without a record are skipped (R-SRV-101).
func (m *Membership) List(ctx context.Context) ([]OrchestratorInfo, error) {
	fc, _, err := store.GetCached(ctx, m.Fleet, &m.cfg, store.FleetConfigKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	leaders, err := Leaders(ctx, m.Fleet, &m.ctl, m.now())
	if err != nil {
		return nil, err
	}
	var out []OrchestratorInfo
	for _, mem := range fc.Members.Value() {
		rec, err := store.Get(ctx, m.Fleet, &store.OrchRecord{ID: store.ID(mem.Orch)})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lo := leaders[mem.Orch]
		sort.Strings(lo)
		out = append(out, OrchestratorInfo{ID: mem.Orch, Endpoint: mem.Endpoint, Version: rec.Version.Value(),
			Started: rec.Started.Value(), Heartbeat: rec.Heartbeat.Value(), LeaderOf: lo})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
