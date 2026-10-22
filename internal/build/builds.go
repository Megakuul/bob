package buildctl

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

// Build states (SPEC §05.6.2, BuildRecord `st`) and phases (`ph`).
const (
	StatePending    = "pending"
	StateQueued     = "queued"
	StateRunning    = "running"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
	StateSuperseded = "superseded"
	StateSkipped    = "skipped"

	PhaseEvaluating = "evaluating"
	PhaseBuilding   = "building"
	PhasePromoting  = "promoting"
)

// Terminal reports whether a build state is absorbing.
func Terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled, StateSuperseded, StateSkipped:
		return true
	}
	return false
}

// Trigger kinds (BuildRecord.tr.k).
const (
	TriggerPush   = "push"
	TriggerTag    = "tag"
	TriggerPR     = "pr"
	TriggerAPI    = "api"
	TriggerRemote = "remote"
	TriggerRerun  = "rerun"
	TriggerRetry  = "retry"
)

// Job kinds (JobSpec.Kind).
const (
	KindEvalBuild = "evalbuild"
	KindEval      = "eval"
	KindBatch     = "batch"
)

// Limits (R-NFR-005, SPEC §05.4.2).
const (
	MaxRunningBuilds = 2000
	maxPlatforms     = 8
	maxVariants      = 8
	maxReason        = 200
	maxLabel         = 1024
	maxErrors        = 20
)

// Fleet is the fleet side the controller enqueues through (fleetsrv.Fleet).
type Fleet interface {
	AddClass(ctx context.Context, c store.Class) (string, error)
	Enqueue(ctx context.Context, class string, j orchestrator.Job) (slot uint64, woken []string, err error)
}

// Canceller asks the owner of a running job to stop at once (orchestrator.Hub over Attach);
// best effort, the job watch of the worker sees `cx` anyway.
type Canceller interface {
	CancelJob(ctx context.Context, jobID string) error
}

// Objects reads the artifacts bucket (cache.Objects): evaluation reports, source snapshots.
type Objects interface {
	Read(ctx context.Context, key string, max int64) (body []byte, etag string, err error)
	Head(ctx context.Context, key string) (*cache.Info, error)
}

// Project is what the controller needs of a project (§06 ProjectService owns the record).
type Project struct {
	ID            string
	DefaultBranch string // "" = main
	Repo          string // JobSpec.Repo of the linked repository ("<forgeID>/<repoID>"); "" = none
}

// Projects looks projects up; an unknown project is ErrNoProject.
type Projects interface {
	Project(ctx context.Context, id string) (*Project, error)
}

// ErrNoProject is the answer of Projects for an unknown project.
var ErrNoProject = errors.New("project not found")

// AnyProject accepts every syntactically valid project id with no linked repository. It is
// the Projects of a server without ProjectService (decision D-IB11-3).
type AnyProject struct{}

var projectID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Project implements Projects.
func (AnyProject) Project(_ context.Context, id string) (*Project, error) {
	if !projectID.MatchString(id) {
		return nil, ErrNoProject
	}
	return &Project{ID: id}, nil
}

// StatusSink writes a commit status to the repository linked to a project (the forge adapter
// of its integration, §6.9.4). It returns an error to have the status retried.
type StatusSink interface {
	SetStatus(ctx context.Context, project string, s forge.Status) error
}

// Config configures the build controller of one orchestrator.
type Config struct {
	Name           string // orchestrator id
	Control, Jobs  *store.Bucket
	Fleet          Fleet
	Cancel         Canceller  // may be nil
	Artifacts      Objects    // may be nil: reports cannot be read, builds fail at evaluation
	Projects       Projects   // nil = AnyProject
	Statuses       StatusSink // nil = no commit statuses
	MaxAttempts    int        // fleet.max_attempts (3)
	JobShards      int        // fleet.job_shards (16)
	AwaitPromotion bool       // succeeded jobs wait for JobRecord.pm (the promotion controller runs)
	BaseURL        string     // web_url base; "" = no web_url
	// DefaultPlatform is the platform of builds that name none ("" = this host's).
	DefaultPlatform string
	Interval        time.Duration // controller interval (5 s)
	Skew            time.Duration // fleet.skew
	Now             func() time.Time
	Log             *slog.Logger
}

// Builds is the build controller and the state it shares with BuildService.
type Builds struct {
	cfg   Config
	queue *orchestrator.Queue // class-less: job reads, cancel, finalise
	wake  chan struct{}

	mu      sync.Mutex
	keys    map[string]ed25519.PublicKey
	pages   map[string]*store.PagedIndex[store.BuildEntry]
	retry   map[string]statusRetry // status retry backoff per build|context
	running store.Cache[store.RunningBuilds]
	failed  store.Cache[store.FailedBuilds]
	records store.Cache[store.BuildRecord]
}

// New performs no I/O.
func New(cfg Config) (*Builds, error) {
	if cfg.Control == nil || cfg.Jobs == nil || cfg.Fleet == nil {
		return nil, errors.New("buildctl: needs the control and fleet buckets and the fleet")
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.JobShards <= 0 {
		cfg.JobShards = 16
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Projects == nil {
		cfg.Projects = AnyProject{}
	}
	if cfg.DefaultPlatform == "" {
		cfg.DefaultPlatform = HostPlatform()
	}
	b := &Builds{cfg: cfg, wake: make(chan struct{}, 1), keys: map[string]ed25519.PublicKey{},
		pages: map[string]*store.PagedIndex[store.BuildEntry]{}, retry: map[string]statusRetry{}}
	b.queue = &orchestrator.Queue{Fleet: cfg.Jobs, Control: cfg.Control, Shards: cfg.JobShards,
		WorkerKey: b.workerKey, Now: cfg.Now}
	return b, nil
}

// HostPlatform is the Nix system of this host.
func HostPlatform() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64-linux"
	case "amd64":
		return "x86_64-linux"
	}
	return runtime.GOARCH + "-linux"
}

func (b *Builds) now() time.Time { return b.cfg.Now().UTC() }

// Wake runs a controller pass soon (after a create or cancel on this orchestrator).
func (b *Builds) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// workerKey returns the result signing key of worker wid (worker/<wid>.sp, never changes for a wid).
func (b *Builds) workerKey(wid string) ed25519.PublicKey {
	b.mu.Lock()
	k, ok := b.keys[wid]
	b.mu.Unlock()
	if ok || wid == "" {
		return k
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := store.Get(ctx, b.cfg.Jobs, &store.WorkerRecord{Wid: wid})
	if err != nil || len(rec.SigPub) != ed25519.PublicKeySize {
		return nil
	}
	b.mu.Lock()
	b.keys[wid] = ed25519.PublicKey(rec.SigPub)
	b.mu.Unlock()
	return rec.SigPub
}

// buildPages returns the (cached) bpage index of a project.
func (b *Builds) buildPages(project string) *store.PagedIndex[store.BuildEntry] {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.pages[project]; ok {
		return p
	}
	p := store.BuildPages(b.cfg.Control, project)
	b.pages[project] = p
	return p
}

// Request creates one build.
type Request struct {
	ID        string // preset build id (CreateFromEvent); "" = a new one
	Project   string
	Trigger   store.BuildTrigger
	Source    store.BuildSource
	Branch    string
	Commit    string
	Author    string
	Pipelines []string // run exactly these pipelines (CreateBuild{pipeline}, retry, rerun)
	Targets   []string // plain targets (no pipeline)
	Platforms []string
	Variants  []string
	Test      bool
	Reason    string
	RetryOf   uint64
	RerunOf   uint64
	Attempt   int
}

// InvalidError is a request the caller must fix; Field names the request field.
type InvalidError struct{ Field, Msg string }

func (e *InvalidError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &InvalidError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// ErrLimit is a request over a server limit (R-NFR-005); the message names the limit.
var ErrLimit = errors.New("limit reached")

var (
	shaRE  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	refRE  = regexp.MustCompile(`^refs/(heads|tags|pull|merge-requests)/[^\s\x00-\x1f]{1,240}$`)
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// validate checks a request (hostile input) and normalises its platforms.
func (r *Request) validate() error {
	if !projectID.MatchString(r.Project) {
		return invalid("project_id", "invalid project id")
	}
	switch r.Trigger.Kind {
	case TriggerPush, TriggerTag, TriggerPR, TriggerAPI, TriggerRemote, TriggerRerun, TriggerRetry:
	default:
		return invalid("trigger", "unknown trigger kind %q", r.Trigger.Kind)
	}
	src := r.Source
	switch {
	case src.Upload != "" && (src.Ref != "" || src.SHA != ""):
		return invalid("source", "a build has either a git source or an uploaded snapshot")
	case src.Upload != "" && !sha256Hex.MatchString(src.Upload):
		return invalid("source.upload_sha256", "not a sha256")
	case src.Upload == "" && src.Ref == "" && src.SHA == "":
		return invalid("source", "a git ref or sha, or an uploaded snapshot, is required")
	case src.SHA != "" && !shaRE.MatchString(src.SHA):
		return invalid("source.git.sha", "not a commit sha")
	case src.Ref != "" && !refRE.MatchString(src.Ref):
		return invalid("source.git.ref", "not a ref (refs/heads/…, refs/tags/…, refs/pull/…)")
	case src.FetchRef != "" && !refRE.MatchString(src.FetchRef):
		return invalid("source.git.fetch_ref", "not a ref")
	}
	if r.Trigger.Ref != "" && !refRE.MatchString(r.Trigger.Ref) {
		return invalid("trigger.ref", "not a ref")
	}
	if r.Commit != "" && !shaRE.MatchString(r.Commit) {
		return invalid("commit", "not a commit sha")
	}
	if len(r.Pipelines) > 0 && len(r.Targets) > 0 {
		return invalid("pipeline", "a build runs a pipeline or targets, not both")
	}
	for _, p := range r.Pipelines {
		if !pipelineName.MatchString(p) {
			return invalid("pipeline", "invalid pipeline name %q", p)
		}
	}
	for _, t := range r.Targets {
		if t == "" || len(t) > maxLabel || strings.HasPrefix(t, "@") || strings.ContainsAny(t, "\x00\n ") {
			return invalid("targets", "invalid target %q", t)
		}
	}
	if len(r.Platforms) > maxPlatforms {
		return invalid("platforms", "at most %d platforms", maxPlatforms)
	}
	for i, p := range r.Platforms {
		n, ok := NormPlatform(p)
		if !ok {
			return invalid("platforms", "unknown platform %q", p)
		}
		r.Platforms[i] = n
	}
	r.Platforms = slices.Compact(slices.Sorted(slices.Values(r.Platforms)))
	if len(r.Variants) > maxVariants {
		return invalid("variants", "at most %d variants", maxVariants)
	}
	for _, v := range r.Variants {
		if !nameRE.MatchString(v) {
			return invalid("variants", "invalid variant %q", v)
		}
	}
	if len(r.Reason) > maxReason {
		return invalid("reason", "at most %d bytes", maxReason)
	}
	sel := selectionOfRequest(r)
	if !sel.RunsPipelines() && len(sel.Labels) == 0 {
		return invalid("pipeline", "a pipeline or targets are required")
	}
	if _, err := sel.Encode(); err != nil {
		return invalid("targets", "%v", err)
	}
	return nil
}

// NormPlatform maps "aarch64-linux", "linux/arm64" and the x86_64 spellings to the Nix system.
func NormPlatform(p string) (string, bool) {
	switch p {
	case "aarch64-linux", "linux/arm64", "arm64":
		return "aarch64-linux", true
	case "x86_64-linux", "linux/amd64", "amd64":
		return "x86_64-linux", true
	}
	return "", false
}

// refOf is the git ref of a build.
func refOf(tr store.BuildTrigger, src store.BuildSource, branch string) string {
	switch {
	case tr.Ref != "":
		return tr.Ref
	case src.Ref != "":
		return src.Ref
	case branch != "":
		return "refs/heads/" + branch
	}
	return ""
}

// selection derives what a build runs from its record (stable for the build's life):
// explicit pipelines, else plain targets, else the forge event of its ref.
func selection(pipelines string, targets []string, ref string, test bool, variants []string, upload string) Selection {
	s := Selection{Test: test, Variants: variants, Upload: upload}
	switch {
	case pipelines != "":
		s.Pipelines = strings.Split(pipelines, ",")
	case len(targets) > 0:
		s.Labels = targets
	case strings.HasPrefix(ref, "refs/heads/"):
		s.Event, s.Ref = EventPush, strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		s.Event, s.Ref = EventTag, strings.TrimPrefix(ref, "refs/tags/")
	case strings.HasPrefix(ref, "refs/pull/"), strings.HasPrefix(ref, "refs/merge-requests/"):
		s.Event = EventPullRequest
	}
	return s
}

func selectionOfRequest(r *Request) Selection {
	return selection(strings.Join(r.Pipelines, ","), r.Targets, refOf(r.Trigger, r.Source, r.Branch), r.Test, r.Variants, r.Source.Upload)
}

// SelectionOf is what build rec runs.
func SelectionOf(rec *store.BuildRecord) Selection {
	return selection(rec.Pipeline, rec.Targets, refOf(rec.Trigger, rec.Source, rec.Branch), rec.Test, rec.Variants, rec.Source.Upload)
}

// Create creates a build in state pending (SPEC §05.6.2 trigger): CAS pctr.b+1 → n, then the
// index entries (bidx/running, bpage, bbr; intent first), then the record. The controller
// plans it on its next pass.
func (b *Builds) Create(ctx context.Context, r Request) (*store.BuildRecord, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	proj, err := b.cfg.Projects.Project(ctx, r.Project)
	if err != nil {
		return nil, err
	}
	if r.Source.Upload != "" {
		if err := b.checkUpload(ctx, r.Project, r.Source.Upload); err != nil {
			return nil, err
		}
	} else if proj.Repo == "" && r.Source.Upload == "" && b.cfg.Projects != (AnyProject{}) {
		return nil, invalid("source", "project %s has no linked repository; upload a source snapshot", r.Project)
	}
	running, _, err := store.GetCached(ctx, b.cfg.Control, &b.running, &store.RunningBuilds{Name: "running"})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if running != nil && len(running.Builds) >= MaxRunningBuilds {
		return nil, fmt.Errorf("%w: %d running builds (the limit of bidx/running)", ErrLimit, MaxRunningBuilds)
	}
	now := b.now()
	ctr, err := store.CAS(ctx, b.cfg.Control, &store.ProjectCounters{Project: r.Project}, func(cur *store.ProjectCounters) (*store.ProjectCounters, error) {
		next := store.ProjectCounters{Project: r.Project}
		if cur != nil {
			next = *cur
		}
		next.Builds++
		return &next, nil
	})
	if err != nil {
		return nil, fmt.Errorf("pctr/%s: %w", r.Project, err)
	}
	n := ctr.Builds
	id := r.ID
	if id == "" {
		if id, err = NewBuildID(now); err != nil {
			return nil, err
		}
	}
	lg, _ := logstore.LogPartition(id, logstore.DefaultLogShards)
	commit := r.Commit
	if commit == "" {
		commit = r.Source.SHA
	}
	attempt := r.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	rec := &store.BuildRecord{ID: id, Project: r.Project, Number: n, State: StatePending, Trigger: r.Trigger,
		Branch: r.Branch, Commit: commit, Author: r.Author, Created: now, Platforms: r.Platforms,
		RetryOf: r.RetryOf, RerunOf: r.RerunOf, Source: r.Source, Targets: r.Targets, Variants: r.Variants,
		LogPartition: lg, Attempt: attempt, Pipeline: strings.Join(r.Pipelines, ","), Test: r.Test, Reason: r.Reason}
	if len(rec.Platforms) == 0 {
		rec.Platforms = []string{b.cfg.DefaultPlatform}
	}
	// intent first: index entries before the record (R-SRV-101)
	if err := b.setRunning(ctx, rec); err != nil {
		return nil, err
	}
	if err := b.buildPages(r.Project).Set(ctx, n-1, entryOf(rec)); err != nil {
		return nil, fmt.Errorf("bpage: %w", err)
	}
	if ref := refOf(rec.Trigger, rec.Source, rec.Branch); ref != "" {
		if err := b.addBranchEntry(ctx, r.Project, ref, n); err != nil {
			return nil, err
		}
	}
	if err := store.Create(ctx, b.cfg.Control, rec); err != nil {
		return nil, fmt.Errorf("build/%s: %w", id, err)
	}
	b.cfg.Log.Info("build created", "build", id, "project", r.Project, "number", n, "trigger", r.Trigger.Kind)
	b.Wake()
	return rec, nil
}

// checkUpload requires the snapshot src/<project>/<sha>.tar.zst to exist (PUT /upload first).
func (b *Builds) checkUpload(ctx context.Context, project, sha string) error {
	if b.cfg.Artifacts == nil {
		return invalid("source.upload_sha256", "source uploads are not available on this server")
	}
	_, err := b.cfg.Artifacts.Head(ctx, UploadKey(project, sha))
	if errors.Is(err, cache.ErrNotFound) {
		return invalid("source.upload_sha256", "no uploaded snapshot %s (PUT /upload/source/sha256/<hex> first)", sha)
	}
	return err
}

// UploadKey is the artifacts key of a source snapshot (SPEC §6.9.7).
func UploadKey(project, sha string) string { return "src/" + project + "/" + sha + ".tar.zst" }

// setRunning adds rec to bidx/running (every non-terminal build is listed there, decision
// D-IB11-1; the started time is set when its first job is claimed).
func (b *Builds) setRunning(ctx context.Context, rec *store.BuildRecord) error {
	_, err := store.CAS(ctx, b.cfg.Control, &store.RunningBuilds{Name: "running"}, func(cur *store.RunningBuilds) (*store.RunningBuilds, error) {
		next := store.RunningBuilds{Name: "running", Builds: map[string]store.RunningBuild{}}
		if cur != nil {
			for k, v := range cur.Builds {
				next.Builds[k] = v
			}
		}
		e := store.RunningBuild{Project: rec.Project, N: rec.Number, Started: rec.Started}
		if old, ok := next.Builds[rec.ID]; ok && old == e {
			return nil, store.ErrNoChange
		}
		if _, ok := next.Builds[rec.ID]; !ok && len(next.Builds) >= MaxRunningBuilds {
			return nil, fmt.Errorf("%w: %d running builds (the limit of bidx/running)", ErrLimit, MaxRunningBuilds)
		}
		next.Builds[rec.ID] = e
		return &next, nil
	})
	if err != nil {
		return fmt.Errorf("bidx/running: %w", err)
	}
	return nil
}

// dropRunning removes a build from bidx/running.
func (b *Builds) dropRunning(ctx context.Context, id string) error {
	_, err := store.CAS(ctx, b.cfg.Control, &store.RunningBuilds{Name: "running"}, func(cur *store.RunningBuilds) (*store.RunningBuilds, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if _, ok := cur.Builds[id]; !ok {
			return nil, store.ErrNoChange
		}
		next := store.RunningBuilds{Name: "running", Builds: map[string]store.RunningBuild{}}
		for k, v := range cur.Builds {
			if k != id {
				next.Builds[k] = v
			}
		}
		return &next, nil
	})
	return err
}

// addFailed appends a build to the bidx/failed ring (last 200).
func (b *Builds) addFailed(ctx context.Context, rec *store.BuildRecord) error {
	_, err := store.CAS(ctx, b.cfg.Control, &store.FailedBuilds{Name: "failed"}, func(cur *store.FailedBuilds) (*store.FailedBuilds, error) {
		next := store.FailedBuilds{Name: "failed"}
		if cur != nil {
			if slices.ContainsFunc(cur.Builds, func(f store.FailedBuild) bool { return f.Build == rec.ID }) {
				return nil, store.ErrNoChange
			}
			next.Builds = slices.Clone(cur.Builds)
		}
		next.Builds = append(next.Builds, store.FailedBuild{Build: rec.ID, Project: rec.Project, N: rec.Number, Finished: rec.Finished})
		if len(next.Builds) > 200 {
			next.Builds = next.Builds[len(next.Builds)-200:]
		}
		return &next, nil
	})
	return err
}

// addBranchEntry appends build number n to the branch index of ref (bbrm count, bbr page).
func (b *Builds) addBranchEntry(ctx context.Context, project, ref string, n uint64) error {
	key, err := store.BranchCounterKey(project, ref)
	if err != nil {
		return err
	}
	ctr, err := store.CAS(ctx, b.cfg.Control, key, func(cur *store.BranchCounter) (*store.BranchCounter, error) {
		next := *key
		if cur != nil {
			next = *cur
		}
		next.Count++
		return &next, nil
	})
	if err != nil {
		return fmt.Errorf("bbrm: %w", err)
	}
	idx, err := store.BranchBuilds(b.cfg.Control, project, ref)
	if err != nil {
		return err
	}
	if err := idx.Set(ctx, ctr.Count-1, n); err != nil {
		return fmt.Errorf("bbr: %w", err)
	}
	return nil
}

// entryOf is the bpage entry of a build.
func entryOf(rec *store.BuildRecord) store.BuildEntry {
	e := store.BuildEntry{N: rec.Number, Build: rec.ID, State: rec.State, Branch: rec.Branch,
		Commit: rec.Commit, Trigger: rec.Trigger.Kind, Created: rec.Created}
	if len(e.Commit) > 12 {
		e.Commit = e.Commit[:12]
	}
	if !rec.Finished.IsZero() && !rec.Started.IsZero() {
		e.Duration = rec.Finished.Sub(rec.Started)
	}
	return e
}

// syncEntry rewrites the bpage entry of rec when its state or duration changed.
func (b *Builds) syncEntry(ctx context.Context, rec *store.BuildRecord) error {
	idx := b.buildPages(rec.Project)
	want := entryOf(rec)
	page, err := idx.Page(ctx, idx.LastPage(rec.Number))
	if err == nil {
		if cur := page[(rec.Number-1)%uint64(idx.P)]; cur == want {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return idx.Set(ctx, rec.Number-1, want)
}

// Get reads a build record (GET, the source of truth).
func (b *Builds) Get(ctx context.Context, id string) (*store.BuildRecord, error) {
	if len(id) != 26 {
		return nil, store.ErrNotFound
	}
	return store.Get(ctx, b.cfg.Control, &store.BuildRecord{ID: id})
}

// ByNumber returns the id of build n of a project ("" when unknown or tombstoned).
func (b *Builds) ByNumber(ctx context.Context, project string, n uint64) (string, error) {
	if n == 0 {
		return "", nil
	}
	idx := b.buildPages(project)
	page, err := idx.Page(ctx, idx.LastPage(n))
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	e := page[(n-1)%uint64(idx.P)]
	if e.N != n {
		return "", nil
	}
	return e.Build, nil
}

// update CASes build/<id> with fn; fn returning store.ErrNoChange skips the write.
func (b *Builds) update(ctx context.Context, id string, fn func(cur *store.BuildRecord) error) (*store.BuildRecord, error) {
	return store.CAS(ctx, b.cfg.Control, &store.BuildRecord{ID: id}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		next := *cur
		next.Jobs = slices.Clone(cur.Jobs)
		next.Pipelines = slices.Clone(cur.Pipelines)
		next.Errors = slices.Clone(cur.Errors)
		if cur.Reported != nil {
			next.Reported = make(map[string]string, len(cur.Reported))
			for k, v := range cur.Reported {
				next.Reported[k] = v
			}
		}
		if err := fn(&next); err != nil {
			return nil, err
		}
		return &next, nil
	})
}

// ErrFinished is the refusal of CancelBuild on a terminal build.
var ErrFinished = errors.New("build is already finished")

// Cancel moves a non-terminal build to cancelled (user cancel) and cancels its jobs.
func (b *Builds) Cancel(ctx context.Context, id, by string) (*store.BuildRecord, error) {
	return b.stop(ctx, id, StateCancelled, 0)
}

// stop ends a non-terminal build as cancelled or superseded (by build number sp) and cancels
// its queued and running jobs; the controller finalises them.
func (b *Builds) stop(ctx context.Context, id, state string, sp uint64) (*store.BuildRecord, error) {
	now := b.now()
	rec, err := b.update(ctx, id, func(cur *store.BuildRecord) error {
		if Terminal(cur.State) {
			return ErrFinished
		}
		cur.State, cur.Finished, cur.SupersededBy = state, now, sp
		for i := range cur.Pipelines {
			if !Terminal(cur.Pipelines[i].State) {
				cur.Pipelines[i].State = state
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	b.cancelJobs(ctx, rec)
	if err := b.syncEntry(ctx, rec); err != nil {
		b.cfg.Log.Warn("build: bpage entry", "build", id, "err", err.Error())
	}
	b.Wake()
	return rec, nil
}

// cancelJobs requests cancellation of every non-terminal job of rec (queued: at once;
// claimed/running: cx plus CancelJob over Attach).
func (b *Builds) cancelJobs(ctx context.Context, rec *store.BuildRecord) {
	for _, j := range rec.Jobs {
		if orchestrator.Terminal(j.State) {
			continue
		}
		if err := b.queue.Cancel(ctx, j.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			b.cfg.Log.Warn("build: cancel job", "job", j.ID, "err", err.Error())
			continue
		}
		if b.cfg.Cancel != nil {
			_ = b.cfg.Cancel.CancelJob(ctx, j.ID)
		}
	}
}

// Retry creates the retry of a failed, cancelled or superseded build: trigger retry, same
// source, the pipelines that did not succeed (or the same targets), attempt + 1.
func (b *Builds) Retry(ctx context.Context, id, by string) (*store.BuildRecord, error) {
	old, err := b.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch old.State {
	case StateFailed, StateCancelled, StateSuperseded:
	default:
		return nil, fmt.Errorf("%w: only failed, cancelled or superseded builds can be retried (build is %s)", ErrState, old.State)
	}
	r := again(old, TriggerRetry, by)
	r.RetryOf, r.Attempt = old.Number, max(old.Attempt, 1)+1
	if old.Evaluated && len(old.Pipelines) > 0 {
		r.Pipelines = nil
		for _, p := range old.Pipelines {
			if p.State != StateSucceeded {
				r.Pipelines = append(r.Pipelines, p.Name)
			}
		}
		if len(r.Pipelines) == 0 {
			r.Pipelines = pipelineNames(old.Pipelines)
		}
	}
	return b.Create(ctx, r)
}

// ErrState is an action the build's state does not allow.
var ErrState = errors.New("build state does not allow this")

// Rerun creates a new build with the same source and pipelines (any terminal state).
func (b *Builds) Rerun(ctx context.Context, id, by string) (*store.BuildRecord, error) {
	old, err := b.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !Terminal(old.State) {
		return nil, fmt.Errorf("%w: only finished builds can be re-run (build is %s)", ErrState, old.State)
	}
	r := again(old, TriggerRerun, by)
	r.RerunOf = old.Number
	return b.Create(ctx, r)
}

// again is a request repeating old: same source, ref, platforms, variants and selection.
func again(old *store.BuildRecord, kind, by string) Request {
	r := Request{Project: old.Project, Source: old.Source, Branch: old.Branch, Commit: old.Commit, Author: old.Author,
		Targets: old.Targets, Platforms: slices.Clone(old.Platforms), Variants: old.Variants, Test: old.Test}
	r.Trigger = store.BuildTrigger{Kind: kind, By: by, Ref: refOf(old.Trigger, old.Source, old.Branch),
		PR: old.Trigger.PR, Fork: old.Trigger.Fork}
	if old.Pipeline != "" {
		r.Pipelines = strings.Split(old.Pipeline, ",")
	} else if old.Evaluated && len(old.Targets) == 0 && len(old.Pipelines) > 0 {
		r.Pipelines = pipelineNames(old.Pipelines)
	}
	return r
}

func pipelineNames(ps []store.PipelineRun) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
