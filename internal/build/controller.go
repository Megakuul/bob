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
	running, _, err := store.GetCached(ctx, b.cfg.Control, &b.running, &store.RunningBuilds{Name: "running"})
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
	for id, e := range running.Builds {
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
			b.cfg.Log.Warn("build controller: build not advanced", "build", rec.ID, "err", err.Error())
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
	case Terminal(rec.State):
		return b.settle(ctx, rec)
	case rec.State == StatePending:
		rec, err = b.plan(ctx, rec, recs)
	default:
		rec, err = b.progress(ctx, rec)
	}
	if rec != nil {
		recs[rec.ID] = rec
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
	ref := refOf(rec.Trigger, rec.Source, rec.Branch)
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
	for _, p := range older.Pipelines {
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
		ref := refOf(older.Trigger, older.Source, older.Branch)
		def := "main"
		if p, err := b.cfg.Projects.Project(ctx, older.Project); err == nil && p.DefaultBranch != "" {
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
			if o.ID == rec.ID || o.Project != rec.Project || o.Number >= rec.Number || Terminal(o.State) || lane(o) != l {
				continue
			}
			switch b.mode(ctx, o) {
			case ModeQueue:
				return rec, nil // waits in pending until the older build is terminal
			case ModeSupersede:
				stopped, err := b.stop(ctx, o.ID, StateSuperseded, rec.Number)
				if err != nil && !errors.Is(err, ErrFinished) {
					return rec, err
				}
				if stopped != nil {
					recs[o.ID] = stopped
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
	proj, err := b.cfg.Projects.Project(ctx, rec.Project)
	if err != nil {
		return b.fail(ctx, rec, "", fmt.Sprintf("project %s: %v", rec.Project, err))
	}
	var rows []store.BuildJob
	for i, pl := range rec.Platforms {
		class, err := b.cfg.Fleet.AddClass(ctx, store.Class{Platform: pl})
		if err != nil {
			return rec, err
		}
		rows = append(rows, store.BuildJob{ID: JobID(rec.ID, len(rec.Jobs)+i+1), Kind: KindEvalBuild, Class: class, State: orchestrator.StateQueued})
	}
	// the rows first (intent): a crash before an enqueue leaves a queued row without a job
	// record, which the next pass enqueues
	rec, err = b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
		if cur.State != StatePending {
			return store.ErrNoChange
		}
		cur.State, cur.Phase = StateQueued, PhaseEvaluating
		cur.Jobs = append(cur.Jobs, rows...)
		return nil
	})
	if err != nil || rec.State != StateQueued {
		return rec, err
	}
	for _, row := range rows {
		if err := b.enqueue(ctx, rec, row, targets, proj.Repo, 0); err != nil {
			return rec, err
		}
	}
	b.cfg.Log.Info("build planned", "build", rec.ID, "jobs", len(rows))
	return rec, nil
}

// enqueue writes the signed job of row (job record, then its slot) and wakes a worker.
func (b *Builds) enqueue(ctx context.Context, rec *store.BuildRecord, row store.BuildJob, targets []string, repo string, attempts int64) error {
	spec := store.JobSpec{V: 1, JobID: row.ID, BuildID: rec.ID, Project: rec.Project, Kind: row.Kind, Targets: targets,
		Class: row.Class, Repo: repo, Commit: rec.Commit, Staging: "staging/" + rec.ID + "/" + row.ID + "/",
		Created: b.now().Unix()}
	if spec.Commit == "" && rec.Source.Upload == "" {
		spec.Commit = rec.Source.Ref // resolved by the worker at checkout
	}
	raw, err := fleet.Encode(spec)
	if err != nil {
		return err
	}
	_, _, err = b.cfg.Fleet.Enqueue(ctx, row.Class, orchestrator.Job{ID: row.ID, Build: rec.ID, Spec: raw, Repo: repo, Attempts: attempts})
	if errors.Is(err, store.ErrExists) {
		return nil // the job record exists: an earlier enqueue got that far (the reconciler re-slots)
	}
	return err
}

// progress follows the jobs of a queued or running build and finishes it when they are done.
func (b *Builds) progress(ctx context.Context, rec *store.BuildRecord) (*store.BuildRecord, error) {
	var errs []error
	for _, row := range slices.Clone(rec.Jobs) {
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
		if Terminal(rec.State) {
			return rec, errors.Join(errs...)
		}
	}
	if len(errs) > 0 {
		return rec, errors.Join(errs...)
	}
	if slices.ContainsFunc(rec.Jobs, func(j store.BuildJob) bool { return !orchestrator.Terminal(j.State) }) {
		return rec, nil
	}
	return b.finish(ctx, rec)
}

// follow advances one non-terminal job row of a queued or running build.
func (b *Builds) follow(ctx context.Context, rec *store.BuildRecord, row store.BuildJob) (*store.BuildRecord, error) {
	job, err := store.Get(ctx, b.cfg.Jobs, &store.JobRecord{ID: row.ID})
	if errors.Is(err, store.ErrNotFound) {
		// never enqueued (crash between the row and the job record): enqueue it now
		proj, perr := b.cfg.Projects.Project(ctx, rec.Project)
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
	if !orchestrator.Terminal(job.State) {
		now := b.now()
		return b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
			changed := setRow(cur, rowOf(job, row))
			if job.State != orchestrator.StateQueued && cur.State == StateQueued {
				cur.State, cur.Started, changed = StateRunning, now, true
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
	row.State, row.Attempts, row.Epoch, row.Worker, row.Promotion = job.State, job.Attempt, job.Epoch, job.Owner, job.Promotion
	if !job.Claimed.IsZero() && !job.Changed.IsZero() && orchestrator.Terminal(job.State) {
		row.Duration = job.Changed.Sub(job.Claimed)
	}
	return row
}

// setRow replaces the row with row's id; it reports whether something changed.
func setRow(rec *store.BuildRecord, row store.BuildJob) bool {
	for i := range rec.Jobs {
		if rec.Jobs[i].ID == row.ID {
			if rec.Jobs[i] == row {
				return false
			}
			rec.Jobs[i] = row
			return true
		}
	}
	rec.Jobs = append(rec.Jobs, row)
	return true
}

var codeRE = regexp.MustCompile(`BOB-E-[0-9]{4}`)

// terminalJob takes the result of a finished job into the build and finalises the job.
func (b *Builds) terminalJob(ctx context.Context, rec *store.BuildRecord, row store.BuildJob, job *store.JobRecord) (*store.BuildRecord, error) {
	if job.State != orchestrator.StateCancelled && !fleet.VerifyResult(b.workerKey(job.Owner), job.ID, job.Epoch, job.Result, job.ResultSig) {
		// forged by another fleet writer: Finalise counts it and restores the owner's state
		_, err := b.queue.Finalise(ctx, job.ID)
		return nil, err
	}
	res, _ := fleet.DecodeResult(job.Result)
	spec, _ := fleet.DecodeSpec(job.Spec)
	var report *jobspec.Report
	var reject string
	if res.EvalSHA != "" && (row.Kind == KindEvalBuild || row.Kind == KindEval) {
		report, reject = b.readReport(ctx, jobspec.StagingPrefix(spec.Staging, job.Attempt), res.EvalSHA)
		if report != nil {
			if err := report.Verify(SelectionOf(rec)); err != nil {
				reject = jobspec.ErrMatchMismatch.Error()
				if !errors.Is(err, jobspec.ErrMatchMismatch) {
					reject += " (" + err.Error() + ")"
				}
				b.cfg.Log.Warn("security: evaluation result rejected", "build", rec.ID, "job", job.ID, "worker", job.Owner, "err", err.Error())
				report = nil
			}
		}
	}
	blocked := false
	if job.State == orchestrator.StateSucceeded && reject == "" && b.cfg.AwaitPromotion {
		switch {
		case job.Promotion == "":
			// the promotion controller has not finished this job yet: never finalise before it
			return b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
				r := rowOf(job, row)
				r.State = orchestrator.StateRunning // the row ends with the verdict
				changed := setRow(cur, r)
				if cur.State == StateQueued {
					cur.State, cur.Started, changed = StateRunning, b.now(), true
				}
				if cur.Phase != PhasePromoting {
					cur.Phase, changed = PhasePromoting, true
				}
				if !changed {
					return store.ErrNoChange
				}
				return nil
			})
		case strings.HasPrefix(job.Promotion, "blocked"):
			blocked = true // staging incomplete: run the job again (§6.6.4 step 1)
		case job.Promotion != "done":
			reject = "promotion: " + job.Promotion
		}
	}
	infraRetry := (job.State == orchestrator.StateFailed && res.Status == "infra" || blocked) && reject == ""
	retryRow := store.BuildJob{}
	if infraRetry && job.Attempt < int64(b.cfg.MaxAttempts) {
		retryRow = store.BuildJob{ID: JobID(rec.ID, len(rec.Jobs)+1), Kind: row.Kind, Class: row.Class,
			State: orchestrator.StateQueued, Attempts: job.Attempt}
	}
	now := b.now()
	next, err := b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
		setRow(cur, rowOf(job, row))
		if len(job.NonReproducible) > 0 {
			if cur.NonReproducible == nil {
				cur.NonReproducible = map[string][]string{}
			}
			cur.NonReproducible[job.ID] = slices.Clone(job.NonReproducible)
		}
		if cur.Started.IsZero() {
			cur.Started = now
		}
		if cur.State == StateQueued {
			cur.State = StateRunning
		}
		if report != nil {
			applyReport(cur, report)
		}
		switch {
		case reject != "":
			cur.Errors = addError(cur.Errors, store.BuildError{Message: reject})
		case retryRow.ID != "":
			if len(cur.Jobs) >= store.MaxBuildJobs {
				return fmt.Errorf("build %s: %d jobs, the limit", cur.ID, store.MaxBuildJobs)
			}
			if !slices.ContainsFunc(cur.Jobs, func(j store.BuildJob) bool { return j.ID == retryRow.ID }) {
				cur.Jobs = append(cur.Jobs, retryRow)
			}
		case blocked:
			cur.Errors = addError(cur.Errors, store.BuildError{Message: fmt.Sprintf(
				"build failed: job %s: %s after %d attempts", job.ID, job.Promotion, b.cfg.MaxAttempts)})
		case infraRetry:
			cur.Errors = addError(cur.Errors, store.BuildError{Message: fmt.Sprintf(
				"build failed: job %s failed %d times on workers (infrastructure)", job.ID, b.cfg.MaxAttempts)})
		case job.State == orchestrator.StateFailed && report == nil:
			e := store.BuildError{Message: firstLine(res.Error)}
			e.Code = codeRE.FindString(res.Error)
			if e.Message == "" {
				e.Message = "job " + job.ID + " failed"
			}
			cur.Errors = addError(cur.Errors, e)
			for _, l := range res.Failed {
				cur.Result.Failed = appendUnique(cur.Result.Failed, l)
			}
		}
		if cur.Phase == PhaseEvaluating && report != nil {
			cur.Phase = PhaseBuilding
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if report != nil {
		if next, err = b.writeNodes(ctx, next, job.ID, report); err != nil {
			return next, err
		}
	}
	if retryRow.ID != "" {
		proj, err := b.cfg.Projects.Project(ctx, rec.Project)
		if err != nil {
			return next, err
		}
		if err := b.enqueue(ctx, next, retryRow, spec.Targets, proj.Repo, retryRow.Attempts); err != nil {
			return next, err
		}
	}
	if _, err := b.queue.Finalise(ctx, job.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return next, err
	}
	if reject != "" && !Terminal(next.State) {
		return b.fail(ctx, next, job.ID, "")
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

// applyReport takes an accepted evaluation report into the build record.
func applyReport(rec *store.BuildRecord, r *jobspec.Report) {
	rec.Evaluated = true
	byName := map[string]jobspec.Pipeline{}
	for _, p := range r.Pipelines {
		byName[p.Name] = p
	}
	for _, name := range r.Matched {
		p := byName[name]
		if !slices.ContainsFunc(rec.Pipelines, func(x store.PipelineRun) bool { return x.Name == name }) {
			rec.Pipelines = append(rec.Pipelines, store.PipelineRun{Name: name, State: StateRunning,
				Concurrency: p.Concurrency, Releases: len(p.Releases) > 0})
		}
	}
	for _, p := range r.Packages {
		if p.Status == "failed" || p.Status == "fail" {
			rec.Result.Failed = appendUnique(rec.Result.Failed, p.Label)
		}
	}
	for _, e := range r.Errors {
		rec.Errors = addError(rec.Errors, store.BuildError{Code: e.Code, Message: e.Message, Label: e.Label, Hint: e.Hint})
	}
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
	next, err := b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
		if Terminal(cur.State) {
			moved = false
			return store.ErrNoChange
		}
		moved = true
		cur.State, cur.Finished = StateFailed, now
		if cur.Started.IsZero() {
			cur.Started = now
		}
		if msg != "" {
			cur.Errors = addError(cur.Errors, store.BuildError{Message: msg})
		}
		if len(cur.Errors) > 0 {
			cur.Result.Error = cur.Errors[0].Message
		}
		for i := range cur.Pipelines {
			if !Terminal(cur.Pipelines[i].State) {
				cur.Pipelines[i].State = StateFailed
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
	ok := len(rec.Errors) == 0 && len(rec.Result.Failed) == 0
	for _, j := range rec.Jobs {
		ok = ok && (j.State == orchestrator.StateSucceeded || j.State == orchestrator.StateFailed && hasRetry(rec, j))
	}
	if !ok {
		return b.fail(ctx, rec, "", "")
	}
	state := StateSucceeded
	if sel := SelectionOf(rec); sel.Event != "" && rec.Evaluated && len(rec.Pipelines) == 0 {
		state = StateSkipped // no pipeline matched the forge event (§6.9.4)
	}
	now := b.now()
	moved := false
	next, err := b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
		if Terminal(cur.State) {
			moved = false
			return store.ErrNoChange
		}
		moved = true
		cur.State, cur.Finished, cur.Phase = state, now, ""
		for i := range cur.Pipelines {
			cur.Pipelines[i].State = state
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
	for _, o := range rec.Jobs {
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
	for _, row := range rec.Jobs {
		if orchestrator.Terminal(row.State) {
			continue
		}
		job, err := store.Get(ctx, b.cfg.Jobs, &store.JobRecord{ID: row.ID})
		switch {
		case errors.Is(err, store.ErrNotFound):
			// never enqueued, or finalised by the reconciler before the row was copied
			_, err = b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
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
		if job.State == orchestrator.StateSucceeded && job.Promotion == "" && b.cfg.AwaitPromotion {
			open++ // promotion pending: never finalise before the verdict
			continue
		}
		if !orchestrator.Terminal(job.State) {
			open++
			if !job.Cancel {
				if err := b.queue.Cancel(ctx, job.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
				if b.cfg.Cancel != nil {
					_ = b.cfg.Cancel.CancelJob(ctx, job.ID)
				}
			}
			continue
		}
		if _, err := b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
			if !setRow(cur, rowOf(job, row)) {
				return store.ErrNoChange
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := b.queue.Finalise(ctx, job.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	fresh, err := b.Get(ctx, rec.ID)
	if err != nil {
		return err
	}
	if err := b.syncEntry(ctx, fresh); err != nil {
		return err
	}
	if fresh.State == StateFailed {
		if err := b.addFailed(ctx, fresh); err != nil {
			return err
		}
	}
	settled := b.report(ctx, fresh)
	if open > 0 || !settled && b.now().Sub(fresh.Finished) < statusWindow {
		return nil
	}
	return b.dropRunning(ctx, rec.ID)
}

// buildTime is the creation time in a build id.
func buildTime(id string) (time.Time, error) {
	if len(id) != 26 {
		return time.Time{}, fmt.Errorf("build id %q", id)
	}
	var ms uint64
	for i := range 10 {
		v := strings.IndexByte(crockford, id[i])
		if v < 0 {
			return time.Time{}, fmt.Errorf("build id %q", id)
		}
		ms = ms<<5 | uint64(v)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}
