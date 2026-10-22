package promote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/store"
)

// Promotion verdicts (JobRecord.pm, SPEC §6.6.4).
const (
	Pending     = ""
	Done        = "done"
	Blocked     = "blocked: staging incomplete"
	Failed      = "failed: artifact verification"
	jobSucceded = "succeeded"
)

// Limits of SPEC §6.6.4.
const (
	MaxComplete   = 4 << 20
	MaxPaths      = 100_000
	levelParallel = 4
	// unknownRefGrace is how long a claimed reference that is neither in the job, nor skipped,
	// nor served may stay unknown (another job's promotion may still be running) before the
	// job fails verification.
	unknownRefGrace = time.Hour
	// staleNAR: a served-prefix NAR without narinfo that is older than this is an orphan of a
	// crashed promotion and may be replaced.
	staleNAR = time.Hour
	lockTTL  = time.Minute
	metaHash = "bob-narhash"
)

// Complete is COMPLETE.json, the last object a worker stages (§6.6.4).
type Complete struct {
	V       int      `json:"v"`
	Job     string   `json:"job"`
	Build   string   `json:"build"`
	Project string   `json:"project"`
	Paths   []string `json:"paths"`
	Images  []Image  `json:"images"`
	Assets  []Asset  `json:"assets"`
	Skipped []string `json:"skipped"`
}

// Image is one staged OCI image.
type Image struct {
	Repo      string   `json:"repo"`
	Tag       string   `json:"tag"`
	Manifest  string   `json:"manifest"`
	MediaType string   `json:"mediaType"`
	Blobs     []string `json:"blobs"`
}

// Asset is one staged release asset.
type Asset struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var (
	digestRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	assetRE   = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,255}$`)
	hexRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	attemptRE = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// ParseComplete reads and validates COMPLETE.json of job/build/project (hostile input).
func ParseComplete(b []byte, job, build, project string) (*Complete, error) {
	if len(b) > MaxComplete {
		return nil, fmt.Errorf("COMPLETE.json larger than %d bytes", MaxComplete)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Complete
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("COMPLETE.json: %w", err)
	}
	switch {
	case c.V != 1:
		return nil, fmt.Errorf("COMPLETE.json: version %d", c.V)
	case c.Job != job || c.Build != build || c.Project != project:
		return nil, fmt.Errorf("COMPLETE.json names job %q build %q project %q, not this job", c.Job, c.Build, c.Project)
	case len(c.Paths) > MaxPaths || len(c.Skipped) > MaxPaths:
		return nil, fmt.Errorf("COMPLETE.json lists more than %d paths", MaxPaths)
	case len(c.Images) > 1024 || len(c.Assets) > 1024:
		return nil, errors.New("COMPLETE.json lists too many images or assets")
	}
	seen := map[string]bool{}
	for _, p := range c.Paths {
		if !cache.ValidStorePath(p) || seen[cache.HashOf(p)] {
			return nil, fmt.Errorf("COMPLETE.json: invalid or duplicate path %.300q", p)
		}
		seen[cache.HashOf(p)] = true
	}
	for _, p := range c.Skipped {
		if !cache.ValidStorePath(p) {
			return nil, fmt.Errorf("COMPLETE.json: invalid skipped path %.300q", p)
		}
	}
	for _, im := range c.Images {
		if !digestRE.MatchString(im.Manifest) || len(im.Blobs) > 4096 {
			return nil, fmt.Errorf("COMPLETE.json: invalid image %.200q", im.Repo)
		}
		for _, d := range im.Blobs {
			if !digestRE.MatchString(d) {
				return nil, fmt.Errorf("COMPLETE.json: invalid digest %.100q", d)
			}
		}
	}
	for _, a := range c.Assets {
		if !assetRE.MatchString(a.File) || a.File == "." || a.File == ".." || !hexRE.MatchString(a.SHA256) || a.Size < 0 {
			return nil, fmt.Errorf("COMPLETE.json: invalid asset %.300q", a.File)
		}
	}
	return &c, nil
}

// Config configures a Promoter.
type Config struct {
	Name           string // this orchestrator's id (logs, job locks)
	Control, Fleet *store.Bucket
	Objects        *cache.Objects
	Vis            *cache.VisStore // cache.NixVisPrefix
	Keys           *Keys
	JobShards      int // fleet.job_shards (jact)
	Now            func() time.Time
	Log            *slog.Logger
	// Images promotes the OCI part of a job (§6.6.4 step 4, the registry milestone). nil
	// leaves staged images unpromoted (logged).
	Images func(ctx context.Context, j *Job) error
}

// Promoter promotes the staging of succeeded jobs into the binary cache (SPEC §6.6.4,
// R-REG-002, R-REG-152, R-REG-005). Every step is idempotent and every write conditional, so
// any number of orchestrators may promote the same job at once, and a crash at any point
// leaves only orphans without narinfo.
type Promoter struct {
	c Config
}

// New returns a Promoter.
func New(c Config) (*Promoter, error) {
	if c.Control == nil || c.Fleet == nil || c.Objects == nil || c.Vis == nil || c.Keys == nil || c.Name == "" {
		return nil, errors.New("promote: control, fleet, artifacts, visibility, keys and a name are required")
	}
	if c.JobShards <= 0 {
		c.JobShards = 16
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Promoter{c: c}, nil
}

// Job is one job being promoted.
type Job struct {
	ID, Build, Project, Worker string
	Staging                    string // staging/<build>/<job>/<attempt>/
	Complete                   *Complete
}

// Run is one promotion pass (the promotion controller calls it every 10 s): every active
// job that succeeded and is not promoted yet. Jobs are found through jact (no LIST).
func (p *Promoter) Run(ctx context.Context) error {
	active, err := store.ActiveJobs(p.c.Fleet, p.c.JobShards).All(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var errs []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.Promote(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("job %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Promote promotes job id if it succeeded and is not promoted yet (also the event path:
// called as soon as a job's result lands). An error means "try again later".
func (p *Promoter) Promote(ctx context.Context, id string) error {
	if _, err := store.Segment(id); err != nil {
		return nil
	}
	rec, err := store.Get(ctx, p.c.Fleet, &store.JobRecord{ID: store.ID(id)})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.State.Value() != jobSucceded || rec.Promotion.Value() != Pending {
		return nil
	}
	return p.promote(ctx, rec)
}

func (p *Promoter) workerKey(ctx context.Context, wid string) ed25519.PublicKey {
	if _, err := store.Segment(wid); err != nil {
		return nil
	}
	w, err := store.Get(ctx, p.c.Fleet, &store.WorkerRecord{Wid: store.ID(wid)})
	if err != nil || len(w.SigPub.Value()) != ed25519.PublicKeySize {
		return nil
	}
	return w.SigPub.Value()
}

func (p *Promoter) specKeys(ctx context.Context) []ed25519.PublicKey {
	fc, err := store.Get(ctx, p.c.Fleet, store.FleetConfigKey())
	if err != nil {
		return nil
	}
	var out []ed25519.PublicKey
	for _, k := range fc.Keys.Value() {
		out = append(out, k.Pub)
	}
	return out
}

// job checks the record's signatures (a worker can overwrite job records, ADR 0013 T10) and
// the staging key of its result. ok is false for a record that is not (yet) trustworthy.
func (p *Promoter) job(ctx context.Context, rec *store.JobRecord, log *slog.Logger) (j *Job, compSHA, bad string, ok bool) {
	spec, ok := fleet.DecodeSpec(rec.Spec.Value())
	if !ok || !fleet.VerifySpec(p.specKeys(ctx), rec.Spec.Value(), rec.SpecSig.Value()) {
		log.Warn("security: job spec not signed by an orchestrator; not promoted")
		return nil, "", "", false
	}
	if !fleet.VerifyResult(p.workerKey(ctx, rec.Owner.Value()), rec.ID.Value(), rec.Epoch.Value(), rec.Result.Value(), rec.ResultSig.Value()) {
		log.Warn("security: job result not signed by its owner; not promoted")
		return nil, "", "", false
	}
	res, ok := fleet.DecodeResult(rec.Result.Value())
	if !ok || res.Status != "ok" {
		return nil, "", "", false
	}
	parts := strings.Split(res.Complete, "/")
	if len(parts) != 5 || parts[0] != "staging" || parts[1] != spec.BuildID || parts[2] != rec.ID.Value() ||
		!segmentRE.MatchString(parts[1]) || !attemptRE.MatchString(parts[3]) || parts[4] != "COMPLETE.json" {
		return nil, "", fmt.Sprintf("result names %.200q, not a staging key of this job", res.Complete), true
	}
	return &Job{ID: rec.ID.Value(), Build: spec.BuildID, Project: spec.Project, Worker: rec.Owner.Value(),
		Staging: strings.Join(parts[:4], "/") + "/"}, res.CompSHA, "", true
}

// lock is the per-job promotion lock: it keeps two orchestrators from doing the same work at
// once. Correctness never depends on it (every write is conditional).
func (p *Promoter) lock(jobID string) *store.Lease {
	return store.NewLease(p.c.Control, func(s store.LeaseState) *promoLock {
		l := &promoLock{Job: store.ID(jobID), Owner: store.Set(s.Owner), Epoch: store.Set(s.Epoch), Expires: store.Set(s.Expires)}
		if len(s.Cursor) > 0 {
			l.Cursor = store.Set(s.Cursor)
		}
		return l
	}, func(r *promoLock) store.LeaseState {
		return store.LeaseState{Owner: r.Owner.Value(), Epoch: r.Epoch.Value(), Expires: r.Expires.Value(), Cursor: r.Cursor.Value()}
	}, p.c.Name, lockTTL)
}

// promoLock is the lease record plock/<jobID> of the promotion lock.
type promoLock struct {
	Job     store.KeyField             `pk:"plock" cbor:"-"`
	ETag    store.ETagField            `etag:"true" cbor:"-"`
	Owner   store.DataField[string]    `cbor:"o,omitempty"`
	Epoch   store.DataField[uint64]    `cbor:"ep,omitempty"`
	Expires store.DataField[time.Time] `cbor:"ex,omitempty"`
	Cursor  store.DataField[[]byte]    `cbor:"cu,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *promoLock) Draft() *promoLock {
	return &promoLock{Job: r.Job, ETag: r.ETag, Owner: store.Carry(r.Owner), Epoch: store.Carry(r.Epoch),
		Expires: store.Carry(r.Expires), Cursor: store.Carry(r.Cursor)}
}

// acquire waits until this orchestrator holds the job lock, renewing it in the background
// until stop is called.
func (p *Promoter) acquire(ctx context.Context, l *store.Lease) (stop func(), err error) {
	for {
		held, _, err := l.TryAcquire(ctx)
		if err != nil {
			return nil, err
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	rctx, cancel := context.WithCancel(ctx)
	done := safe.Go(func() error {
		for {
			select {
			case <-rctx.Done():
				return nil
			case <-time.After(lockTTL / 3):
				_, _, _ = l.TryAcquire(rctx)
			}
		}
	})
	return func() { cancel(); <-done }, nil
}

type stagedPath struct {
	path, hash string
	claim      *cache.NarInfo
	deps       []string // hashes of in-job references
	level      int
}

// outcome is what a promotion attempt of one job did.
type outcome struct {
	mu          sync.Mutex
	promoted    int
	already     int
	nonRepro    []string
	failed      error // a verification failure (verdict Failed)
	unknownRefs []string
}

func (o *outcome) add(f func(*outcome)) {
	o.mu.Lock()
	f(o)
	o.mu.Unlock()
}

func (p *Promoter) promote(ctx context.Context, rec *store.JobRecord) error {
	log := p.c.Log.With("job", rec.ID.Value(), "worker", rec.Owner.Value(), "orchestrator", p.c.Name)
	j, compSHA, bad, ok := p.job(ctx, rec, log)
	if !ok {
		return nil
	}
	if bad != "" {
		return p.fail(ctx, rec, log, bad)
	}
	body, _, err := p.c.Objects.Read(ctx, j.Staging+"COMPLETE.json", MaxComplete)
	switch {
	case errors.Is(err, cache.ErrNotFound):
		log.Warn("job promotion blocked: staging incomplete", "staging", j.Staging)
		return p.finish(ctx, rec, Blocked, nil)
	case errors.Is(err, cache.ErrTooLarge):
		return p.fail(ctx, rec, log, "COMPLETE.json is too large")
	case err != nil:
		return err
	}
	if compSHA != "" {
		h := sha256.Sum256(body)
		if hex.EncodeToString(h[:]) != strings.ToLower(compSHA) {
			return p.fail(ctx, rec, log, "COMPLETE.json does not match the signed result")
		}
	}
	if j.Complete, err = ParseComplete(body, j.ID, j.Build, j.Project); err != nil {
		return p.fail(ctx, rec, log, err.Error())
	}
	paths, err := p.claims(ctx, j)
	if err != nil {
		if isVerify(err) {
			return p.fail(ctx, rec, log, err.Error())
		}
		return err
	}

	l := p.lock(j.ID)
	stop, err := p.acquire(ctx, l)
	if err != nil {
		return err
	}
	defer p.unlock(context.WithoutCancel(ctx), j.ID, l)
	defer stop()
	// Re-read: another orchestrator may have finished while we waited for the lock. Its
	// result is then confirmed (every path already there) instead of redone.
	cur, err := store.Get(ctx, p.c.Fleet, &store.JobRecord{ID: rec.ID})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Promotion.Value() != Pending {
		if cur.Promotion.Value() == Done {
			p.confirm(ctx, j, paths, log)
		}
		return nil
	}

	out := &outcome{}
	if err := p.promotePaths(ctx, j, paths, out, log); err != nil {
		return err
	}
	if out.promoted > 0 {
		log.Info(fmt.Sprintf("promoted %d", out.promoted), "build", j.Build, "project", j.Project)
	}
	if out.already > 0 {
		log.Info(fmt.Sprintf("alreadyThere %d", out.already), "build", j.Build, "project", j.Project)
	}
	switch {
	case out.failed != nil:
		return p.fail(ctx, rec, log, out.failed.Error())
	case len(out.unknownRefs) > 0:
		if p.c.Now().Sub(rec.Changed.Value()) < unknownRefGrace {
			log.Info("job promotion waits for references that are not served yet", "refs", strings.Join(out.unknownRefs, " "))
			return nil
		}
		return p.fail(ctx, rec, log, "claimed references that are neither in the job, nor skipped, nor served: "+
			strings.Join(out.unknownRefs, " "))
	}
	if err := p.assets(ctx, j); err != nil {
		if isVerify(err) {
			return p.fail(ctx, rec, log, err.Error())
		}
		return err
	}
	images := len(j.Complete.Images) == 0
	if !images && p.c.Images != nil {
		if err := p.c.Images(ctx, j); err != nil {
			if isVerify(err) {
				return p.fail(ctx, rec, log, err.Error())
			}
			return err
		}
		images = true
	} else if !images {
		log.Warn("job has staged images, but this server has no registry; images stay in staging")
	}
	if err := p.finish(ctx, rec, Done, out.nonRepro); err != nil {
		return err
	}
	p.cleanup(ctx, j, images, log)
	return nil
}

// unlock releases the job lock and deletes its record (it is recreated by the next
// promotion attempt); a failure leaves an expired lock, which never blocks anybody.
func (p *Promoter) unlock(ctx context.Context, jobID string, l *store.Lease) {
	if err := l.Release(ctx); err != nil {
		return
	}
	if rec, err := store.Get(ctx, p.c.Control, &promoLock{Job: store.ID(jobID)}); err == nil {
		_ = store.Delete(ctx, p.c.Control, rec)
	}
}

// fail sets the verdict Failed and logs the security event with the worker id.
func (p *Promoter) fail(ctx context.Context, rec *store.JobRecord, log *slog.Logger, reason string) error {
	log.Warn("security: job promotion failed: artifact verification", "reason", reason)
	return p.finish(ctx, rec, Failed, nil)
}

// finish CASes the verdict into the job record (only the attempt that was promoted).
func (p *Promoter) finish(ctx context.Context, rec *store.JobRecord, verdict string, nonRepro []string) error {
	_, err := store.CAS(ctx, p.c.Fleet, &store.JobRecord{ID: rec.ID}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		if cur == nil || cur.State.Value() != jobSucceded || cur.Epoch.Value() != rec.Epoch.Value() || cur.Owner.Value() != rec.Owner.Value() || cur.Promotion.Value() != Pending {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.Promotion = store.Set(verdict)
		nr := slices.Clone(cur.NonReproducible.Value())
		for _, p := range nonRepro {
			if !slices.Contains(nr, p) {
				nr = append(nr, p)
			}
		}
		slices.Sort(nr)
		next.NonReproducible = store.Set(nr)
		return &next, nil
	})
	return err
}

// claims reads every staged narinfo claim and orders the paths topologically.
func (p *Promoter) claims(ctx context.Context, j *Job) ([]*stagedPath, error) {
	byHash := map[string]*stagedPath{}
	var out []*stagedPath
	for _, path := range j.Complete.Paths {
		h := cache.HashOf(path)
		body, _, err := p.c.Objects.Read(ctx, j.Staging+"narinfo/"+h+".narinfo", cache.MaxNarInfo)
		switch {
		case errors.Is(err, cache.ErrNotFound):
			return nil, verifyFailed("staged narinfo of %s is missing", path)
		case errors.Is(err, cache.ErrTooLarge):
			return nil, verifyFailed("staged narinfo of %s is too large", path)
		case err != nil:
			return nil, err
		}
		claim, err := cache.ParseNarInfo(body)
		if err != nil {
			return nil, verifyFailed("staged narinfo of %s: %v", path, err)
		}
		if claim.StorePath != path {
			return nil, verifyFailed("staged narinfo of %s claims StorePath %s", path, claim.StorePath)
		}
		sp := &stagedPath{path: path, hash: h, claim: claim}
		byHash[h] = sp
		out = append(out, sp)
	}
	for _, sp := range out {
		for _, r := range sp.claim.References {
			if rh := cache.HashOf(r); rh != sp.hash && byHash[rh] != nil {
				sp.deps = append(sp.deps, rh)
			}
		}
	}
	// levels: a path comes after every in-job path it references
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	var visit func(sp *stagedPath) error
	visit = func(sp *stagedPath) error {
		switch state[sp.hash] {
		case 1:
			return verifyFailed("reference cycle through %s", sp.path)
		case 2:
			return nil
		}
		state[sp.hash] = 1
		for _, d := range sp.deps {
			if err := visit(byHash[d]); err != nil {
				return err
			}
			sp.level = max(sp.level, byHash[d].level+1)
		}
		state[sp.hash] = 2
		return nil
	}
	for _, sp := range out {
		if err := visit(sp); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].level < out[b].level })
	return out, nil
}

// served reads the served narinfo of hash (nil when absent).
func (p *Promoter) served(ctx context.Context, hash string) (*cache.NarInfo, string, error) {
	body, etag, err := p.c.Objects.Read(ctx, cache.NarInfoKey(hash), cache.MaxNarInfo)
	if errors.Is(err, cache.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	ni, err := cache.ParseNarInfo(body)
	if err != nil {
		return nil, "", fmt.Errorf("served narinfo %s is damaged: %w", hash, err)
	}
	return ni, etag, nil
}

// confirm logs what another orchestrator's finished promotion of this job left served.
func (p *Promoter) confirm(ctx context.Context, j *Job, paths []*stagedPath, log *slog.Logger) {
	n := 0
	for _, sp := range paths {
		if ni, _, err := p.served(ctx, sp.hash); err == nil && ni != nil {
			n++
		}
	}
	if n > 0 {
		log.Info(fmt.Sprintf("alreadyThere %d", n), "build", j.Build, "project", j.Project)
	}
}

// promotePaths runs step 3 level by level, levelParallel paths at once. A verification
// failure stops before the next level, so dependents of a bad path are never served.
func (p *Promoter) promotePaths(ctx context.Context, j *Job, paths []*stagedPath, out *outcome, log *slog.Logger) error {
	known := map[string]bool{}
	for _, sp := range paths {
		known[sp.hash] = true
		for _, r := range sp.claim.References {
			known[cache.HashOf(r)] = true
		}
	}
	skipped := map[string]bool{}
	for _, s := range j.Complete.Skipped {
		known[cache.HashOf(s)] = true
		skipped[cache.HashOf(s)] = true
	}
	inJob := map[string]bool{}
	for _, sp := range paths {
		inJob[sp.hash] = true
	}
	r := &refs{p: p, inJob: inJob, skipped: skipped, known: known, servedCache: map[string]bool{}}
	for start := 0; start < len(paths); {
		end := start
		for end < len(paths) && paths[end].level == paths[start].level {
			end++
		}
		level := paths[start:end]
		start = end
		sem := make(chan struct{}, levelParallel)
		var waits []<-chan error
		for _, sp := range level {
			sem <- struct{}{}
			waits = append(waits, safe.Go(func() error {
				defer func() { <-sem }()
				return p.promotePath(ctx, j, sp, r, out, log)
			}))
		}
		var errs []error
		for _, w := range waits {
			if err := <-w; err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		if out.failed != nil || len(out.unknownRefs) > 0 {
			return nil
		}
	}
	return nil
}

// refs answers "is this reference acceptable" for one job.
type refs struct {
	p                     *Promoter
	inJob, skipped, known map[string]bool
	mu                    sync.Mutex
	servedCache           map[string]bool
}

func (r *refs) isServed(ctx context.Context, hash string) (bool, error) {
	r.mu.Lock()
	v, ok := r.servedCache[hash]
	r.mu.Unlock()
	if ok {
		return v, nil
	}
	_, err := r.p.c.Objects.Head(ctx, cache.NarInfoKey(hash))
	switch {
	case errors.Is(err, cache.ErrNotFound):
		v = false
	case err != nil:
		return false, err
	default:
		v = true
	}
	r.mu.Lock()
	r.servedCache[hash] = v
	r.mu.Unlock()
	return v, nil
}

// promotePath is step 3 for one path.
func (p *Promoter) promotePath(ctx context.Context, j *Job, sp *stagedPath, r *refs, out *outcome, log *slog.Logger) error {
	// a. already served
	srv, _, err := p.served(ctx, sp.hash)
	if err != nil {
		return err
	}
	if srv != nil {
		return p.alreadyServed(ctx, j, sp, srv, out, log)
	}
	claimed := map[string]bool{sp.hash: true}
	for _, ref := range sp.claim.References {
		h := cache.HashOf(ref)
		claimed[h] = true
		if h == sp.hash || r.inJob[h] || r.skipped[h] {
			continue
		}
		ok, err := r.isServed(ctx, h)
		if err != nil {
			return err
		}
		if !ok {
			out.add(func(o *outcome) { o.unknownRefs = append(o.unknownRefs, ref) })
			return nil
		}
	}
	// b. verify the staged NAR
	staged := j.Staging + "nar/" + sp.hash + ".nar.zst"
	v, err := verifyNAR(ctx, p.c.Objects, staged, r.known)
	if err != nil {
		if isVerify(err) {
			out.add(func(o *outcome) { o.failed = fmt.Errorf("%s: %w", sp.path, err) })
			return nil
		}
		return err
	}
	defer v.close()
	if reason := p.checkClaim(ctx, sp, v, claimed, r); reason != "" {
		if strings.HasPrefix(reason, "!") {
			return errors.New(reason[1:])
		}
		out.add(func(o *outcome) { o.failed = fmt.Errorf("%s: %s", sp.path, reason) })
		return nil
	}
	// c. copy the NAR (ETag guard on the source, create-only on the destination)
	switch nr, err := p.copyNAR(ctx, staged, sp, v); {
	case err != nil:
		return err
	case nr:
		out.add(func(o *outcome) { o.nonRepro = append(o.nonRepro, sp.path) })
		return nil
	}
	// d. listing and visibility
	if err := p.c.Objects.PutFile(ctx, cache.ListingKey(sp.hash), v.Listing, v.ListingSize, "application/json"); err != nil {
		return err
	}
	if err := p.c.Vis.AddProject(ctx, sp.hash, j.Project); err != nil {
		return err
	}
	// the promotion log entry precedes the narinfo (cache GC walks it, §05.4)
	if _, err := store.PromoLog(p.c.Control, p.c.Now()).Append(ctx, &store.PromoEntry{
		StoreHash: store.Set(sp.hash), Project: store.Set(j.Project), Build: store.Set(j.Build), At: store.Set(p.c.Now().UTC())}); err != nil {
		return err
	}
	// e. sign and publish the narinfo, create-only and last
	key, err := p.c.Keys.Active(ctx)
	if err != nil {
		return err
	}
	ni := &cache.NarInfo{StorePath: sp.path, URL: "nar/" + sp.hash + ".nar.zst", Compression: "zstd",
		FileHash: v.FileHash, FileSize: v.FileSize, NarHash: v.NarHash, NarSize: v.NarSize,
		References: slices.Sorted(slices.Values(sp.claim.References))}
	ni.References = slices.Compact(ni.References)
	ni.Sigs = []string{key.Sign(ni.Info())}
	_, err = p.c.Objects.Put(ctx, cache.NarInfoKey(sp.hash), ni.Format(), "text/x-nix-narinfo", cache.Cond{IfNoneMatch: true})
	switch {
	case errors.Is(err, cache.ErrPrecondition):
		out.add(func(o *outcome) { o.already++ }) // another orchestrator won: success
	case err != nil:
		return err
	default:
		out.add(func(o *outcome) { o.promoted++ })
	}
	return nil
}

// checkClaim compares the claim with what the NAR proved. A reason starting with "!" is a
// storage error (retried), any other reason a verification failure.
func (p *Promoter) checkClaim(ctx context.Context, sp *stagedPath, v *verified, claimed map[string]bool, r *refs) string {
	c := sp.claim
	switch {
	case c.NarHash != v.NarHash || c.NarSize != v.NarSize:
		return fmt.Sprintf("NAR does not match its claim (NarHash %s/%d, claimed %s/%d)", v.NarHash, v.NarSize, c.NarHash, c.NarSize)
	case c.FileHash != "" && (c.FileHash != v.FileHash || c.FileSize != v.FileSize):
		return "compressed NAR does not match its claim"
	}
	for h := range v.Found {
		if !claimed[h] {
			return "the NAR references " + h + ", which the claim does not list"
		}
	}
	for h := range v.Extra {
		ok, err := r.isServed(ctx, h)
		if err != nil {
			return "!" + err.Error()
		}
		if ok && !claimed[h] {
			return "the NAR references the served path " + h + ", which the claim does not list"
		}
	}
	return ""
}

// copyNAR is step 3c. nonRepro is true when the served-prefix NAR already holds different
// bytes of a published path.
func (p *Promoter) copyNAR(ctx context.Context, staged string, sp *stagedPath, v *verified) (nonRepro bool, err error) {
	meta := map[string]string{metaHash: v.NarHash}
	dst := cache.NarKey(sp.hash)
	err = p.c.Objects.Copy(ctx, staged, v.ETag, dst, meta, "")
	if !errors.Is(err, cache.ErrPrecondition) {
		return false, err
	}
	inf, herr := p.c.Objects.Head(ctx, dst)
	switch {
	case errors.Is(herr, cache.ErrNotFound):
		// dst absent, so the source changed after verification: retry the path from scratch
		return false, fmt.Errorf("staged NAR %s changed during promotion", staged)
	case herr != nil:
		return false, herr
	case inf.Meta[metaHash] == v.NarHash:
		return false, nil // our bytes (an earlier, crashed attempt or a concurrent orchestrator)
	}
	if srv, _, err := p.served(ctx, sp.hash); err != nil {
		return false, err
	} else if srv != nil {
		return srv.NarHash != v.NarHash, nil
	}
	if p.c.Now().Sub(inf.LastModified) < staleNAR {
		return false, fmt.Errorf("another promotion of %s with different bytes is in progress", sp.path)
	}
	// an orphan of a crashed promotion: replace exactly that version
	return false, p.c.Objects.Copy(ctx, staged, v.ETag, dst, meta, inf.ETag)
}

// alreadyServed is step 3a: the path is published. Its project is added only when the staged
// NAR proves the same content (a worker cannot gain visibility of another project's path by
// naming its hash).
func (p *Promoter) alreadyServed(ctx context.Context, j *Job, sp *stagedPath, srv *cache.NarInfo, out *outcome, log *slog.Logger) error {
	if srv.NarHash != sp.claim.NarHash {
		log.Warn("non-reproducible path: the published bytes are kept", "path", sp.path,
			"published", srv.NarHash, "staged", sp.claim.NarHash)
		out.add(func(o *outcome) { o.nonRepro = append(o.nonRepro, sp.path) })
		return nil
	}
	doc, err := p.c.Vis.Get(ctx, sp.hash)
	if err != nil {
		return err
	}
	if doc == nil || !slices.Contains(doc.Projects, j.Project) {
		v, err := verifyNAR(ctx, p.c.Objects, j.Staging+"nar/"+sp.hash+".nar.zst", map[string]bool{})
		if err != nil {
			if isVerify(err) {
				out.add(func(o *outcome) { o.failed = fmt.Errorf("%s: %w", sp.path, err) })
				return nil
			}
			return err
		}
		v.close()
		if v.NarHash != srv.NarHash {
			out.add(func(o *outcome) { o.nonRepro = append(o.nonRepro, sp.path) })
			return nil
		}
		if err := p.c.Vis.AddProject(ctx, sp.hash, j.Project); err != nil {
			return err
		}
	}
	out.add(func(o *outcome) { o.already++ })
	return nil
}

// assets is step 5: assets are verified and stay in staging until a release copies them.
func (p *Promoter) assets(ctx context.Context, j *Job) error {
	for _, a := range j.Complete.Assets {
		key := j.Staging + "assets/" + a.File
		sum, n, err := hashStream(ctx, p.c.Objects, key)
		switch {
		case errors.Is(err, cache.ErrNotFound):
			return verifyFailed("staged asset %s is missing", a.File)
		case err != nil:
			return err
		case sum != a.SHA256 || n != a.Size:
			return verifyFailed("staged asset %s does not match COMPLETE.json", a.File)
		}
	}
	return nil
}

// cleanup deletes the staged objects of the promoted job by known key (R-STORE-105): NARs and
// claims, OCI blobs once images are promoted, and COMPLETE.json last when nothing in staging
// is still needed (assets stay until a release copies them). The rest expires by the
// lifecycle rule or the staging sweep.
func (p *Promoter) cleanup(ctx context.Context, j *Job, imagesDone bool, log *slog.Logger) {
	var keys []string
	for _, path := range j.Complete.Paths {
		h := cache.HashOf(path)
		keys = append(keys, j.Staging+"nar/"+h+".nar.zst", j.Staging+"narinfo/"+h+".narinfo")
	}
	if imagesDone {
		for _, im := range j.Complete.Images {
			for _, d := range append([]string{im.Manifest}, im.Blobs...) {
				keys = append(keys, j.Staging+"oci/blobs/sha256/"+strings.TrimPrefix(d, "sha256:"))
			}
		}
	}
	if imagesDone && len(j.Complete.Assets) == 0 {
		keys = append(keys, j.Staging+"COMPLETE.json")
	}
	n := 0
	for i, k := range keys {
		if k == j.Staging+"COMPLETE.json" && n < i {
			break // keep the manifest while objects it names remain
		}
		if err := p.c.Objects.Delete(ctx, k); err != nil {
			log.Warn("staged object not deleted (expires with the lifecycle rule)", "key", k, "err", err.Error())
			continue
		}
		n++
	}
}
