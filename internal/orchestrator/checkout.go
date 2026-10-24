package orchestrator

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/store"
)

// Checkout is the credential a worker gets for one job (SPEC §6.9.6).
type Checkout struct {
	CloneURL string
	Username string
	Token    string
	Expires  time.Time
}

// ForgeMinter is the forge adapter's token part (IssueCheckout / RevokeCheckout).
type ForgeMinter interface {
	Mint(ctx context.Context, repo string, expires time.Time) (handle string, c Checkout, err error)
	Revoke(ctx context.Context, handle string) error
}

// JobMinter is a ForgeMinter that names the token after the job (the forge adapters: Forgejo
// tokens are bob-job-<unix-seconds>-<jobID>, D62); Get prefers it.
type JobMinter interface {
	MintForJob(ctx context.Context, jobID, repo string, expires time.Time) (handle string, c Checkout, err error)
}

// CheckoutTTL is the lifetime asked of a per-job forge token; the sweeper revokes it at expiry
// even while the job runs, and the worker asks again (D62).
const CheckoutTTL = time.Hour

// checkoutShards is the shard count of ftok/<sh2(jobID)>.
const checkoutShards = 16

// DownloadIssuer mints and revokes the per-job cache download tokens of
// GetCheckoutCredentialResponse.download_token (auth.Identity): bob_dt_, download scope, the
// job's project only. The handle names the token without being it.
type DownloadIssuer interface {
	IssueJobDownload(ctx context.Context, jobID, project string, expires time.Time) (token, handle string, err error)
	RevokeJobDownload(ctx context.Context, handle string) error
}

// DownloadTTL is the lifetime of a job's download token. It is a backstop only: the token is
// revoked when the job ends or its lease moves on (JobEnded, the forge-sweeper), and the
// job's Nix needs it for the whole job, which may be longer than CheckoutTTL.
const DownloadTTL = 24 * time.Hour

// CheckoutRecord is one outstanding per-job token: a forge token (D62) or, with Download, the
// job's cache download token. Never the token value.
type CheckoutRecord struct {
	Epoch        int64     `cbor:"ep,omitempty"`
	Forge        string    `cbor:"f,omitempty"`
	Handle       string    `cbor:"h,omitempty"`
	Expires      time.Time `cbor:"ex,omitzero"`
	ForgeExpires time.Time `cbor:"fx,omitzero"`
	Download     bool      `cbor:"dt,omitempty"`
}

// LeaseBinding is the lease of a job that an orchestrator accepted (the first
// GetCheckoutCredential of that worker and epoch). It lives in the control bucket, which
// workers cannot write, because the job record they can rewrite (ADR 0013 T10) is no proof of
// ownership on its own (I-R2 F2, decision A390).
type LeaseBinding struct {
	Worker string    `cbor:"w,omitempty"`
	Epoch  int64     `cbor:"ep,omitempty"`
	At     time.Time `cbor:"at,omitzero"`
}

// checkoutShard is ftok/<sh2(jobID)>: job id -> its outstanding tokens, and job id -> the
// lease the orchestrators accepted for it.
type checkoutShard struct {
	Shard  store.KeyField                               `pk:"ftok" cbor:"-"`
	ETag   store.ETagField                              `etag:"true" cbor:"-"`
	Jobs   store.DataField[map[string][]CheckoutRecord] `cbor:"m,omitempty"`
	Leases store.DataField[map[string]LeaseBinding]     `cbor:"l,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *checkoutShard) Draft() *checkoutShard {
	return &checkoutShard{
		Shard:  r.Shard,
		ETag:   r.ETag,
		Jobs:   store.Carry(r.Jobs),
		Leases: store.Carry(r.Leases),
	}
}

// withJobs is the update value of shard key with token entries jobs and the leases of cur
// (nil: none); nil when both are empty, which deletes the document.
func withJobs(key *checkoutShard, cur *checkoutShard, jobs map[string][]CheckoutRecord) *checkoutShard {
	next := &checkoutShard{Shard: key.Shard}
	if len(jobs) > 0 {
		next.Jobs = store.Set(jobs)
	}
	if cur != nil {
		next.Leases = store.Carry(cur.Leases)
	}
	if next.Jobs == nil && next.Leases == nil {
		return nil
	}
	return next
}

func shardOf(jobID string) (*checkoutShard, error) {
	sh, err := store.Sh2(jobID, checkoutShards)
	if err != nil {
		return nil, err
	}
	return &checkoutShard{Shard: store.ID(sh)}, nil
}

// Checkouts serves GetCheckoutCredential and the forge-sweeper.
type Checkouts struct {
	Control *store.Bucket
	Queue   *Queue
	Forge   ForgeMinter
	// Downloads mints the jobs' cache download tokens; nil: jobs get none (they substitute
	// only public paths).
	Downloads DownloadIssuer
	Now       func() time.Time
	// LeaseTTL and Skew are fleet.lease_ttl and the clock skew allowance (defaults 60 s, 5 s):
	// a lease the orchestrators accepted moves to another worker only when the reconciler
	// would requeue the job (its holder is revoked, gone or has no live heartbeat).
	LeaseTTL, Skew time.Duration

	cache store.Cache[checkoutShard]
}

func (c *Checkouts) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Get returns a checkout credential for worker wid, job id at epoch. A worker that does not
// own the job at that epoch (owner + epoch on job/<id>, state claimed or running) gets
// "permission_denied: worker holds no lease on job <id>".
func (c *Checkouts) Get(ctx context.Context, wid, jobID string, epoch int64) (Checkout, error) {
	spec, err := c.held(ctx, wid, jobID, epoch)
	if err != nil {
		return Checkout{}, err
	}
	if spec.Repo == "" {
		return Checkout{}, fmt.Errorf("job %s has no repository (uploaded source)", jobID)
	}
	key, err := shardOf(jobID)
	if err != nil {
		return Checkout{}, err
	}
	now := c.now()
	var handle string
	var co Checkout
	if jm, ok := c.Forge.(JobMinter); ok {
		handle, co, err = jm.MintForJob(ctx, jobID, spec.Repo, now.Add(CheckoutTTL))
	} else {
		handle, co, err = c.Forge.Mint(ctx, spec.Repo, now.Add(CheckoutTTL))
	}
	if err != nil {
		return Checkout{}, fmt.Errorf("forge: issue checkout token for %s: %w", spec.Repo, err)
	}
	if err := validCheckout(co); err != nil {
		return Checkout{}, errors.Join(err, c.Forge.Revoke(ctx, handle))
	}
	forge, _, _ := strings.Cut(spec.Repo, "/")
	entry := CheckoutRecord{Epoch: epoch, Forge: forge, Handle: handle, Expires: now.Add(CheckoutTTL), ForgeExpires: co.Expires.UTC()}
	err = store.Change(ctx, c.Control, key, func(cur *checkoutShard) (*checkoutShard, error) {
		jobs := map[string][]CheckoutRecord{}
		if cur != nil {
			maps.Copy(jobs, cur.Jobs.Value())
		}
		jobs[jobID] = append(append([]CheckoutRecord(nil), jobs[jobID]...), entry)
		return &checkoutShard{Shard: key.Shard, Jobs: store.Set(jobs)}, nil
	})
	if err != nil {
		// No record means no sweeper: revoke now rather than leak the token.
		return Checkout{}, errors.Join(err, c.Forge.Revoke(ctx, handle))
	}
	return co, nil
}

// held returns the spec of job jobID when worker wid holds it at epoch, else errNoLease. The
// job record is worker-writable (ADR 0013 T10), so it is only the first check: the spec must
// be signed by an orchestrator for this job id, and the lease must be the one the
// orchestrators accepted in the control bucket (bind).
func (c *Checkouts) held(ctx context.Context, wid, jobID string, epoch int64) (store.JobSpec, error) {
	rec, err := c.Queue.record(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return store.JobSpec{}, errNoLease(jobID)
	}
	if err != nil {
		return store.JobSpec{}, err
	}
	if rec.Owner.Value() != wid || rec.Epoch.Value() != epoch || (rec.State.Value() != StateClaimed && rec.State.Value() != StateRunning) {
		return store.JobSpec{}, errNoLease(jobID)
	}
	spec, ok := fleet.DecodeSpec(rec.Spec.Value())
	if !ok || spec.JobID != jobID || !fleet.VerifySpec(c.specKeys(ctx), rec.Spec.Value(), rec.SpecSig.Value()) {
		slog.Warn("security: checkout refused: the job spec is not signed by an orchestrator for this job", "job", jobID, "worker", wid)
		return store.JobSpec{}, errNoLease(jobID)
	}
	if err := c.bind(ctx, wid, jobID, epoch); err != nil {
		return store.JobSpec{}, err
	}
	return spec, nil
}

// specKeys are the orchestrator keys a job spec may be signed with: the queue's own key and
// the keys published in fleet/cfg.k (rotation).
func (c *Checkouts) specKeys(ctx context.Context) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	if len(c.Queue.Key) == ed25519.PrivateKeySize {
		if pub, ok := c.Queue.Key.Public().(ed25519.PublicKey); ok {
			keys = append(keys, pub)
		}
	}
	if fc, err := store.Get(ctx, c.Queue.Fleet, store.FleetConfigKey()); err == nil {
		for _, k := range fc.Keys.Value() {
			keys = append(keys, k.Pub)
		}
	}
	return keys
}

// bind records (or confirms) that worker wid holds job jobID at epoch, in ftok/<sh2(jobID)> of
// the control bucket. A lease that was accepted for another worker, or for a later epoch, is
// refused, unless the reconciler's requeue rule says the accepted holder's lease is over (it
// is revoked or gone, or has no live heartbeat signed by its key that lists the job). So a
// worker that rewrites the job record cannot take over a job another live worker holds
// (I-R2 F2, decision A390).
func (c *Checkouts) bind(ctx context.Context, wid, jobID string, epoch int64) error {
	key, err := shardOf(jobID)
	if err != nil {
		return err
	}
	now := c.now()
	type verdict struct {
		b    LeaseBinding
		over bool
	}
	var seen []verdict // the holder checks of earlier CAS rounds (fn may run again)
	refused := false
	err = store.Change(ctx, c.Control, key, func(cur *checkoutShard) (*checkoutShard, error) {
		refused = false
		leases := map[string]LeaseBinding{}
		var jobs map[string][]CheckoutRecord
		if cur != nil {
			maps.Copy(leases, cur.Leases.Value())
			jobs = cur.Jobs.Value()
		}
		b, ok := leases[jobID]
		switch {
		case ok && b.Worker == wid && b.Epoch == epoch:
			return nil, store.ErrNoChange
		case ok && epoch <= b.Epoch:
			refused = true
			return nil, store.ErrNoChange
		case ok && b.Worker != wid:
			over, known := false, false
			for _, v := range seen {
				if v.b == b {
					over, known = v.over, true
				}
			}
			if !known {
				o, err := c.leaseOver(ctx, b, jobID)
				if err != nil {
					return nil, err
				}
				over = o
				seen = append(seen, verdict{b, o})
			}
			if !over {
				refused = true
				return nil, store.ErrNoChange
			}
		}
		leases[jobID] = LeaseBinding{Worker: wid, Epoch: epoch, At: now}
		next := &checkoutShard{Shard: key.Shard, Leases: store.Set(leases)}
		if len(jobs) > 0 {
			next.Jobs = store.Set(jobs)
		}
		return next, nil
	})
	if err != nil {
		return err
	}
	if refused {
		slog.Warn("security: checkout refused: another worker holds the job's accepted lease", "job", jobID, "worker", wid, "epoch", epoch)
		return errNoLease(jobID)
	}
	return nil
}

// leaseOver reports whether the accepted lease b of job jobID has ended by the reconciler's
// rule (§05.6.1), judged only from state its holder alone can write: its worker record is
// revoked, gone or absent; its heartbeat is absent, not signed by its key or older than
// lease_ttl + skew; or a heartbeat newer than the binding no longer lists the job.
func (c *Checkouts) leaseOver(ctx context.Context, b LeaseBinding, jobID string) (bool, error) {
	if _, err := store.Segment(b.Worker); err != nil {
		return true, nil
	}
	w, err := store.Get(ctx, c.Queue.Fleet, &store.WorkerRecord{Wid: store.ID(b.Worker)})
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if st := w.State.Value(); st == "revoked" || st == "gone" {
		return true, nil
	}
	hb, err := store.Get(ctx, c.Queue.Fleet, &store.Heartbeat{Wid: store.ID(b.Worker)})
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	ttl, skew := c.LeaseTTL, c.Skew
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	if skew <= 0 {
		skew = 5 * time.Second
	}
	at := hb.At.Value()
	if !fleet.VerifyHeartbeat(w.SigPub.Value(), hb) || at.Add(ttl+skew).Before(c.now()) {
		return true, nil
	}
	if !at.After(b.At.Add(skew)) {
		return false, nil // the holder has not had a heartbeat since the binding
	}
	for _, j := range hb.Jobs.Value() {
		if j.Job == jobID && (j.Epoch == b.Epoch || j.Epoch == 0) {
			return false, nil
		}
	}
	return true, nil
}

// Lease is what GetCheckoutCredential knows of a held job besides its forge credential.
type Lease struct {
	Spec store.JobSpec
	// DownloadToken is the job's cache download token ("" without Downloads or without a
	// project); it expires at DownloadExpires unless revoked earlier.
	DownloadToken   string
	DownloadExpires time.Time
}

// Lease checks that worker wid holds job jobID at epoch (else errNoLease, nothing minted) and
// mints the job's cache download token, recorded in ftok/<sh2(jobID)> (only its handle) so that
// JobEnded and the forge-sweeper revoke it. Download tokens the job got before (an earlier
// epoch, or a repeated call) are revoked once the new one is recorded.
func (c *Checkouts) Lease(ctx context.Context, wid, jobID string, epoch int64) (Lease, error) {
	spec, err := c.held(ctx, wid, jobID, epoch)
	if err != nil {
		return Lease{}, err
	}
	l := Lease{Spec: spec}
	if c.Downloads == nil || spec.Project == "" {
		return l, nil
	}
	key, err := shardOf(jobID)
	if err != nil {
		return Lease{}, err
	}
	now := c.now()
	expires := now.Add(DownloadTTL)
	tok, handle, err := c.Downloads.IssueJobDownload(ctx, jobID, spec.Project, expires)
	if err != nil {
		return Lease{}, fmt.Errorf("download token for job %s: %w", jobID, err)
	}
	entry := CheckoutRecord{Epoch: epoch, Handle: handle, Expires: expires, Download: true}
	var older []CheckoutRecord
	err = store.Change(ctx, c.Control, key, func(cur *checkoutShard) (*checkoutShard, error) {
		jobs := map[string][]CheckoutRecord{}
		if cur != nil {
			maps.Copy(jobs, cur.Jobs.Value())
		}
		older = nil
		for _, e := range jobs[jobID] {
			if e.Download {
				older = append(older, e)
			}
		}
		jobs[jobID] = append(append([]CheckoutRecord(nil), jobs[jobID]...), entry)
		return withJobs(key, cur, jobs), nil
	})
	if err != nil {
		// No record means no sweeper: revoke now rather than leak the token.
		return Lease{}, errors.Join(err, c.Downloads.RevokeJobDownload(ctx, handle))
	}
	if len(older) > 0 {
		if _, err := c.revoke(ctx, key, map[string][]CheckoutRecord{jobID: older}); err != nil {
			slog.Warn("checkout: an earlier download token of the job was not revoked yet, the sweeper retries", "job", jobID, "err", err)
		}
	}
	l.DownloadToken, l.DownloadExpires = tok, expires
	return l, nil
}

// validCheckout refuses an adapter answer that would leak a credential in a URL.
func validCheckout(co Checkout) error {
	u, err := url.Parse(co.CloneURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return fmt.Errorf("forge: clone URL %q is not an https URL without credentials", co.CloneURL)
	}
	if co.Token == "" || strings.Contains(co.CloneURL, co.Token) {
		return errors.New("forge: checkout token missing or part of the clone URL")
	}
	return nil
}

// JobEnded revokes the job's tokens (called at job end).
func (c *Checkouts) JobEnded(ctx context.Context, jobID string) error {
	key, err := shardOf(jobID)
	if err != nil {
		return err
	}
	cur, err := store.Get(ctx, c.Control, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = c.revoke(ctx, key, map[string][]CheckoutRecord{jobID: cur.Jobs.Value()[jobID]})
	return err
}

// Sweep is one forge-sweeper pass (every 60 s) over ftok/*: revoke tokens of terminal jobs.
// Tokens past their expiry are revoked too, even while the job runs.
func (c *Checkouts) Sweep(ctx context.Context) (revoked int, err error) {
	now := c.now()
	var errs []error
	for i := 0; i < checkoutShards; i++ {
		key := &checkoutShard{Shard: store.ID(fmt.Sprintf("%02d", i))}
		cur, _, err := store.GetCached(ctx, c.Control, &c.cache, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return revoked, err
		}
		due := map[string][]CheckoutRecord{}
		for jobID, entries := range cur.Jobs.Value() {
			v, err := c.Queue.Job(ctx, jobID)
			ended := errors.Is(err, store.ErrNotFound) || (err == nil && Terminal(v.State))
			if err != nil && !ended {
				errs = append(errs, err)
				continue
			}
			for _, e := range entries {
				// A download token belongs to one lease: requeued or claimed again, it is due.
				moved := e.Download && v != nil && (e.Epoch != v.Epoch || v.State == StateQueued)
				if ended || moved || e.Expires.Before(now) || (!e.ForgeExpires.IsZero() && e.ForgeExpires.Before(now)) {
					due[jobID] = append(due[jobID], e)
				}
			}
		}
		n, err := c.revoke(ctx, key, due)
		revoked += n
		if err != nil {
			errs = append(errs, err)
		}
		// A lease binding stays while the job record exists (only orchestrators delete job
		// records, at finalise); a terminal state is no reason, workers can write it.
		var finished []string
		for jobID := range cur.Leases.Value() {
			if _, err := c.Queue.Job(ctx, jobID); errors.Is(err, store.ErrNotFound) {
				finished = append(finished, jobID)
			} else if err != nil {
				errs = append(errs, err)
			}
		}
		if err := c.dropLeases(ctx, key, finished); err != nil {
			errs = append(errs, err)
		}
	}
	return revoked, errors.Join(errs...)
}

// dropLeases removes the lease bindings of finalised jobs from the shard.
func (c *Checkouts) dropLeases(ctx context.Context, key *checkoutShard, jobIDs []string) error {
	if len(jobIDs) == 0 {
		return nil
	}
	return store.Change(ctx, c.Control, &checkoutShard{Shard: key.Shard}, func(cur *checkoutShard) (*checkoutShard, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		leases := maps.Clone(cur.Leases.Value())
		for _, id := range jobIDs {
			delete(leases, id)
		}
		if len(leases) == len(cur.Leases.Value()) {
			return nil, store.ErrNoChange
		}
		next := &checkoutShard{Shard: key.Shard, Jobs: store.Carry(cur.Jobs)}
		if len(leases) > 0 {
			next.Leases = store.Set(leases)
		}
		if next.Jobs == nil && next.Leases == nil {
			return nil, nil
		}
		return next, nil
	})
}

// revoke revokes the given tokens at the forge and removes those that were revoked from the
// shard; a token whose revoke failed stays for the next sweep.
func (c *Checkouts) revoke(ctx context.Context, key *checkoutShard, due map[string][]CheckoutRecord) (int, error) {
	gone := map[string]bool{}
	var errs []error
	for jobID, entries := range due {
		for _, e := range entries {
			if err := c.revokeOne(ctx, e); err != nil {
				slog.Warn("forge-sweeper: revoke failed, retried next pass", "job", jobID, "forge", e.Forge, "download", e.Download, "err", err)
				errs = append(errs, err)
				continue
			}
			gone[e.Handle] = true
		}
	}
	if len(gone) == 0 {
		return 0, errors.Join(errs...)
	}
	err := store.Change(ctx, c.Control, &checkoutShard{Shard: key.Shard}, func(cur *checkoutShard) (*checkoutShard, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		jobs := map[string][]CheckoutRecord{}
		for jobID, entries := range cur.Jobs.Value() {
			var keep []CheckoutRecord
			for _, e := range entries {
				if !gone[e.Handle] {
					keep = append(keep, e)
				}
			}
			if len(keep) > 0 {
				jobs[jobID] = keep
			}
		}
		// nil (no tokens and no leases left) deletes the shard document
		return withJobs(key, cur, jobs), nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return len(gone), errors.Join(errs...)
}

// revokeOne revokes one recorded token at the forge, or the job's download token at bob.
func (c *Checkouts) revokeOne(ctx context.Context, e CheckoutRecord) error {
	if !e.Download {
		return c.Forge.Revoke(ctx, e.Handle)
	}
	if c.Downloads == nil {
		return errors.New("no download token issuer configured")
	}
	return c.Downloads.RevokeJobDownload(ctx, e.Handle)
}
