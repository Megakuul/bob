package buildctl

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/megakuul/bob/internal/build/jobspec"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/safe"
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

// UploadProjects is implemented by a Projects that creates a project on the first upload
// (`--remote`) build that names it: a project without a repository (decision D-IB13-1).
type UploadProjects interface {
	ForUpload(ctx context.Context, id string) (*Project, error)
}

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
	// Finished is called once when a build becomes terminal (outgoing webhooks, §6.9.8).
	Finished func(ctx context.Context, rec *store.BuildRecord)
	// Evaluated is called after an evaluation report of build rec was accepted (R-PIPE-003
	// verified), with the build as updated by it (the pipelines cache of ListPipelines).
	Evaluated func(ctx context.Context, rec *store.BuildRecord, r *jobspec.Report)
	Interval  time.Duration // controller interval (5 s)
	Skew      time.Duration // fleet.skew
	Now       func() time.Time
	Log       *slog.Logger
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

	releaser Releaser // SetReleaser; nil = release pipelines publish nothing
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
		cfg.DefaultPlatform = jobspec.HostPlatform()
	}
	b := &Builds{cfg: cfg, wake: make(chan struct{}, 1), keys: map[string]ed25519.PublicKey{},
		pages: map[string]*store.PagedIndex[store.BuildEntry]{}, retry: map[string]statusRetry{}}
	b.queue = &orchestrator.Queue{Fleet: cfg.Jobs, Control: cfg.Control, Shards: cfg.JobShards,
		WorkerKey: b.workerKey, Now: cfg.Now}
	return b, nil
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
	rec, err := store.Get(ctx, b.cfg.Jobs, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil || len(rec.SigPub.Value()) != ed25519.PublicKeySize {
		return nil
	}
	b.mu.Lock()
	b.keys[wid] = ed25519.PublicKey(rec.SigPub.Value())
	b.mu.Unlock()
	return rec.SigPub.Value()
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
	Version   string // release version (CreateBuild{version}); "" = the tag's for a tag build
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
	case src.Upload != "" && !jobspec.IsSHA256(src.Upload):
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
		if !jobspec.PipelineName(p) {
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
	if r.Version != "" && !jobspec.ValidVersion(r.Version) {
		return invalid("version", "invalid release version %q (1 to 64 letters, digits and . + _ - starting with a letter or digit)", r.Version)
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
func selection(pipelines string, targets []string, ref string, test bool, variants []string, upload, version string) jobspec.Selection {
	s := jobspec.Selection{Test: test, Variants: variants, Upload: upload, Version: version}
	if tag, ok := strings.CutPrefix(ref, "refs/tags/"); ok && s.Version == "" {
		// a tag build (also its retry or re-run) builds its releases with the tag's version
		s.Version = jobspec.TagVersion(tag)
	}
	switch {
	case pipelines != "":
		s.Pipelines = strings.Split(pipelines, ",")
	case len(targets) > 0:
		s.Labels = targets
	case strings.HasPrefix(ref, "refs/heads/"):
		s.Event, s.Ref = jobspec.EventPush, strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		s.Event, s.Ref = jobspec.EventTag, strings.TrimPrefix(ref, "refs/tags/")
	case strings.HasPrefix(ref, "refs/pull/"), strings.HasPrefix(ref, "refs/merge-requests/"):
		s.Event = jobspec.EventPullRequest
	}
	return s
}

func selectionOfRequest(r *Request) jobspec.Selection {
	return selection(strings.Join(r.Pipelines, ","), r.Targets, refOf(r.Trigger, r.Source, r.Branch), r.Test, r.Variants, r.Source.Upload, r.Version)
}

// SelectionOf is what build rec runs.
func SelectionOf(rec *store.BuildRecord) jobspec.Selection {
	return selection(rec.Pipeline.Value(), rec.Targets.Value(), refOf(rec.Trigger.Value(), rec.Source.Value(), rec.Branch.Value()), rec.Test.Value(), rec.Variants.Value(), rec.Source.Value().Upload, rec.Version.Value())
}

// Create creates a build in state pending (SPEC §05.6.2 trigger): CAS pctr.b+1 → n, then the
// index entries (bidx/running, bpage, bbr; intent first), then the record. The controller
// plans it on its next pass.
func (b *Builds) Create(ctx context.Context, r Request) (*store.BuildRecord, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	proj, err := b.cfg.Projects.Project(ctx, r.Project)
	up, implicit := b.cfg.Projects.(UploadProjects)
	if err != nil && !(errors.Is(err, ErrNoProject) && implicit && r.Source.Upload != "") {
		return nil, err
	}
	if r.Source.Upload != "" {
		if err := b.checkUpload(ctx, r.Project, r.Source.Upload); err != nil {
			return nil, err
		}
		if proj == nil {
			// the snapshot exists: its uploader may call CreateBuild on this project id
			if _, err := up.ForUpload(ctx, r.Project); err != nil {
				return nil, err
			}
		}
	} else if proj.Repo == "" && r.Source.Upload == "" && b.cfg.Projects != (AnyProject{}) {
		return nil, invalid("source", "project %s has no linked repository; upload a source snapshot", r.Project)
	}
	running, _, err := store.GetCached(ctx, b.cfg.Control, &b.running, &store.RunningBuilds{Name: store.ID("running")})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if running != nil && len(running.Builds.Value()) >= MaxRunningBuilds {
		return nil, fmt.Errorf("%w: %d running builds (the limit of bidx/running)", ErrLimit, MaxRunningBuilds)
	}
	now := b.now()
	var ctr *store.ProjectCounters
	err = retryConflict(ctx, func() (err error) {
		ctr, err = store.CAS(ctx, b.cfg.Control, &store.ProjectCounters{Project: store.ID(r.Project)}, func(cur *store.ProjectCounters) (*store.ProjectCounters, error) {
			next, builds := store.ProjectCounters{Project: store.ID(r.Project)}, uint64(0)
			if cur != nil {
				next, builds = *cur, cur.Builds.Value()
			}
			next.Builds = store.Set(builds + 1)
			return &next, nil
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("pctr/%s: %w", r.Project, err)
	}
	n := ctr.Builds.Value()
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
	platforms := r.Platforms
	if len(platforms) == 0 {
		platforms = []string{b.cfg.DefaultPlatform}
	}
	// intent first: index entries before the record (R-SRV-101). The three indexes are
	// independent documents, so they are written concurrently: the webhook → BuildRecord
	// budget (B-071, 500 ms after receipt) cannot afford three sequential CAS round trips.
	entry := store.BuildEntry{N: n, Build: id, State: StatePending, Branch: r.Branch, Commit: commit,
		Trigger: r.Trigger.Kind, Created: now}
	if len(entry.Commit) > 12 {
		entry.Commit = entry.Commit[:12]
	}
	writes := []<-chan error{
		safe.Go(func() error {
			return retryConflict(ctx, func() error { return b.setRunning(ctx, id, store.RunningBuild{Project: r.Project, N: n}) })
		}),
		safe.Go(func() error {
			if err := retryConflict(ctx, func() error { return b.buildPages(r.Project).Set(ctx, n-1, entry) }); err != nil {
				return fmt.Errorf("bpage: %w", err)
			}
			return nil
		}),
	}
	if ref := refOf(r.Trigger, r.Source, r.Branch); ref != "" {
		writes = append(writes, safe.Go(func() error {
			return retryConflict(ctx, func() error { return b.addBranchEntry(ctx, r.Project, ref, n) })
		}))
	}
	var werr error
	for _, w := range writes {
		werr = errors.Join(werr, <-w)
	}
	if werr != nil {
		return nil, werr
	}
	if err := store.Create(ctx, b.cfg.Control, &store.BuildRecord{ID: store.ID(id), Project: store.Set(r.Project), Number: store.Set(n),
		State: store.Set[string](StatePending), Trigger: store.Set(r.Trigger), Branch: store.Set(r.Branch), Commit: store.Set(commit),
		Author: store.Set(r.Author), Created: store.Set(now), Platforms: store.Set(platforms), RetryOf: store.Set(r.RetryOf),
		RerunOf: store.Set(r.RerunOf), Source: store.Set(r.Source), Targets: store.Set(r.Targets), Variants: store.Set(r.Variants),
		LogPartition: store.Set(lg), Attempt: store.Set(attempt), Pipeline: store.Set(strings.Join(r.Pipelines, ",")),
		Test: store.Set(r.Test), Reason: store.Set(r.Reason), Version: store.Set(r.Version)}); err != nil {
		return nil, fmt.Errorf("build/%s: %w", id, err)
	}
	rec, err := store.Get(ctx, b.cfg.Control, &store.BuildRecord{ID: store.ID(id)})
	if err != nil {
		return nil, fmt.Errorf("build/%s: %w", id, err)
	}
	b.cfg.Log.Info("build created", "build", id, "project", r.Project, "number", n, "trigger", r.Trigger.Kind)
	b.Wake()
	return rec, nil
}

// conflictRounds bounds how often Create repeats a CAS that lost every one of its rounds
// (store.ErrConflict). pctr/<project>, bidx/running and the build page are shared by all
// builds created at the same moment; with tens of concurrent CreateBuild calls (a push to many
// pipelines, a fleet of CI jobs) the eight rounds of one CAS are not enough for the last
// writer (A297: 30 concurrent remote builds of one project, a third failed with "build changed
// concurrently"). Every round re-reads, so a repeat never writes a stale value.
const conflictRounds = 8

func retryConflict(ctx context.Context, fn func() error) error {
	for i := 1; ; i++ {
		err := fn()
		if !errors.Is(err, store.ErrConflict) || i == conflictRounds || ctx.Err() != nil {
			return err
		}
	}
}

// checkUpload requires the snapshot src/<project>/<sha>.tar.zst to exist (PUT /upload first).
func (b *Builds) checkUpload(ctx context.Context, project, sha string) error {
	if b.cfg.Artifacts == nil {
		return invalid("source.upload_sha256", "source uploads are not available on this server")
	}
	_, err := b.cfg.Artifacts.Head(ctx, jobspec.UploadKey(project, sha))
	if errors.Is(err, cache.ErrNotFound) {
		return invalid("source.upload_sha256", "no uploaded snapshot %s (PUT /upload/source/sha256/<hex> first)", sha)
	}
	return err
}

// setRunning adds build id to bidx/running (every non-terminal build is listed there, decision
// D-IB11-1; the started time is set when its first job is claimed).
func (b *Builds) setRunning(ctx context.Context, id string, e store.RunningBuild) error {
	err := store.Change(ctx, b.cfg.Control, &store.RunningBuilds{Name: store.ID("running")}, func(cur *store.RunningBuilds) (*store.RunningBuilds, error) {
		builds := map[string]store.RunningBuild{}
		if cur != nil {
			maps.Copy(builds, cur.Builds.Value())
		}
		if old, ok := builds[id]; ok && old == e {
			return nil, store.ErrNoChange
		}
		if _, ok := builds[id]; !ok && len(builds) >= MaxRunningBuilds {
			return nil, fmt.Errorf("%w: %d running builds (the limit of bidx/running)", ErrLimit, MaxRunningBuilds)
		}
		builds[id] = e
		return &store.RunningBuilds{Name: store.ID("running"), Builds: store.Set(builds)}, nil
	})
	if err != nil {
		return fmt.Errorf("bidx/running: %w", err)
	}
	return nil
}

// dropRunning removes a build from bidx/running.
func (b *Builds) dropRunning(ctx context.Context, id string) error {
	err := store.Change(ctx, b.cfg.Control, &store.RunningBuilds{Name: store.ID("running")}, func(cur *store.RunningBuilds) (*store.RunningBuilds, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if _, ok := cur.Builds.Value()[id]; !ok {
			return nil, store.ErrNoChange
		}
		builds := maps.Clone(cur.Builds.Value())
		delete(builds, id)
		return &store.RunningBuilds{Name: store.ID("running"), Builds: store.Set(builds)}, nil
	})
	return err
}

// addFailed appends a build to the bidx/failed ring (last 200).
func (b *Builds) addFailed(ctx context.Context, rec *store.BuildRecord) error {
	err := store.Change(ctx, b.cfg.Control, &store.FailedBuilds{Name: store.ID("failed")}, func(cur *store.FailedBuilds) (*store.FailedBuilds, error) {
		var builds []store.FailedBuild
		if cur != nil {
			if slices.ContainsFunc(cur.Builds.Value(), func(f store.FailedBuild) bool { return f.Build == rec.ID.Value() }) {
				return nil, store.ErrNoChange
			}
			builds = slices.Clone(cur.Builds.Value())
		}
		builds = append(builds, store.FailedBuild{Build: rec.ID.Value(), Project: rec.Project.Value(), N: rec.Number.Value(), Finished: rec.Finished.Value()})
		if len(builds) > 200 {
			builds = builds[len(builds)-200:]
		}
		return &store.FailedBuilds{Name: store.ID("failed"), Builds: store.Set(builds)}, nil
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
		next, count := *key, uint64(0)
		if cur != nil {
			next, count = *cur, cur.Count.Value()
		}
		next.Count = store.Set(count + 1)
		return &next, nil
	})
	if err != nil {
		return fmt.Errorf("bbrm: %w", err)
	}
	idx, err := store.BranchBuilds(b.cfg.Control, project, ref)
	if err != nil {
		return err
	}
	if err := idx.Set(ctx, ctr.Count.Value()-1, n); err != nil {
		return fmt.Errorf("bbr: %w", err)
	}
	return nil
}

// entryOf is the bpage entry of a build.
func entryOf(rec *store.BuildRecord) store.BuildEntry {
	e := store.BuildEntry{N: rec.Number.Value(), Build: rec.ID.Value(), State: rec.State.Value(), Branch: rec.Branch.Value(),
		Commit: rec.Commit.Value(), Trigger: rec.Trigger.Value().Kind, Created: rec.Created.Value()}
	if len(e.Commit) > 12 {
		e.Commit = e.Commit[:12]
	}
	if !rec.Finished.Value().IsZero() && !rec.Started.Value().IsZero() {
		e.Duration = rec.Finished.Value().Sub(rec.Started.Value())
	}
	return e
}

// syncEntry rewrites the bpage entry of rec when its state or duration changed.
func (b *Builds) syncEntry(ctx context.Context, rec *store.BuildRecord) error {
	idx := b.buildPages(rec.Project.Value())
	want := entryOf(rec)
	page, err := idx.Page(ctx, idx.LastPage(rec.Number.Value()))
	if err == nil {
		if cur := page[(rec.Number.Value()-1)%uint64(idx.P)]; cur == want {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return idx.Set(ctx, rec.Number.Value()-1, want)
}

// Get reads a build record (GET, the source of truth).
func (b *Builds) Get(ctx context.Context, id string) (*store.BuildRecord, error) {
	if !validBuildID(id) {
		return nil, store.ErrNotFound
	}
	return store.Get(ctx, b.cfg.Control, &store.BuildRecord{ID: store.ID(id)})
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
	return store.CAS(ctx, b.cfg.Control, &store.BuildRecord{ID: store.ID(id)}, func(cur *store.BuildRecord) (*store.BuildRecord, error) {
		if cur == nil {
			return nil, store.ErrNotFound
		}
		// cur is a fresh read of this CAS attempt, so fn may change its slices and maps in
		// place; a field fn replaces with store.Set must not be read again by fn.
		next := *cur
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
		if Terminal(cur.State.Value()) {
			return ErrFinished
		}
		cur.State, cur.Finished, cur.SupersededBy = store.Set(state), store.Set(now), store.Set(sp)
		for i := range cur.Pipelines.Value() {
			if !Terminal(cur.Pipelines.Value()[i].State) {
				cur.Pipelines.Value()[i].State = state
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	b.ended(ctx, rec)
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
	for _, j := range rec.Jobs.Value() {
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
	switch old.State.Value() {
	case StateFailed, StateCancelled, StateSuperseded:
	default:
		return nil, fmt.Errorf("%w: only failed, cancelled or superseded builds can be retried (build is %s)", ErrState, old.State.Value())
	}
	r := again(old, TriggerRetry, by)
	r.RetryOf, r.Attempt = old.Number.Value(), max(old.Attempt.Value(), 1)+1
	if old.Evaluated.Value() && len(old.Pipelines.Value()) > 0 {
		r.Pipelines = nil
		for _, p := range old.Pipelines.Value() {
			if p.State != StateSucceeded {
				r.Pipelines = append(r.Pipelines, p.Name)
			}
		}
		if len(r.Pipelines) == 0 {
			r.Pipelines = pipelineNames(old.Pipelines.Value())
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
	if !Terminal(old.State.Value()) {
		return nil, fmt.Errorf("%w: only finished builds can be re-run (build is %s)", ErrState, old.State.Value())
	}
	r := again(old, TriggerRerun, by)
	r.RerunOf = old.Number.Value()
	return b.Create(ctx, r)
}

// again is a request repeating old: same source, ref, platforms, variants and selection.
func again(old *store.BuildRecord, kind, by string) Request {
	r := Request{Project: old.Project.Value(), Source: old.Source.Value(), Branch: old.Branch.Value(), Commit: old.Commit.Value(), Author: old.Author.Value(),
		Targets: old.Targets.Value(), Platforms: slices.Clone(old.Platforms.Value()), Variants: old.Variants.Value(), Test: old.Test.Value(),
		Version: old.Version.Value()}
	r.Trigger = store.BuildTrigger{Kind: kind, By: by, Ref: refOf(old.Trigger.Value(), old.Source.Value(), old.Branch.Value()),
		PR: old.Trigger.Value().PR, Fork: old.Trigger.Value().Fork}
	if old.Pipeline.Value() != "" {
		r.Pipelines = strings.Split(old.Pipeline.Value(), ",")
	} else if old.Evaluated.Value() && len(old.Targets.Value()) == 0 && len(old.Pipelines.Value()) > 0 {
		r.Pipelines = pipelineNames(old.Pipelines.Value())
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

// ended runs the Finished hook for a build that just became terminal.
func (b *Builds) ended(ctx context.Context, rec *store.BuildRecord) {
	if b.cfg.Finished == nil || rec == nil || !Terminal(rec.State.Value()) {
		return
	}
	if err := safeCall(func() { b.cfg.Finished(ctx, rec) }); err != nil {
		b.cfg.Log.Warn("build: finished hook", "build", rec.ID.Value(), "err", err.Error())
	}
}

func safeCall(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	fn()
	return nil
}
