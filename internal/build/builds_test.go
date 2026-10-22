package buildctl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/fleetsrv"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// The controller tests run against RustFS with the real fleet queue; a fake worker claims
// jobs and writes signed results and evaluation reports, as the worker process does.

type objects struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (o *objects) Read(_ context.Context, key string, max int64) ([]byte, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	if !ok {
		return nil, "", cache.ErrNotFound
	}
	if int64(len(b)) > max {
		return nil, "", cache.ErrTooLarge
	}
	return b, `"e"`, nil
}

func (o *objects) Head(_ context.Context, key string) (*cache.Info, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return &cache.Info{Size: int64(len(b))}, nil
}

func (o *objects) put(key string, b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = b
}

type sink struct {
	mu sync.Mutex
	s  []forge.Status
}

func (s *sink) SetStatus(_ context.Context, _ string, st forge.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s = append(s.s, st)
	return nil
}

func (s *sink) states(ctx string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, st := range s.s {
		if st.Context == ctx {
			out = append(out, string(st.State)+" "+st.Description)
		}
	}
	return out
}

type projects struct{}

func (projects) Project(_ context.Context, id string) (*buildctl.Project, error) {
	return &buildctl.Project{ID: id, DefaultBranch: "main", Repo: "f1/1"}, nil
}

type lab struct {
	t        *testing.T
	ctx      context.Context
	control  *store.Bucket
	fleet    *store.Bucket
	art      *objects
	statuses *sink
	b        *buildctl.Builds
	wid      string
	key      ed25519.PrivateKey
}

func newLab(t *testing.T, be *env.Backend, await ...bool) *lab {
	t.Helper()
	ctx := context.Background()
	l := &lab{t: t, ctx: ctx, control: be.Open(t, "control"), fleet: be.Open(t, "fleet"),
		art: &objects{m: map[string][]byte{}}, statuses: &sink{}}
	f, err := fleetsrv.New(ctx, fleetsrv.Config{Name: "o1", Control: l.control, Fleet: l.fleet})
	if err != nil {
		t.Fatal(err)
	}
	l.b, err = buildctl.New(buildctl.Config{Name: "o1", Control: l.control, Jobs: l.fleet, Fleet: f,
		Artifacts: l.art, Projects: projects{}, Statuses: l.statuses, BaseURL: "https://bob.example",
		AwaitPromotion: len(await) > 0 && await[0]})
	if err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	l.wid, l.key = "w1", key
	if err := store.Create(ctx, l.fleet, &store.WorkerRecord{Wid: store.ID(l.wid), State: store.Set[string]("active"), SigPub: store.Set([]byte(pub))}); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *lab) pass() {
	l.t.Helper()
	if err := l.b.Pass(l.ctx); err != nil {
		l.t.Fatalf("pass: %v", err)
	}
}

func (l *lab) build(id string) *store.BuildRecord {
	l.t.Helper()
	rec, err := l.b.Get(l.ctx, id)
	if err != nil {
		l.t.Fatal(err)
	}
	return rec
}

// job returns the live record of the only non-terminal job row of build id.
func (l *lab) job(id string) *store.JobRecord {
	l.t.Helper()
	for _, row := range l.build(id).Jobs.Value() {
		if j, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(row.ID)}); err == nil {
			return j
		}
	}
	l.t.Fatalf("build %s has no live job", id)
	return nil
}

// run claims job j as the fake worker and finishes it with res, writing report (if any) first.
func (l *lab) run(j *store.JobRecord, status string, report *jobspec.Report, failed ...string) {
	l.t.Helper()
	claimed, err := store.CAS(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		n := *cur
		n.State, n.Owner, n.Epoch, n.Attempt, n.Claimed, n.Changed = store.Set[string]("running"), store.Set(l.wid), store.Set(cur.Epoch.Value()+1), store.Set(cur.Attempt.Value()+1), store.Set(time.Now()), store.Set(time.Now())
		return &n, nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
	spec, _ := fleet.DecodeSpec(claimed.Spec.Value())
	res := store.JobResult{V: 1, Status: status, Failed: failed, Finished: time.Now().Unix()}
	if status == "failed" {
		res.Error = "✗ BOB-E-2005 test //cmd/api:test failed"
	}
	if report != nil {
		body, _ := json.Marshal(report)
		l.art.put(jobspec.ReportKey(jobspec.StagingPrefix(spec.Staging, claimed.Attempt.Value())), body)
		res.EvalSHA = jobspec.SHA256(body)
	}
	rs, _ := fleet.Encode(res)
	state := "succeeded"
	if status != "ok" {
		state = "failed"
	}
	if err := store.Change(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		n := *cur
		n.State, n.Result, n.ResultSig, n.Changed = store.Set(state), store.Set(rs), store.Set(fleet.SignResult(l.key, j.ID.Value(), cur.Epoch.Value(), rs)), store.Set(time.Now())
		return &n, nil
	}); err != nil {
		l.t.Fatal(err)
	}
}

var pipelines = []jobspec.Pipeline{
	{Name: "ci", Triggers: []string{"push main", "push feature*"}, Targets: []string{"//..."}, Tests: []string{"//..."}},
	{Name: "release", Triggers: []string{"tag v*"}, Concurrency: "queue", Targets: []string{"//cmd/api:api"}, Releases: []string{"api"}},
}

func okReport(matched ...string) *jobspec.Report {
	r := &jobspec.Report{V: 1, Pipelines: pipelines, Matched: matched,
		Packages: []jobspec.PackageReport{{Label: "//cmd/api:api", Status: "built"}, {Label: "//libs/foo:foo", Status: "cached"}}}
	r.Targets, r.Tests = jobspec.Targets(pipelines, matched)
	return r
}

const sha1 = "0123456789abcdef0123456789abcdef01234567"

func push(branch string) forge.Event {
	return forge.Event{Type: forge.EventPush, DeliveryID: "d-" + branch, Push: &forge.Push{Branch: branch, After: sha1}}
}

func TestBuildController(t *testing.T) {
	be := env.Start(t)
	t.Run("push-succeeds", func(t *testing.T) { pushSucceeds(t, newLab(t, be)) })
	t.Run("forged-match-rejected", func(t *testing.T) { forgedMatch(t, newLab(t, be)) })
	t.Run("supersede", func(t *testing.T) { supersede(t, newLab(t, be)) })
	t.Run("queue", func(t *testing.T) { queue(t, newLab(t, be)) })
	t.Run("cancel-retry-rerun", func(t *testing.T) { cancelRetryRerun(t, newLab(t, be)) })
	t.Run("infra-retries", func(t *testing.T) { infraRetries(t, newLab(t, be)) })
	t.Run("promotion", func(t *testing.T) { promotion(t, newLab(t, be, true)) })
	t.Run("event-idempotent", func(t *testing.T) { eventIdempotent(t, newLab(t, be)) })
}

func pushSucceeds(t *testing.T, l *lab) {
	id, err := l.b.CreateFromEvent(l.ctx, "acme", "k1", push("main"))
	if err != nil {
		t.Fatal(err)
	}
	if rec := l.build(id); rec.State.Value() != buildctl.StatePending || rec.Number.Value() != 1 {
		t.Fatalf("created %s #%d", rec.State.Value(), rec.Number.Value())
	}
	l.pass()
	j := l.job(id)
	spec, _ := fleet.DecodeSpec(j.Spec.Value())
	if j.State.Value() != "queued" || spec.Kind != buildctl.KindEvalBuild || !slices.Equal(spec.Targets, []string{"@event:push:main"}) || spec.Repo != "f1/1" {
		t.Fatalf("job %s %+v", j.State.Value(), spec)
	}
	if rec := l.build(id); rec.State.Value() != buildctl.StateQueued || rec.Phase.Value() != buildctl.PhaseEvaluating {
		t.Fatalf("after plan: %s %s", rec.State.Value(), rec.Phase.Value())
	}
	l.run(j, "ok", okReport("ci"))
	l.pass()
	rec := l.build(id)
	if rec.State.Value() != buildctl.StateSucceeded || len(rec.Pipelines.Value()) != 1 || rec.Pipelines.Value()[0].Name != "ci" || rec.Counts.Value().Built != 1 || rec.Counts.Value().Cached != 1 {
		t.Fatalf("build: %s %+v %+v errors %+v", rec.State.Value(), rec.Pipelines.Value(), rec.Counts.Value(), rec.Errors.Value())
	}
	if _, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("job record after finalise: %v, want deleted", err)
	}
	if ci := l.statuses.states("bob/ci"); len(ci) == 0 || !strings.HasPrefix(ci[len(ci)-1], "success #1 succeeded") {
		t.Errorf("bob/ci statuses %v", ci)
	}
	if ev := l.statuses.states(buildctl.ContextEvaluate); len(ev) < 2 || !strings.HasPrefix(ev[0], "pending") || !strings.HasPrefix(ev[len(ev)-1], "success") {
		t.Errorf("bob/evaluate statuses %v", ev)
	}
	if rel := l.statuses.states("bob/release"); len(rel) != 0 {
		t.Errorf("bob/release statuses %v for a push", rel)
	}
	l.pass() // settles: leaves bidx/running
	running, err := store.Get(l.ctx, l.control, &store.RunningBuilds{Name: store.ID("running")})
	if err != nil || len(running.Builds.Value()) != 0 {
		t.Errorf("bidx/running after the build: %+v %v", running, err)
	}
	if p, err := l.b.JobProject(l.ctx, j.ID.Value()); err != nil || p != "acme" {
		t.Errorf("job %s resolves to %q %v", j.ID.Value(), p, err)
	}
	nodes, _ := l.b.Nodes(l.ctx, id)
	if nodes == nil || len(nodes.Nodes.Value()) != 2 {
		t.Errorf("bnodes %+v", nodes)
	}
	pb, err := l.b.ToProto(l.ctx, rec)
	if err != nil || pb.GetWebUrl() != "https://bob.example/p/acme/builds/"+id || pb.GetCounts().GetBuilt() != 1 {
		t.Errorf("proto %v %v", pb, err)
	}
}

func forgedMatch(t *testing.T, l *lab) {
	id, err := l.b.CreateFromEvent(l.ctx, "acme", "k1", push("main"))
	if err != nil {
		t.Fatal(err)
	}
	l.pass()
	forged := okReport("ci", "release") // release has no push trigger
	l.run(l.job(id), "ok", forged)
	l.pass()
	rec := l.build(id)
	if rec.State.Value() != buildctl.StateFailed || len(rec.Errors.Value()) == 0 || !strings.Contains(rec.Errors.Value()[0].Message, "evaluation result rejected: pipeline match mismatch") {
		t.Fatalf("forged report: %s %+v", rec.State.Value(), rec.Errors.Value())
	}
	if len(rec.Pipelines.Value()) != 0 {
		t.Errorf("pipelines of a rejected report were taken: %+v", rec.Pipelines.Value())
	}
	// a report whose bytes differ from the signed sha is rejected too
	id2, _ := l.b.CreateFromEvent(l.ctx, "acme", "k2", push("main"))
	l.pass()
	j := l.job(id2)
	l.run(j, "ok", okReport("ci"))
	spec, _ := fleet.DecodeSpec(j.Spec.Value())
	l.art.put(jobspec.ReportKey(jobspec.StagingPrefix(spec.Staging, 1)), []byte(`{"v":1}`))
	l.pass()
	if rec := l.build(id2); rec.State.Value() != buildctl.StateFailed {
		t.Errorf("tampered report: build %s", rec.State.Value())
	}
}

func apiBuild(t *testing.T, l *lab, ref, sha string) *store.BuildRecord {
	t.Helper()
	rec, err := l.b.Create(l.ctx, buildctl.Request{Project: "acme", Trigger: store.BuildTrigger{Kind: buildctl.TriggerAPI, By: "user:u1", Ref: ref},
		Source: store.BuildSource{Ref: ref, SHA: sha}, Pipelines: []string{"ci"}})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func supersede(t *testing.T, l *lab) {
	first := apiBuild(t, l, "refs/heads/feature", sha1)
	l.pass()
	second := apiBuild(t, l, "refs/heads/feature", strings.Repeat("ab", 20))
	l.pass()
	got := l.build(first.ID.Value())
	if got.State.Value() != buildctl.StateSuperseded || got.SupersededBy.Value() != second.Number.Value() {
		t.Fatalf("first: %s sp %d", got.State.Value(), got.SupersededBy.Value())
	}
	pb, _ := l.b.ToProto(l.ctx, got)
	if pb.GetTrigger().GetSupersededBy() != second.ID.Value() {
		t.Errorf("superseded_by %q, want %s", pb.GetTrigger().GetSupersededBy(), second.ID.Value())
	}
	if s := l.statuses.states(buildctl.ContextEvaluate); len(s) == 0 || !slices.ContainsFunc(s, func(x string) bool {
		return strings.HasPrefix(x, "cancelled") && strings.Contains(x, fmt.Sprintf("superseded by #%d", second.Number.Value()))
	}) {
		t.Errorf("statuses of the superseded build: %v", s)
	}
	if st := l.build(second.ID.Value()).State.Value(); st != buildctl.StateQueued {
		t.Errorf("second: %s", st)
	}
	l.pass()
	if j, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(got.Jobs.Value()[0].ID)}); err == nil && j.State.Value() != "cancelled" {
		t.Errorf("job of the superseded build: %s", j.State.Value())
	}
}

func queue(t *testing.T, l *lab) {
	a, _ := l.b.CreateFromEvent(l.ctx, "acme", "k1", push("main"))
	l.pass()
	ev := push("main")
	ev.Push.After = strings.Repeat("cd", 20)
	bID, _ := l.b.CreateFromEvent(l.ctx, "acme", "k2", ev)
	l.pass()
	if st := l.build(bID).State.Value(); st != buildctl.StatePending {
		t.Fatalf("newer push to main: %s, want pending (queue)", st)
	}
	l.run(l.job(a), "ok", okReport("ci"))
	l.pass()
	l.pass()
	if st := l.build(a).State.Value(); st != buildctl.StateSucceeded {
		t.Fatalf("older: %s", st)
	}
	if st := l.build(bID).State.Value(); st != buildctl.StateQueued {
		t.Fatalf("newer after the older finished: %s", st)
	}
}

func cancelRetryRerun(t *testing.T, l *lab) {
	rec := apiBuild(t, l, "refs/heads/main", sha1)
	l.pass()
	l.run(l.job(rec.ID.Value()), "failed", okReport("ci"), "//cmd/api:test")
	l.pass()
	failed := l.build(rec.ID.Value())
	if failed.State.Value() != buildctl.StateFailed || failed.Pipelines.Value()[0].State != buildctl.StateFailed {
		t.Fatalf("failed build: %s %+v", failed.State.Value(), failed.Pipelines.Value())
	}
	if _, err := l.b.Cancel(l.ctx, rec.ID.Value(), "u"); !errors.Is(err, buildctl.ErrFinished) {
		t.Errorf("cancel of a finished build: %v", err)
	}
	retry, err := l.b.Retry(l.ctx, rec.ID.Value(), "user:u1")
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := l.b.ToProto(l.ctx, retry)
	if pb.GetTrigger().GetRetryOf() != rec.ID.Value() || pb.GetAttempt() != 2 || pb.GetTrigger().GetKind().String() != "TRIGGER_KIND_RETRY" || retry.Pipeline.Value() != "ci" {
		t.Errorf("retry: %v pipeline %q", pb.GetTrigger(), retry.Pipeline.Value())
	}
	rerun, err := l.b.Rerun(l.ctx, rec.ID.Value(), "user:u1")
	if err != nil {
		t.Fatal(err)
	}
	if pb, _ := l.b.ToProto(l.ctx, rerun); pb.GetTrigger().GetRerunOf() != rec.ID.Value() {
		t.Errorf("rerun: %v", pb.GetTrigger())
	}
	l.pass() // retry and rerun: parallel builds of main queue behind each other
	running := l.build(retry.ID.Value())
	if running.State.Value() != buildctl.StateQueued {
		t.Fatalf("retry build %s", running.State.Value())
	}
	c, err := l.b.Cancel(l.ctx, retry.ID.Value(), "u")
	if err != nil || c.State.Value() != buildctl.StateCancelled {
		t.Fatalf("cancel: %v %v", c, err)
	}
	l.pass()
	if _, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(running.Jobs.Value()[0].ID)}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancelled job not finalised: %v", err)
	}
}

func infraRetries(t *testing.T, l *lab) {
	rec := apiBuild(t, l, "refs/heads/main", sha1)
	l.pass()
	for i := range 3 {
		j := l.job(rec.ID.Value())
		if j.Attempt.Value() != int64(i) {
			t.Fatalf("attempt %d: job %s has attempt %d", i+1, j.ID.Value(), j.Attempt.Value())
		}
		l.run(j, "infra", nil)
		l.pass()
	}
	got := l.build(rec.ID.Value())
	if got.State.Value() != buildctl.StateFailed || len(got.Errors.Value()) == 0 || !strings.Contains(got.Errors.Value()[0].Message, "failed 3 times on workers (infrastructure)") {
		t.Fatalf("after 3 infra failures: %s %+v (%d jobs)", got.State.Value(), got.Errors.Value(), len(got.Jobs.Value()))
	}
}

func eventIdempotent(t *testing.T, l *lab) {
	a, err := l.b.CreateFromEvent(l.ctx, "acme", "same", push("main"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.b.CreateFromEvent(l.ctx, "acme", "same", push("main"))
	if err != nil || a != b {
		t.Fatalf("repeated delivery: %s %s %v", a, b, err)
	}
	if _, err := l.b.CreateFromEvent(l.ctx, "acme", "del", forge.Event{Type: forge.EventPush, Push: &forge.Push{Branch: "x", Deleted: true}}); !errors.Is(err, buildctl.ErrNoBuild) {
		t.Errorf("deleted branch: %v", err)
	}
}

func setPM(t *testing.T, l *lab, id, pm string, nr ...string) {
	t.Helper()
	if err := store.Change(l.ctx, l.fleet, &store.JobRecord{ID: store.ID(id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		n := *cur
		n.Promotion, n.NonReproducible = store.Set(pm), store.Set(nr)
		return &n, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func promotion(t *testing.T, l *lab) {
	id, _ := l.b.CreateFromEvent(l.ctx, "acme", "k1", push("main"))
	l.pass()
	j := l.job(id)
	l.run(j, "ok", okReport("ci"))
	l.pass()
	if rec := l.build(id); rec.State.Value() != buildctl.StateRunning || rec.Phase.Value() != buildctl.PhasePromoting {
		t.Fatalf("waiting for promotion: %s %s", rec.State.Value(), rec.Phase.Value())
	}
	if _, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}); err != nil {
		t.Fatalf("job finalised before its promotion: %v", err)
	}
	setPM(t, l, j.ID.Value(), "blocked: staging incomplete")
	l.pass()
	j2 := l.job(id)
	if j2.ID.Value() == j.ID.Value() || j2.State.Value() != "queued" {
		t.Fatalf("blocked promotion: next job %s %s", j2.ID.Value(), j2.State.Value())
	}
	l.run(j2, "ok", okReport("ci"))
	setPM(t, l, j2.ID.Value(), "done", "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-x")
	l.pass()
	rec := l.build(id)
	if rec.State.Value() != buildctl.StateSucceeded {
		t.Fatalf("after promotion done: %s %+v", rec.State.Value(), rec.Errors.Value())
	}
	s := &buildctl.Service{B: l.b}
	pj, err := s.B.ToProto(l.ctx, rec)
	if err != nil || pj.GetState().String() != "BUILD_STATE_SUCCEEDED" {
		t.Fatal(pj, err)
	}
	var row store.BuildJob
	for _, r := range rec.Jobs.Value() {
		if r.ID == j2.ID.Value() {
			row = r
		}
	}
	if row.Promotion != "done" || len(rec.NonReproducible.Value()[j2.ID.Value()]) != 1 {
		t.Errorf("row %+v nr %v", row, rec.NonReproducible.Value())
	}
}
