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
	if err := store.Create(ctx, l.fleet, &store.WorkerRecord{Wid: l.wid, State: "active", SigPub: pub}); err != nil {
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
	for _, row := range l.build(id).Jobs {
		if j, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: row.ID}); err == nil {
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
		n.State, n.Owner, n.Epoch, n.Attempt, n.Claimed, n.Changed = "running", l.wid, cur.Epoch+1, cur.Attempt+1, time.Now(), time.Now()
		return &n, nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
	spec, _ := fleet.DecodeSpec(claimed.Spec)
	res := store.JobResult{V: 1, Status: status, Failed: failed, Finished: time.Now().Unix()}
	if status == "failed" {
		res.Error = "✗ BOB-E-2005 test //cmd/api:test failed"
	}
	if report != nil {
		body, _ := json.Marshal(report)
		l.art.put(jobspec.ReportKey(jobspec.StagingPrefix(spec.Staging, claimed.Attempt)), body)
		res.EvalSHA = jobspec.SHA256(body)
	}
	rs, _ := fleet.Encode(res)
	state := "succeeded"
	if status != "ok" {
		state = "failed"
	}
	if _, err := store.CAS(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		n := *cur
		n.State, n.Result, n.ResultSig, n.Changed = state, rs, fleet.SignResult(l.key, j.ID, cur.Epoch, rs), time.Now()
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
	if rec := l.build(id); rec.State != buildctl.StatePending || rec.Number != 1 {
		t.Fatalf("created %s #%d", rec.State, rec.Number)
	}
	l.pass()
	j := l.job(id)
	spec, _ := fleet.DecodeSpec(j.Spec)
	if j.State != "queued" || spec.Kind != buildctl.KindEvalBuild || !slices.Equal(spec.Targets, []string{"@event:push:main"}) || spec.Repo != "f1/1" {
		t.Fatalf("job %s %+v", j.State, spec)
	}
	if rec := l.build(id); rec.State != buildctl.StateQueued || rec.Phase != buildctl.PhaseEvaluating {
		t.Fatalf("after plan: %s %s", rec.State, rec.Phase)
	}
	l.run(j, "ok", okReport("ci"))
	l.pass()
	rec := l.build(id)
	if rec.State != buildctl.StateSucceeded || len(rec.Pipelines) != 1 || rec.Pipelines[0].Name != "ci" || rec.Counts.Built != 1 || rec.Counts.Cached != 1 {
		t.Fatalf("build: %s %+v %+v errors %+v", rec.State, rec.Pipelines, rec.Counts, rec.Errors)
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
	running, err := store.Get(l.ctx, l.control, &store.RunningBuilds{Name: "running"})
	if err != nil || len(running.Builds) != 0 {
		t.Errorf("bidx/running after the build: %+v %v", running, err)
	}
	if p, err := l.b.JobProject(l.ctx, j.ID); err != nil || p != "acme" {
		t.Errorf("job %s resolves to %q %v", j.ID, p, err)
	}
	nodes, _ := l.b.Nodes(l.ctx, id)
	if nodes == nil || len(nodes.Nodes) != 2 {
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
	if rec.State != buildctl.StateFailed || len(rec.Errors) == 0 || !strings.Contains(rec.Errors[0].Message, "evaluation result rejected: pipeline match mismatch") {
		t.Fatalf("forged report: %s %+v", rec.State, rec.Errors)
	}
	if len(rec.Pipelines) != 0 {
		t.Errorf("pipelines of a rejected report were taken: %+v", rec.Pipelines)
	}
	// a report whose bytes differ from the signed sha is rejected too
	id2, _ := l.b.CreateFromEvent(l.ctx, "acme", "k2", push("main"))
	l.pass()
	j := l.job(id2)
	l.run(j, "ok", okReport("ci"))
	spec, _ := fleet.DecodeSpec(j.Spec)
	l.art.put(jobspec.ReportKey(jobspec.StagingPrefix(spec.Staging, 1)), []byte(`{"v":1}`))
	l.pass()
	if rec := l.build(id2); rec.State != buildctl.StateFailed {
		t.Errorf("tampered report: build %s", rec.State)
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
	got := l.build(first.ID)
	if got.State != buildctl.StateSuperseded || got.SupersededBy != second.Number {
		t.Fatalf("first: %s sp %d", got.State, got.SupersededBy)
	}
	pb, _ := l.b.ToProto(l.ctx, got)
	if pb.GetTrigger().GetSupersededBy() != second.ID {
		t.Errorf("superseded_by %q, want %s", pb.GetTrigger().GetSupersededBy(), second.ID)
	}
	if s := l.statuses.states(buildctl.ContextEvaluate); len(s) == 0 || !slices.ContainsFunc(s, func(x string) bool {
		return strings.HasPrefix(x, "cancelled") && strings.Contains(x, fmt.Sprintf("superseded by #%d", second.Number))
	}) {
		t.Errorf("statuses of the superseded build: %v", s)
	}
	if st := l.build(second.ID).State; st != buildctl.StateQueued {
		t.Errorf("second: %s", st)
	}
	l.pass()
	if j, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: got.Jobs[0].ID}); err == nil && j.State != "cancelled" {
		t.Errorf("job of the superseded build: %s", j.State)
	}
}

func queue(t *testing.T, l *lab) {
	a, _ := l.b.CreateFromEvent(l.ctx, "acme", "k1", push("main"))
	l.pass()
	ev := push("main")
	ev.Push.After = strings.Repeat("cd", 20)
	bID, _ := l.b.CreateFromEvent(l.ctx, "acme", "k2", ev)
	l.pass()
	if st := l.build(bID).State; st != buildctl.StatePending {
		t.Fatalf("newer push to main: %s, want pending (queue)", st)
	}
	l.run(l.job(a), "ok", okReport("ci"))
	l.pass()
	l.pass()
	if st := l.build(a).State; st != buildctl.StateSucceeded {
		t.Fatalf("older: %s", st)
	}
	if st := l.build(bID).State; st != buildctl.StateQueued {
		t.Fatalf("newer after the older finished: %s", st)
	}
}

func cancelRetryRerun(t *testing.T, l *lab) {
	rec := apiBuild(t, l, "refs/heads/main", sha1)
	l.pass()
	l.run(l.job(rec.ID), "failed", okReport("ci"), "//cmd/api:test")
	l.pass()
	failed := l.build(rec.ID)
	if failed.State != buildctl.StateFailed || failed.Pipelines[0].State != buildctl.StateFailed {
		t.Fatalf("failed build: %s %+v", failed.State, failed.Pipelines)
	}
	if _, err := l.b.Cancel(l.ctx, rec.ID, "u"); !errors.Is(err, buildctl.ErrFinished) {
		t.Errorf("cancel of a finished build: %v", err)
	}
	retry, err := l.b.Retry(l.ctx, rec.ID, "user:u1")
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := l.b.ToProto(l.ctx, retry)
	if pb.GetTrigger().GetRetryOf() != rec.ID || pb.GetAttempt() != 2 || pb.GetTrigger().GetKind().String() != "TRIGGER_KIND_RETRY" || retry.Pipeline != "ci" {
		t.Errorf("retry: %v pipeline %q", pb.GetTrigger(), retry.Pipeline)
	}
	rerun, err := l.b.Rerun(l.ctx, rec.ID, "user:u1")
	if err != nil {
		t.Fatal(err)
	}
	if pb, _ := l.b.ToProto(l.ctx, rerun); pb.GetTrigger().GetRerunOf() != rec.ID {
		t.Errorf("rerun: %v", pb.GetTrigger())
	}
	l.pass() // retry and rerun: parallel builds of main queue behind each other
	running := l.build(retry.ID)
	if running.State != buildctl.StateQueued {
		t.Fatalf("retry build %s", running.State)
	}
	c, err := l.b.Cancel(l.ctx, retry.ID, "u")
	if err != nil || c.State != buildctl.StateCancelled {
		t.Fatalf("cancel: %v %v", c, err)
	}
	l.pass()
	if _, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: running.Jobs[0].ID}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancelled job not finalised: %v", err)
	}
}

func infraRetries(t *testing.T, l *lab) {
	rec := apiBuild(t, l, "refs/heads/main", sha1)
	l.pass()
	for i := range 3 {
		j := l.job(rec.ID)
		if j.Attempt != int64(i) {
			t.Fatalf("attempt %d: job %s has attempt %d", i+1, j.ID, j.Attempt)
		}
		l.run(j, "infra", nil)
		l.pass()
	}
	got := l.build(rec.ID)
	if got.State != buildctl.StateFailed || len(got.Errors) == 0 || !strings.Contains(got.Errors[0].Message, "failed 3 times on workers (infrastructure)") {
		t.Fatalf("after 3 infra failures: %s %+v (%d jobs)", got.State, got.Errors, len(got.Jobs))
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
	if _, err := store.CAS(l.ctx, l.fleet, &store.JobRecord{ID: id}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		n := *cur
		n.Promotion, n.NonReproducible = pm, nr
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
	if rec := l.build(id); rec.State != buildctl.StateRunning || rec.Phase != buildctl.PhasePromoting {
		t.Fatalf("waiting for promotion: %s %s", rec.State, rec.Phase)
	}
	if _, err := store.Get(l.ctx, l.fleet, &store.JobRecord{ID: j.ID}); err != nil {
		t.Fatalf("job finalised before its promotion: %v", err)
	}
	setPM(t, l, j.ID, "blocked: staging incomplete")
	l.pass()
	j2 := l.job(id)
	if j2.ID == j.ID || j2.State != "queued" {
		t.Fatalf("blocked promotion: next job %s %s", j2.ID, j2.State)
	}
	l.run(j2, "ok", okReport("ci"))
	setPM(t, l, j2.ID, "done", "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-x")
	l.pass()
	rec := l.build(id)
	if rec.State != buildctl.StateSucceeded {
		t.Fatalf("after promotion done: %s %+v", rec.State, rec.Errors)
	}
	s := &buildctl.Service{B: l.b}
	pj, err := s.B.ToProto(l.ctx, rec)
	if err != nil || pj.GetState().String() != "BUILD_STATE_SUCCEEDED" {
		t.Fatal(pj, err)
	}
	var row store.BuildJob
	for _, r := range rec.Jobs {
		if r.ID == j2.ID {
			row = r
		}
	}
	if row.Promotion != "done" || len(rec.NonReproducible[j2.ID]) != 1 {
		t.Errorf("row %+v nr %v", row, rec.NonReproducible)
	}
}
