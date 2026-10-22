package buildctl

import (
	"context"
	"errors"
	"fmt"
	"github.com/megakuul/bob/internal/build/jobspec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// Controller returns the leader-only build controller (SPEC §05.11 `build`, event + 5 s).
func (b *Builds) Controller() *orchestrator.Controller {
	return &orchestrator.Controller{ID: orchestrator.CtlBuild, Fleet: b.cfg.Jobs, Owner: b.cfg.Name,
		Interval: b.cfg.Interval, Skew: b.cfg.Skew, Now: b.cfg.Now, Wake: b.wake, Log: b.cfg.Log,
		Pass: func(ctx context.Context, _ *orchestrator.Run) error { return b.Pass(ctx) }}
}

// intentGrace is how long an index entry may point at a missing record (R-SRV-101).
const intentGrace = 5 * time.Minute

// Pass advances every build listed in bidx/running (every non-terminal build, plus terminal
// ones whose jobs or statuses are not settled yet). Builds are taken oldest first per project
// so the concurrency rule sees the older builds' new states.
func (b *Builds) Pass(ctx context.Context) error {
	running, _, err := store.GetCached(ctx, b.cfg.Control, &b.running, &store.RunningBuilds{Name: store.ID("running")})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	type item struct {
		id string
		e  store.RunningBuild
	}
	var items []item
	for id, e := range running.Builds.Value() {
		items = append(items, item{id, e})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].e.Project != items[j].e.Project {
			return items[i].e.Project < items[j].e.Project
		}
		return items[i].e.N < items[j].e.N
	})
	recs := map[string]*store.BuildRecord{}
	var errs []error
	for _, it := range items {
		rec, err := b.Get(ctx, it.id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// an entry written before a crash of Create: removed after the intent grace
			if t, terr := buildTime(it.id); terr == nil && b.now().Sub(t) > intentGrace {
				errs = append(errs, b.dropRunning(ctx, it.id))
			}
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		recs[it.id] = rec
	}
	for _, it := range items {
		rec := recs[it.id]
		if rec == nil {
			continue
		}
		if err := safe.Run(func() error { return b.advance(ctx, rec, recs) }); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.cfg.Log.Warn("build controller: build not advanced", "build", rec.ID.Value(), "err", err.Error())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// advance runs one step of the build state machine for rec. recs are the other active
// builds of this pass (the concurrency rule); advance updates recs[rec.ID].
func (b *Builds) advance(ctx context.Context, rec *store.BuildRecord, recs map[string]*store.BuildRecord) error {
	var err error
	switch {
	case Terminal(rec.State.Value()):
		return b.settle(ctx, rec)
	case rec.State.Value() == StatePending:
		rec, err = b.plan(ctx, rec, recs)
	default:
		rec, err = b.progress(ctx, rec)
	}
	if rec != nil {
		recs[rec.ID.Value()] = rec
		if serr := b.syncEntry(ctx, rec); serr != nil {
			err = errors.Join(err, serr)
		}
		b.report(ctx, rec)
	}
	return err
}

// Concurrency modes (R-PIPE-005).
const (
	ModeQueue     = "queue"
	ModeSupersede = "supersede"
	ModeParallel  = "parallel"
)

// lane is the concurrency lane of a build: its ref; every tag of a project shares one lane
// (decision D-IB11-5); builds without a ref (uploads) have none.
func lane(rec *store.BuildRecord) string {
	ref := refOf(rec.Trigger.Value(), rec.Source.Value(), rec.Branch.Value())
	if strings.HasPrefix(ref, "refs/tags/") {
		return "refs/tags/*"
	}
	return ref
}

// mode is the concurrency rule between an older active build and a newer one in its lane:
// the pipelines' own setting when the older build was evaluated, else the per-ref default
// (queue for the default branch and tags, supersede otherwise). A build that runs a release
// pipeline is never superseded.
func (b *Builds) mode(ctx context.Context, older *store.BuildRecord) string {
	release, explicit := false, map[string]bool{}
	for _, p := range older.Pipelines.Value() {
		release = release || p.Releases
		if p.Concurrency != "" {
			explicit[p.Concurrency] = true
		}
	}
	m := ""
	switch {
	case explicit[ModeQueue]:
		m = ModeQueue
	case explicit[ModeSupersede]:
		m = ModeSupersede
	case explicit[ModeParallel]:
		m = ModeParallel
	default:
		m = ModeSupersede
		ref := refOf(older.Trigger.Value(), older.Source.Value(), older.Branch.Value())
		def := "main"
		if p, err := b.cfg.Projects.Project(ctx, older.Project.Value()); err == nil && p.DefaultBranch != "" {
			def = p.DefaultBranch
		}
		if strings.HasPrefix(ref, "refs/tags/") || ref == "refs/heads/"+def {
			m = ModeQueue
		}
	}
	if release && m == ModeSupersede {
		m = ModeQueue
	}
	return m
}

// plan applies the concurrency rule to a pending build and, when it may run, enqueues its
// first jobs: one evalbuild closure job per platform (R-FLEET-005 default; decision D-IB11-2).
func (b *Builds) plan(ctx context.Context, rec *store.BuildRecord, recs map[string]*store.BuildRecord) (*store.BuildRecord, error) {
	if l := lane(rec); l != "" {
		for _, o := range recs {
			if o.ID.Value() == rec.ID.Value() || o.Project.Value() != rec.Project.Value() || o.Number.Value() >= rec.Number.Value() || Terminal(o.State.Value()) || lane(o) != l {
				continue
			}
			switch b.mode(ctx, o) {
			case ModeQueue:
				return rec, nil // waits in pending until the older build is terminal
			case ModeSupersede:
				stopped, err := b.stop(ctx, o.ID.Value(), StateSuperseded, rec.Number.Value())
				if err != nil && !errors.Is(err, ErrFinished) {
					return rec, err
				}
				if stopped != nil {
					recs[o.ID.Value()] = stopped
					b.report(ctx, stopped)
				}
			}
		}
	}
	sel := SelectionOf(rec)
	targets, err := sel.Encode()
	if err != nil {
		return b.fail(ctx, rec, "", err.Error())
	}
	proj, err := b.cfg.Projects.Project(ctx, rec.Project.Value())
	if err != nil {
		return b.fail(ctx, rec, "", fmt.Sprintf("project %s: %v", rec.Project.Value(), err))
	}
	var rows []store.BuildJob
	for i, pl := range rec.Platforms.Value() {
		class, err := b.cfg.Fleet.AddClass(ctx, store.Class{Platform: pl})
		if err != nil {
			return rec, err
		}
		rows = append(rows, store.BuildJob{ID: JobID(rec.ID.Value(), len(rec.Jobs.Value())+i+1), Kind: KindEvalBuild, Class: class, State: orchestrator.StateQueued})
	}
	// the rows first (intent): a crash before an enqueue leaves a queued row without a job
	// record, which the next pass enqueues
	rec, err = b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
		if cur.State.Value() != StatePending {
			return store.ErrNoChange
		}
		cur.State, cur.Phase = store.Set[string](StateQueued), store.Set[string](PhaseEvaluating)
		cur.Jobs = store.Set(append(cur.Jobs.Value(), rows...))
		return nil
	})
	if err != nil || rec.State.Value() != StateQueued {
		return rec, err
	}
	for _, row := range rows {
		if err := b.enqueue(ctx, rec, row, targets, proj.Repo, 0); err != nil {
			return rec, err
		}
	}
	b.cfg.Log.Info("build planned", "build", rec.ID.Value(), "jobs", len(rows))
	return rec, nil
}

// enqueue writes the signed job of row (job record, then its slot) and wakes a worker.
func (b *Builds) enqueue(ctx context.Context, rec *store.BuildRecord, row store.BuildJob, targets []string, repo string, attempts int64) error {
	spec := store.JobSpec{V: 1, JobID: row.ID, BuildID: rec.ID.Value(), Project: rec.Project.Value(), Kind: row.Kind, Targets: targets,
		Class: row.Class, Repo: repo, Commit: rec.Commit.Value(), Staging: "staging/" + rec.ID.Value() + "/" + row.ID + "/",
		Created: b.now().Unix()}
	if spec.Commit == "" && rec.Source.Value().Upload == "" {
		spec.Commit = rec.Source.Value().Ref // resolved by the worker at checkout
	}
	raw, err := fleet.Encode(spec)
	if err != nil {
		return err
	}
	_, _, err = b.cfg.Fleet.Enqueue(ctx, row.Class, orchestrator.Job{ID: row.ID, Build: rec.ID.Value(), Spec: raw, Repo: repo, Attempts: attempts})
	if errors.Is(err, store.ErrExists) {
		return nil // the job record exists: an earlier enqueue got that far (the reconciler re-slots)
	}
	return err
}

// progress follows the jobs of a queued or running build and finishes it when they are done.
func (b *Builds) progress(ctx context.Context, rec *store.BuildRecord) (*store.BuildRecord, error) {
	var errs []error
	for _, row := range slices.Clone(rec.Jobs.Value()) {
		if orchestrator.Terminal(row.State) {
			continue
		}
		next, err := b.follow(ctx, rec, row)
		if next != nil {
			rec = next
		}
		if err != nil {
			errs = append(errs, err)
		}
		if Terminal(rec.State.Value()) {
			return rec, errors.Join(errs...)
		}
	}
	if len(errs) > 0 {
		return rec, errors.Join(errs...)
	}
	if slices.ContainsFunc(rec.Jobs.Value(), func(j store.BuildJob) bool { return !orchestrator.Terminal(j.State) }) {
		return rec, nil
	}
	return b.finish(ctx, rec)
}

// follow advances one non-terminal job row of a queued or running build.
func (b *Builds) follow(ctx context.Context, rec *store.BuildRecord, row store.BuildJob) (*store.BuildRecord, error) {
	job, err := store.Get(ctx, b.cfg.Jobs, &store.JobRecord{ID: store.ID(row.ID)})
	if errors.Is(err, store.ErrNotFound) {
		// never enqueued (crash between the row and the job record): enqueue it now
		proj, perr := b.cfg.Projects.Project(ctx, rec.Project.Value())
		if perr != nil {
			return nil, perr
		}
		targets, terr := SelectionOf(rec).Encode()
		if terr != nil {
			return nil, terr
		}
		return nil, b.enqueue(ctx, rec, row, targets, proj.Repo, row.Attempts)
	}
	if err != nil {
		return nil, err
	}
	if !orchestrator.Terminal(job.State.Value()) {
		now := b.now()
		return b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
			changed := setRow(cur, rowOf(job, row))
			if job.State.Value() != orchestrator.StateQueued && cur.State.Value() == StateQueued {
				cur.State, cur.Started, changed = store.Set[string](StateRunning), store.Set(now), true
			}
			if !changed {
				return store.ErrNoChange
			}
			return nil
		})
	}
	return b.terminalJob(ctx, rec, row, job)
}

// rowOf is the jb row of a job record.
func rowOf(job *store.JobRecord, row store.BuildJob) store.BuildJob {
	row.State, row.Attempts, row.Epoch, row.Worker, row.Promotion = job.State.Value(), job.Attempt.Value(), job.Epoch.Value(), job.Owner.Value(), job.Promotion.Value()
	if !job.Claimed.Value().IsZero() && !job.Changed.Value().IsZero() && orchestrator.Terminal(job.State.Value()) {
		row.Duration = job.Changed.Value().Sub(job.Claimed.Value())
	}
	return row
}

// setRow replaces the row with row's id; it reports whether something changed.
func setRow(rec *store.BuildRecord, row store.BuildJob) bool {
	jobs, changed := withRow(rec.Jobs.Value(), row)
	if changed {
		rec.Jobs = store.Set(jobs)
	}
	return changed
}

// withRow returns jobs with row set (replaced by id, or appended) and whether that changed jobs.
func withRow(jobs []store.BuildJob, row store.BuildJob) ([]store.BuildJob, bool) {
	for i := range jobs {
		if jobs[i].ID == row.ID {
			if jobs[i] == row {
				return jobs, false
			}
			jobs[i] = row
			return jobs, true
		}
	}
	return append(jobs, row), true
}

var codeRE = regexp.MustCompile(`BOB-E-[0-9]{4}`)

// terminalJob takes the result of a finished job into the build and finalises the job.
func (b *Builds) terminalJob(ctx context.Context, rec *store.BuildRecord, row store.BuildJob, job *store.JobRecord) (*store.BuildRecord, error) {
	if job.State.Value() != orchestrator.StateCancelled && !fleet.VerifyResult(b.workerKey(job.Owner.Value()), job.ID.Value(), job.Epoch.Value(), job.Result.Value(), job.ResultSig.Value()) {
		// forged by another fleet writer: Finalise counts it and restores the owner's state
		_, err := b.queue.Finalise(ctx, job.ID.Value())
		return nil, err
	}
	res, _ := fleet.DecodeResult(job.Result.Value())
	spec, _ := fleet.DecodeSpec(job.Spec.Value())
	var report *jobspec.Report
	var reject string
	if res.EvalSHA != "" && (row.Kind == KindEvalBuild || row.Kind == KindEval) {
		report, reject = b.readReport(ctx, jobspec.StagingPrefix(spec.Staging, job.Attempt.Value()), res.EvalSHA)
		if report != nil {
			if err := report.Verify(SelectionOf(rec)); err != nil {
				reject = jobspec.ErrMatchMismatch.Error()
				if !errors.Is(err, jobspec.ErrMatchMismatch) {
					reject += " (" + err.Error() + ")"
				}
				b.cfg.Log.Warn("security: evaluation result rejected", "build", rec.ID.Value(), "job", job.ID.Value(), "worker", job.Owner.Value(), "err", err.Error())
				report = nil
			}
		}
	}
	blocked := false
	if job.State.Value() == orchestrator.StateSucceeded && reject == "" && b.cfg.AwaitPromotion {
		switch {
		case job.Promotion.Value() == "":
			// the promotion controller has not finished this job yet: never finalise before it
			return b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
				r := rowOf(job, row)
				r.State = orchestrator.StateRunning // the row ends with the verdict
				changed := setRow(cur, r)
				if cur.State.Value() == StateQueued {
					cur.State, cur.Started, changed = store.Set[string](StateRunning), store.Set(b.now()), true
				}
				if cur.Phase.Value() != PhasePromoting {
					cur.Phase, changed = store.Set[string](PhasePromoting), true
				}
				if !changed {
					return store.ErrNoChange
				}
				return nil
			})
		case strings.HasPrefix(job.Promotion.Value(), "blocked"):
			blocked = true // staging incomplete: run the job again (§6.6.4 step 1)
		case job.Promotion.Value() != "done":
			reject = "promotion: " + job.Promotion.Value()
		}
	}
	infraRetry := (job.State.Value() == orchestrator.StateFailed && res.Status == "infra" || blocked) && reject == ""
	retryRow := store.BuildJob{}
	if infraRetry && job.Attempt.Value() < int64(b.cfg.MaxAttempts) {
		retryRow = store.BuildJob{ID: JobID(rec.ID.Value(), len(rec.Jobs.Value())+1), Kind: row.Kind, Class: row.Class,
			State: orchestrator.StateQueued, Attempts: job.Attempt.Value()}
	}
	now := b.now()
	next, err := b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
		// The fields this update changes in several steps are edited as plain values and
		// written once at the end (a field set with store.Set cannot be read back).
		jobs, _ := withRow(cur.Jobs.Value(), rowOf(job, row))
		errs, result, pipes, evaluated := cur.Errors.Value(), cur.Result.Value(), cur.Pipelines.Value(), cur.Evaluated.Value()
		if len(job.NonReproducible.Value()) > 0 {
			nr := cur.NonReproducible.Value()
			if nr == nil {
				nr = map[string][]string{}
			}
			nr[job.ID.Value()] = slices.Clone(job.NonReproducible.Value())
			cur.NonReproducible = store.Set(nr)
		}
		if cur.Started.Value().IsZero() {
			cur.Started = store.Set(now)
		}
		if cur.State.Value() == StateQueued {
			cur.State = store.Set[string](StateRunning)
		}
		if report != nil {
			pipes, result, errs = applyReport(pipes, result, errs, report)
			evaluated = true
		}
		switch {
		case reject != "":
			errs = addError(errs, store.BuildError{Message: reject})
		case retryRow.ID != "":
			if len(jobs) >= store.MaxBuildJobs {
				return fmt.Errorf("build %s: %d jobs, the limit", cur.ID.Value(), store.MaxBuildJobs)
			}
			if !slices.ContainsFunc(jobs, func(j store.BuildJob) bool { return j.ID == retryRow.ID }) {
				jobs = append(jobs, retryRow)
			}
		case blocked:
			errs = addError(errs, store.BuildError{Message: fmt.Sprintf(
				"build failed: job %s: %s after %d attempts", job.ID.Value(), job.Promotion.Value(), b.cfg.MaxAttempts)})
		case infraRetry:
			errs = addError(errs, store.BuildError{Message: fmt.Sprintf(
				"build failed: job %s failed %d times on workers (infrastructure)", job.ID.Value(), b.cfg.MaxAttempts)})
		case job.State.Value() == orchestrator.StateFailed && report == nil:
			e := store.BuildError{Message: firstLine(res.Error)}
			e.Code = codeRE.FindString(res.Error)
			if e.Message == "" {
				e.Message = "job " + job.ID.Value() + " failed"
			}
			errs = addError(errs, e)
			for _, l := range res.Failed {
				result.Failed = appendUnique(result.Failed, l)
			}
		}
		if cur.Phase.Value() == PhaseEvaluating && report != nil {
			cur.Phase = store.Set[string](PhaseBuilding)
		}
		cur.Jobs, cur.Errors, cur.Result = store.Set(jobs), store.Set(errs), store.Set(result)
		cur.Pipelines, cur.Evaluated = store.Set(pipes), store.Set(evaluated)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if report != nil {
		if b.cfg.Evaluated != nil {
			b.cfg.Evaluated(ctx, next, report)
		}
		if next, err = b.writeNodes(ctx, next, job.ID.Value(), report); err != nil {
			return next, err
		}
	}
	if retryRow.ID != "" {
		proj, err := b.cfg.Projects.Project(ctx, rec.Project.Value())
		if err != nil {
			return next, err
		}
		if err := b.enqueue(ctx, next, retryRow, spec.Targets, proj.Repo, retryRow.Attempts); err != nil {
			return next, err
		}
	}
	if _, err := b.queue.Finalise(ctx, job.ID.Value()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return next, err
	}
	if reject != "" && !Terminal(next.State.Value()) {
		return b.fail(ctx, next, job.ID.Value(), "")
	}
	return next, nil
}

// readReport reads and verifies the evaluation report of one job attempt; reject explains
// why none was accepted.
func (b *Builds) readReport(ctx context.Context, staging, sha string) (*jobspec.Report, string) {
	if b.cfg.Artifacts == nil {
		return nil, "evaluation report unreadable: no artifacts bucket"
	}
	body, _, err := b.cfg.Artifacts.Read(ctx, jobspec.ReportKey(staging), jobspec.MaxReport)
	if err != nil {
		return nil, "evaluation report unreadable: " + err.Error()
	}
	r, err := jobspec.ParseReport(body, sha)
	if err != nil {
		return nil, "evaluation result rejected: " + err.Error()
	}
	return r, ""
}

// applyReport takes an accepted evaluation report into the pipelines, result and errors of a
// build record (the record is then marked evaluated).
func applyReport(pipes []store.PipelineRun, result store.BuildResult, errs []store.BuildError, r *jobspec.Report) ([]store.PipelineRun, store.BuildResult, []store.BuildError) {
	byName := map[string]jobspec.Pipeline{}
	for _, p := range r.Pipelines {
		byName[p.Name] = p
	}
	for _, name := range r.Matched {
		p := byName[name]
		if !slices.ContainsFunc(pipes, func(x store.PipelineRun) bool { return x.Name == name }) {
			pipes = append(pipes, store.PipelineRun{Name: name, State: StateRunning,
				Concurrency: p.Concurrency, Releases: len(p.Releases) > 0})
		}
	}
	for _, p := range r.Packages {
		if p.Status == "failed" || p.Status == "fail" {
			result.Failed = appendUnique(result.Failed, p.Label)
		}
	}
	for _, e := range r.Errors {
		errs = addError(errs, store.BuildError{Code: e.Code, Message: e.Message, Label: e.Label, Hint: e.Hint})
	}
	return pipes, result, errs
}

func addError(es []store.BuildError, e store.BuildError) []store.BuildError {
	if len(es) >= maxErrors || slices.Contains(es, e) {
		return es
	}
	return append(es, e)
}

func appendUnique(l []string, s string) []string {
	if slices.Contains(l, s) || len(l) >= 1024 {
		return l
	}
	return append(l, s)
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "✗"))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 1024 {
		s = s[:1024]
	}
	return strings.TrimSpace(s)
}

// fail ends a non-terminal build as failed with an optional error message, cancelling the
// jobs that are still queued (running jobs finish; settle finalises them).
func (b *Builds) fail(ctx context.Context, rec *store.BuildRecord, _ string, msg string) (*store.BuildRecord, error) {
	now := b.now()
	moved := false
	next, err := b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
		if Terminal(cur.State.Value()) {
			moved = false
			return store.ErrNoChange
		}
		moved = true
		cur.State, cur.Finished = store.Set[string](StateFailed), store.Set(now)
		if cur.Started.Value().IsZero() {
			cur.Started = store.Set(now)
		}
		errs := cur.Errors.Value()
		if msg != "" {
			errs = addError(errs, store.BuildError{Message: msg})
			cur.Errors = store.Set(errs)
		}
		if len(errs) > 0 {
			result := cur.Result.Value()
			result.Error = errs[0].Message
			cur.Result = store.Set(result)
		}
		for i := range cur.Pipelines.Value() {
			if !Terminal(cur.Pipelines.Value()[i].State) {
				cur.Pipelines.Value()[i].State = StateFailed
			}
		}
		return nil
	})
	if err != nil {
		return rec, err
	}
	if moved {
		b.ended(ctx, next)
	}
	b.cancelJobs(ctx, next)
	if err := b.addFailed(ctx, next); err != nil {
		return next, err
	}
	return next, nil
}

// finish ends a build whose jobs are all terminal: succeeded, failed or skipped.
func (b *Builds) finish(ctx context.Context, rec *store.BuildRecord) (*store.BuildRecord, error) {
	ok := len(rec.Errors.Value()) == 0 && len(rec.Result.Value().Failed) == 0
	for _, j := range rec.Jobs.Value() {
		ok = ok && (j.State == orchestrator.StateSucceeded || j.State == orchestrator.StateFailed && hasRetry(rec, j))
	}
	if !ok {
		return b.fail(ctx, rec, "", "")
	}
	state := StateSucceeded
	if sel := SelectionOf(rec); sel.Event != "" && rec.Evaluated.Value() && len(rec.Pipelines.Value()) == 0 {
		state = StateSkipped // no pipeline matched the forge event (§6.9.4)
	}
	now := b.now()
	moved := false
	next, err := b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
		if Terminal(cur.State.Value()) {
			moved = false
			return store.ErrNoChange
		}
		moved = true
		cur.State, cur.Finished, cur.Phase = store.Set(state), store.Set(now), store.Set[string]("")
		for i := range cur.Pipelines.Value() {
			cur.Pipelines.Value()[i].State = state
		}
		return nil
	})
	if err == nil && moved {
		b.ended(ctx, next)
	}
	return next, err
}

// hasRetry reports whether a failed infra job row was replaced by a later attempt.
func hasRetry(rec *store.BuildRecord, j store.BuildJob) bool {
	for _, o := range rec.Jobs.Value() {
		if o.ID > j.ID && o.Kind == j.Kind && o.Class == j.Class && o.Attempts >= j.Attempts && o.Attempts > 0 {
			return true
		}
	}
	return false
}

// settle cleans up after a terminal build: cancels and finalises its remaining jobs, waits for
// the statuses, then removes it from bidx/running.
func (b *Builds) settle(ctx context.Context, rec *store.BuildRecord) error {
	open := 0
	for _, row := range rec.Jobs.Value() {
		if orchestrator.Terminal(row.State) {
			continue
		}
		job, err := store.Get(ctx, b.cfg.Jobs, &store.JobRecord{ID: store.ID(row.ID)})
		switch {
		case errors.Is(err, store.ErrNotFound):
			// never enqueued, or finalised by the reconciler before the row was copied
			_, err = b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
				r := row
				r.State = orchestrator.StateCancelled
				if !setRow(cur, r) {
					return store.ErrNoChange
				}
				return nil
			})
			if err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		}
		if job.State.Value() == orchestrator.StateSucceeded && job.Promotion.Value() == "" && b.cfg.AwaitPromotion {
			open++ // promotion pending: never finalise before the verdict
			continue
		}
		if !orchestrator.Terminal(job.State.Value()) {
			open++
			if !job.Cancel.Value() {
				if err := b.queue.Cancel(ctx, job.ID.Value()); err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
				if b.cfg.Cancel != nil {
					_ = b.cfg.Cancel.CancelJob(ctx, job.ID.Value())
				}
			}
			continue
		}
		if _, err := b.update(ctx, rec.ID.Value(), func(cur *store.BuildRecord) error {
			if !setRow(cur, rowOf(job, row)) {
				return store.ErrNoChange
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := b.queue.Finalise(ctx, job.ID.Value()); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	fresh, err := b.Get(ctx, rec.ID.Value())
	if err != nil {
		return err
	}
	if err := b.syncEntry(ctx, fresh); err != nil {
		return err
	}
	if fresh.State.Value() == StateFailed {
		if err := b.addFailed(ctx, fresh); err != nil {
			return err
		}
	}
	settled := b.report(ctx, fresh)
	if open > 0 || !settled && b.now().Sub(fresh.Finished.Value()) < statusWindow {
		return nil
	}
	return b.dropRunning(ctx, rec.ID.Value())
}
