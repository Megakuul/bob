package orchestrator_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// The controller tests run against RustFS (tests/store/env). Passes take the run time from
// Run.Now, so ageing is done by choosing that time, not by sleeping.

type buckets struct{ control, fleet *store.Bucket }

func open(t *testing.T) buckets {
	t.Helper()
	be := env.Start(t)
	return buckets{control: be.Open(t, "control"), fleet: be.Open(t, "fleet")}
}

func TestControllers(t *testing.T) {
	b := open(t)
	if b.fleet == nil {
		return
	}
	t.Run("handover-after-crash", func(t *testing.T) { handoverAfterCrash(t, b) })
	t.Run("handover-on-shutdown", func(t *testing.T) { handoverOnShutdown(t, b) })
	t.Run("one-leader", func(t *testing.T) { oneLeader(t, b) })
	t.Run("persist", func(t *testing.T) { persist(t, b) })
	t.Run("membership", func(t *testing.T) { membership(t, b) })
	t.Run("index-repair", func(t *testing.T) { indexRepair(t, b) })
	t.Run("retention", func(t *testing.T) { retention(t, b) })
	t.Run("keys", func(t *testing.T) { keys(t, b) })
}

func counter(n *atomic.Int64) func(context.Context, *orchestrator.Run) error {
	return func(context.Context, *orchestrator.Run) error { n.Add(1); return nil }
}

func run(t *testing.T, c *orchestrator.Controller) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	return func() { cancel(); <-done }
}

// A holder killed with -9 leaves its lease behind: another orchestrator takes it within
// TTL + skew (2 × interval + skew) and so within 3 × interval when skew < interval (R-SRV-004).
func handoverAfterCrash(t *testing.T, b buckets) {
	ctx := context.Background()
	const iv, skew = 2 * time.Second, 500 * time.Millisecond
	dead := &store.ControllerLease{Name: "t-crash", Owner: "dead", Epoch: 7, Expires: time.Now().Add(orchestrator.LeaseTTLFactor * iv)}
	if err := store.Create(ctx, b.fleet, dead); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	t0 := time.Now()
	stop := run(t, &orchestrator.Controller{ID: "t-crash", Fleet: b.fleet, Owner: "live", Interval: iv, Skew: skew, Pass: counter(&n)})
	defer stop()
	for n.Load() == 0 && time.Since(t0) < 3*iv+2*time.Second {
		time.Sleep(20 * time.Millisecond)
	}
	if d := time.Since(t0); n.Load() == 0 || d > 3*iv {
		t.Fatalf("takeover after %v (passes %d), want within 3 x interval = %v", d, n.Load(), 3*iv)
	}
	l, err := store.Get(ctx, b.fleet, &store.ControllerLease{Name: "t-crash"})
	if err != nil || l.Owner != "live" || l.Epoch != 8 {
		t.Fatalf("lease after takeover = %+v (%v), want owner live epoch 8", l, err)
	}
}

func handoverOnShutdown(t *testing.T, b buckets) {
	var na, nb atomic.Int64
	const iv = 5 * time.Second
	stopA := run(t, &orchestrator.Controller{ID: "t-shut", Fleet: b.fleet, Owner: "a", Interval: iv, Skew: time.Second, Pass: counter(&na)})
	waitFor(t, 5*time.Second, func() bool { return na.Load() > 0 })
	stopB := run(t, &orchestrator.Controller{ID: "t-shut", Fleet: b.fleet, Owner: "b", Interval: iv, Skew: time.Second, Pass: counter(&nb)})
	defer stopB()
	time.Sleep(time.Second)
	if nb.Load() != 0 {
		t.Fatalf("b passed while a held the lease")
	}
	t0 := time.Now()
	stopA()
	waitFor(t, 2*iv, func() bool { return nb.Load() > 0 })
	// b notices the release at its next lease check (one pass period), long before the
	// released lease would have expired (2 × interval + skew).
	if d := time.Since(t0); d > iv+time.Second {
		t.Errorf("b took over %v after a released the lease, want within one interval (%v)", d, iv)
	}
}

// Three contenders: at no time do two of them run a pass of the same epoch range.
func oneLeader(t *testing.T, b buckets) {
	var mu sync.Mutex
	inPass := 0
	overlap := false
	owners := map[string]int{}
	var stops []func()
	for _, o := range []string{"x", "y", "z"} {
		stops = append(stops, run(t, &orchestrator.Controller{ID: "t-one", Fleet: b.fleet, Owner: o, Interval: time.Second, Every: 200 * time.Millisecond,
			Skew: 200 * time.Millisecond, Pass: func(context.Context, *orchestrator.Run) error {
				mu.Lock()
				inPass++
				overlap = overlap || inPass > 1
				owners[o]++
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inPass--
				mu.Unlock()
				return nil
			}}))
	}
	time.Sleep(3 * time.Second)
	for _, s := range stops {
		s()
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Errorf("two contenders ran a pass at the same time")
	}
	if len(owners) != 1 {
		t.Errorf("passes by %v, want exactly one leader while nobody dies", owners)
	}
}

// A Persist controller hands its state and last pass time to the next leader: an hourly pass
// that just ran is not repeated by the successor.
func persist(t *testing.T, b buckets) {
	var na, nb atomic.Int64
	var got []byte
	var mu sync.Mutex
	mk := func(owner string, n *atomic.Int64) *orchestrator.Controller {
		return &orchestrator.Controller{ID: "t-persist", Fleet: b.fleet, Owner: owner, Interval: time.Hour, Skew: time.Second, Persist: true,
			Pass: func(_ context.Context, r *orchestrator.Run) error {
				n.Add(1)
				mu.Lock()
				got = slices.Clone(r.State)
				mu.Unlock()
				r.State = []byte("state-of-" + owner)
				return nil
			}}
	}
	stop := run(t, mk("a", &na))
	waitFor(t, 5*time.Second, func() bool { return na.Load() == 1 })
	stop()
	stop = run(t, mk("b", &nb))
	time.Sleep(time.Second)
	stop()
	if nb.Load() != 0 {
		t.Errorf("the successor repeated an hourly pass that ran a second ago")
	}
	// with the clock an hour later the successor passes and sees a's state
	later := func() time.Time { return time.Now().Add(61 * time.Minute) }
	c := mk("b", &nb)
	c.Now = later
	stop = run(t, c)
	waitFor(t, 5*time.Second, func() bool { return nb.Load() == 1 })
	stop()
	mu.Lock()
	defer mu.Unlock()
	if string(got) != "state-of-a" {
		t.Errorf("successor state = %q, want state-of-a", got)
	}
}

func membership(t *testing.T, b buckets) {
	ctx := context.Background()
	now := time.Now().UTC()
	m := &orchestrator.Membership{Fleet: b.fleet, ID: "o1", Endpoint: "http://127.0.0.1:1", Version: "v"}
	stop := run2(t, m)
	waitFor(t, 5*time.Second, func() bool {
		l, err := m.List(ctx)
		return err == nil && len(l) == 1 && l[0].ID == "o1"
	})
	// a dead member: entry plus a stale record
	if _, err := store.CAS(ctx, b.fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		next := *cur
		next.Members = append(slices.Clone(cur.Members),
			store.FleetMember{Orch: "dead", Endpoint: "http://127.0.0.1:2", Since: now.Add(-time.Hour)},
			store.FleetMember{Orch: "young", Endpoint: "http://127.0.0.1:3", Since: now})
		return &next, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwned(ctx, b.fleet, &store.OrchRecord{ID: "dead", Heartbeat: now.Add(-31 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	l, _ := m.List(ctx)
	if len(l) != 2 { // young has no record yet: skipped
		t.Errorf("List = %+v, want o1 and dead", l)
	}
	if err := m.Prune(ctx, &orchestrator.Run{Now: now}); err != nil {
		t.Fatal(err)
	}
	fc, err := store.Get(ctx, b.fleet, store.FleetConfigKey())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range fc.Members {
		ids = append(ids, x.Orch)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"o1", "young"}) {
		t.Errorf("members after prune = %v, want o1 and young (joined, record not yet written)", ids)
	}
	if _, err := store.Get(ctx, b.fleet, &store.OrchRecord{ID: "dead"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("orch/dead after prune: %v, want deleted", err)
	}
	if err := m.Prune(ctx, &orchestrator.Run{Now: now.Add(31 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if fc, _ = store.Get(ctx, b.fleet, store.FleetConfigKey()); len(fc.Members) != 1 {
		t.Errorf("members = %+v, want only o1 once young never wrote a record within 30 s", fc.Members)
	}
	// leader_of: a lease held by o1
	if err := store.PutOwned(ctx, b.fleet, &store.ControllerLease{Name: orchestrator.CtlRetention, Owner: "o1", Epoch: 1, Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if l, err := m.List(ctx); err != nil || len(l) != 1 || !slices.Contains(l[0].LeaderOf, orchestrator.CtlRetention) {
		t.Errorf("List = %+v (%v), want o1 leader of retention", l, err)
	}
	stop()
	fc, _ = store.Get(ctx, b.fleet, store.FleetConfigKey())
	if fc != nil && len(fc.Members) != 0 {
		t.Errorf("members after a graceful stop = %+v, want none", fc.Members)
	}
}

func run2(t *testing.T, m *orchestrator.Membership) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	return func() { cancel(); <-done }
}

func indexRepair(t *testing.T, b buckets) {
	ctx := context.Background()
	now := time.Now().UTC()
	act := store.ActiveJobs(b.fleet, 4)
	for id, at := range map[string]time.Time{"jold": now.Add(-6 * time.Minute), "jyoung": now, "jlive": now.Add(-time.Hour)} {
		if err := act.Add(ctx, id, store.ActiveJob{Build: "b1", Created: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create(ctx, b.fleet, &store.JobRecord{ID: "jlive", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	// bpage: one entry with its record, one dangling old, one dangling young
	pages := store.BuildPages(b.control, "p1")
	for i, e := range []store.BuildEntry{{N: 1, Build: "blive", Created: now.Add(-time.Hour)}, {N: 2, Build: "bgone", Created: now.Add(-time.Hour)}, {N: 3, Build: "bnew", Created: now}} {
		if err := pages.Set(ctx, uint64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create(ctx, b.control, &store.BuildRecord{ID: "blive", Project: "p1", State: "running"}); err != nil {
		t.Fatal(err)
	}
	// rmem: u1 bound (listed), u2 listed without binding, u3 bound but not listed
	for _, u := range []*store.UserRecord{{ID: "u1", Bindings: []store.RoleBindingEntry{{Role: "dev", Project: "*"}}}, {ID: "u2"},
		{ID: "u3", Bindings: []store.RoleBindingEntry{{Role: "dev", Project: "p1"}}}} {
		if err := store.Create(ctx, b.control, u); err != nil {
			t.Fatal(err)
		}
	}
	ux := store.UserIndex(b.control)
	for i, id := range []string{"u1", "u2", "u3"} {
		if err := ux.Set(ctx, uint64(i), store.UserIndexEntry{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create(ctx, b.control, &store.IDList{Name: "all", IDs: []string{"dev"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, b.control, &store.RoleMembers{Role: "dev", Principals: []string{"user:u1", "user:u2"}}); err != nil {
		t.Fatal(err)
	}
	x := &orchestrator.IndexRepair{Control: b.control, Fleet: b.fleet, JobShards: 4,
		Projects: func(context.Context) ([]string, error) { return []string{"p1"}, nil }}
	r := &orchestrator.Run{Now: now}
	res, err := x.Repair(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Jobs != 1 || res.Builds != 1 || res.MembersAdded != 1 || res.MembersRemoved != 0 {
		t.Errorf("first pass = %+v, want 1 job, 1 build, 1 member added, none removed yet", res)
	}
	all, _ := act.All(ctx)
	if _, ok := all["jold"]; ok {
		t.Errorf("dangling old jact entry kept")
	}
	if _, ok := all["jyoung"]; !ok {
		t.Errorf("young jact entry (writer may still be busy) removed")
	}
	if _, ok := all["jlive"]; !ok {
		t.Errorf("jact entry with a record removed")
	}
	pg, _ := pages.Page(ctx, 0)
	if pg[0].Build != "blive" || pg[1].State != orchestrator.BuildEntryTombstone || pg[1].Build != "" || pg[2].Build != "bnew" {
		t.Errorf("bpage after repair = %+v", pg[:3])
	}
	mem, _ := store.Get(ctx, b.control, store.RoleMembersKey("dev"))
	if !slices.Contains(mem.Principals, "user:u3") || !slices.Contains(mem.Principals, "user:u2") {
		t.Errorf("rmem/dev after the first pass = %v, want u3 added and u2 kept (seen once)", mem.Principals)
	}
	// 6 minutes later the dangling member is removed
	r.Now = now.Add(6 * time.Minute)
	res, err = x.Repair(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	mem, _ = store.Get(ctx, b.control, store.RoleMembersKey("dev"))
	if res.MembersRemoved != 1 || slices.Contains(mem.Principals, "user:u2") || !slices.Contains(mem.Principals, "user:u1") {
		t.Errorf("second pass = %+v, rmem/dev %v; want u2 removed", res, mem.Principals)
	}
}

func retention(t *testing.T, b buckets) {
	ctx := context.Background()
	now := time.Now().UTC()
	year := 366 * 24 * time.Hour
	pages := store.BuildPages(b.control, "p2")
	type bld struct {
		id, state string
		created   time.Time
	}
	builds := []bld{{"old1", "succeeded", now.Add(-year)}, {"old2", "running", now.Add(-year)}, {"old3", "failed", now.Add(-year)}, {"new1", "succeeded", now}}
	for i, x := range builds {
		if err := pages.Set(ctx, uint64(i), store.BuildEntry{N: uint64(i + 1), Build: x.id, State: x.state, Created: x.created}); err != nil {
			t.Fatal(err)
		}
		if err := store.Create(ctx, b.control, &store.BuildRecord{ID: x.id, Project: "p2", State: x.state, Created: x.created}); err != nil {
			t.Fatal(err)
		}
	}
	type nodes struct {
		ID   string `pk:"bnodes" cbor:"-"`
		ETag string `etag:"true" cbor:"-"`
		N    int    `cbor:"n"`
	}
	if err := store.Create(ctx, b.control, &nodes{ID: "old1", N: 3}); err != nil {
		t.Fatal(err)
	}
	// queue slots: 5 slots, head 1006 → all below head − 1000
	if _, err := store.CAS(ctx, b.fleet, store.FleetConfigKey(), func(cur *store.FleetConfig) (*store.FleetConfig, error) {
		next := store.FleetConfigKey()
		if cur != nil {
			*next = *cur
		}
		next.Classes = append(next.Classes, store.FleetClass{ID: "c1"})
		return next, nil
	}); err != nil {
		t.Fatal(err)
	}
	q := store.Queue(b.fleet, "c1")
	for range 5 {
		if _, err := q.Append(ctx, &store.QueueSlot{Job: "j"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.AdvanceHead(ctx, 1006); err != nil {
		t.Fatal(err)
	}
	reg := store.RegLog(b.fleet)
	for range 3 {
		if _, err := reg.Append(ctx, &store.RegSlot{Wid: "w"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutOwned(ctx, b.fleet, &store.ControllerLease{Name: orchestrator.CtlRegistrar, Cursor: []byte("1003")}); err != nil {
		t.Fatal(err)
	}
	var dropped []string
	x := &orchestrator.Retention{Control: b.control, Fleet: b.fleet,
		Projects: func(context.Context) ([]string, error) { return []string{"p2"}, nil },
		DropPartitions: func(_ context.Context, class string, before time.Time) (int, error) {
			dropped = append(dropped, class+"<"+before.Format("2006-01-02"))
			return 0, nil
		}}
	r := &orchestrator.Run{Now: now}
	res, err := x.Collect(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Builds != 2 || res.QueueSlots != 5 || res.RegSlots != 2 {
		t.Errorf("retention = %+v, want 2 builds, 5 queue slots, 2 registration slots", res)
	}
	for _, x := range builds {
		_, err := store.Get(ctx, b.control, &store.BuildRecord{ID: x.id})
		if gone := errors.Is(err, store.ErrNotFound); gone != (x.id == "old1" || x.id == "old3") {
			t.Errorf("build %s gone = %v (%v)", x.id, gone, err)
		}
	}
	if _, err := store.Get(ctx, b.control, &nodes{ID: "old1"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bnodes/old1: %v, want deleted with its build", err)
	}
	pg, _ := pages.Page(ctx, 0)
	if pg[0].State != orchestrator.BuildEntryTombstone || pg[1].Build != "old2" || pg[2].State != orchestrator.BuildEntryTombstone || pg[3].Build != "new1" {
		t.Errorf("bpage after retention = %+v", pg[:4])
	}
	for s := uint64(1); s <= 5; s++ {
		if _, err := q.Read(ctx, s); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("queue slot %d: %v, want deleted", s, err)
		}
	}
	if _, err := reg.Read(ctx, 3); err != nil {
		t.Errorf("registration slot 3 (= cursor − 1000) deleted: %v", err)
	}
	want := []string{"log<" + now.Add(-30*24*time.Hour).Format("2006-01-02"), "run<" + now.Add(-400*24*time.Hour).Format("2006-01-02"), "job<" + now.Add(-400*24*time.Hour).Format("2006-01-02")}
	if !slices.Equal(dropped, want) {
		t.Errorf("partition drops = %v, want %v", dropped, want)
	}
	// the next pass starts where this one stopped and deletes nothing more
	res, err = x.Collect(ctx, r)
	if err != nil || res.Builds+res.QueueSlots+res.RegSlots != 0 {
		t.Errorf("second pass = %+v (%v), want nothing", res, err)
	}
}

func keys(t *testing.T, b buckets) {
	ctx := context.Background()
	k := &orchestrator.Keys{Control: b.control, Fleet: b.fleet}
	now := time.Now().UTC()
	ks, err := k.Rotate(ctx, now)
	if err != nil || len(ks) != 1 || !strings.HasPrefix(ks[0].Kid, "k") {
		t.Fatalf("first rotate = %+v (%v), want one key", ks, err)
	}
	if again, err := k.Rotate(ctx, now); err != nil || len(again) != 1 {
		t.Fatalf("second rotate = %+v (%v), want the same single key", again, err)
	}
	if _, err := store.Get(ctx, b.control, &orchestrator.OrchKeyRecord{Kid: ks[0].Kid}); err != nil {
		t.Fatalf("okey/%s: %v", ks[0].Kid, err)
	}
	// 84 days later the next key is created; 91 days later the first is retired
	ks, err = k.Rotate(ctx, now.Add(84*24*time.Hour))
	if err != nil || len(ks) != 2 {
		t.Fatalf("rotate 7 d before notAfter = %+v (%v), want two keys", ks, err)
	}
	ks, err = k.Rotate(ctx, now.Add(91*24*time.Hour))
	if err != nil || len(ks) != 1 {
		t.Fatalf("rotate after notAfter = %+v (%v), want the retired key gone", ks, err)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", d)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
