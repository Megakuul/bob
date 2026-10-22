package orchestrator

import (
	"context"
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

// CheckoutRecord is one outstanding per-job forge token: never the token value (D62).
type CheckoutRecord struct {
	Epoch        int64     `cbor:"ep,omitempty"`
	Forge        string    `cbor:"f,omitempty"`
	Handle       string    `cbor:"h,omitempty"`
	Expires      time.Time `cbor:"ex,omitzero"`
	ForgeExpires time.Time `cbor:"fx,omitzero"`
}

// checkoutShard is ftok/<sh2(jobID)>: job id -> its outstanding tokens.
type checkoutShard struct {
	Shard store.KeyField                               `pk:"ftok" cbor:"-"`
	ETag  store.ETagField                              `etag:"true" cbor:"-"`
	Jobs  store.DataField[map[string][]CheckoutRecord] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *checkoutShard) Draft() *checkoutShard {
	return &checkoutShard{Shard: r.Shard,
		ETag: r.ETag,
		Jobs: store.Carry(r.Jobs)}
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
	Now     func() time.Time

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
	rec, err := c.Queue.record(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return Checkout{}, errNoLease(jobID)
	}
	if err != nil {
		return Checkout{}, err
	}
	if rec.Owner.Value() != wid || rec.Epoch.Value() != epoch || (rec.State.Value() != StateClaimed && rec.State.Value() != StateRunning) {
		return Checkout{}, errNoLease(jobID)
	}
	spec, _ := fleet.DecodeSpec(rec.Spec.Value())
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
	_, err = store.CAS(ctx, c.Control, key, func(cur *checkoutShard) (*checkoutShard, error) {
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
				if ended || e.Expires.Before(now) || (!e.ForgeExpires.IsZero() && e.ForgeExpires.Before(now)) {
					due[jobID] = append(due[jobID], e)
				}
			}
		}
		n, err := c.revoke(ctx, key, due)
		revoked += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return revoked, errors.Join(errs...)
}

// revoke revokes the given tokens at the forge and removes those that were revoked from the
// shard; a token whose revoke failed stays for the next sweep.
func (c *Checkouts) revoke(ctx context.Context, key *checkoutShard, due map[string][]CheckoutRecord) (int, error) {
	gone := map[string]bool{}
	var errs []error
	for jobID, entries := range due {
		for _, e := range entries {
			if err := c.Forge.Revoke(ctx, e.Handle); err != nil {
				slog.Warn("forge-sweeper: revoke failed, retried next pass", "job", jobID, "forge", e.Forge, "err", err)
				errs = append(errs, err)
				continue
			}
			gone[e.Handle] = true
		}
	}
	if len(gone) == 0 {
		return 0, errors.Join(errs...)
	}
	_, err := store.CAS(ctx, c.Control, &checkoutShard{Shard: key.Shard}, func(cur *checkoutShard) (*checkoutShard, error) {
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
		if len(jobs) == 0 {
			return nil, nil // last entry: delete the shard document
		}
		return &checkoutShard{Shard: key.Shard, Jobs: store.Set(jobs)}, nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return len(gone), errors.Join(errs...)
}
