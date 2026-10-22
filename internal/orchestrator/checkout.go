package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Shard string                      `pk:"ftok" cbor:"-"`
	ETag  string                      `etag:"true" cbor:"-"`
	Jobs  map[string][]CheckoutRecord `cbor:"m,omitempty"`
}

func shardOf(jobID string) (*checkoutShard, error) {
	sh, err := store.Sh2(jobID, checkoutShards)
	if err != nil {
		return nil, err
	}
	return &checkoutShard{Shard: sh}, nil
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
	if rec.Owner != wid || rec.Epoch != epoch || (rec.State != StateClaimed && rec.State != StateRunning) {
		return Checkout{}, errNoLease(jobID)
	}
	spec, _ := fleet.DecodeSpec(rec.Spec)
	if spec.Repo == "" {
		return Checkout{}, fmt.Errorf("job %s has no repository (uploaded source)", jobID)
	}
	key, err := shardOf(jobID)
	if err != nil {
		return Checkout{}, err
	}
	now := c.now()
	handle, co, err := c.Forge.Mint(ctx, spec.Repo, now.Add(CheckoutTTL))
	if err != nil {
		return Checkout{}, fmt.Errorf("forge: issue checkout token for %s: %w", spec.Repo, err)
	}
	if err := validCheckout(co); err != nil {
		return Checkout{}, errors.Join(err, c.Forge.Revoke(ctx, handle))
	}
	forge, _, _ := strings.Cut(spec.Repo, "/")
	entry := CheckoutRecord{Epoch: epoch, Forge: forge, Handle: handle, Expires: now.Add(CheckoutTTL), ForgeExpires: co.Expires.UTC()}
	_, err = store.CAS(ctx, c.Control, key, func(cur *checkoutShard) (*checkoutShard, error) {
		next := checkoutShard{Shard: key.Shard, Jobs: map[string][]CheckoutRecord{}}
		if cur != nil {
			next.ETag = cur.ETag
			for k, v := range cur.Jobs {
				next.Jobs[k] = v
			}
		}
		next.Jobs[jobID] = append(append([]CheckoutRecord(nil), next.Jobs[jobID]...), entry)
		return &next, nil
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
	_, err = c.revoke(ctx, key, map[string][]CheckoutRecord{jobID: cur.Jobs[jobID]})
	return err
}

// Sweep is one forge-sweeper pass (every 60 s) over ftok/*: revoke tokens of terminal jobs.
// Tokens past their expiry are revoked too, even while the job runs.
func (c *Checkouts) Sweep(ctx context.Context) (revoked int, err error) {
	now := c.now()
	var errs []error
	for i := 0; i < checkoutShards; i++ {
		key := &checkoutShard{Shard: fmt.Sprintf("%02d", i)}
		cur, _, err := store.GetCached(ctx, c.Control, &c.cache, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return revoked, err
		}
		due := map[string][]CheckoutRecord{}
		for jobID, entries := range cur.Jobs {
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
		next := checkoutShard{Shard: key.Shard, ETag: cur.ETag, Jobs: map[string][]CheckoutRecord{}}
		for jobID, entries := range cur.Jobs {
			var keep []CheckoutRecord
			for _, e := range entries {
				if !gone[e.Handle] {
					keep = append(keep, e)
				}
			}
			if len(keep) > 0 {
				next.Jobs[jobID] = keep
			}
		}
		if len(next.Jobs) == 0 {
			return nil, nil // last entry: delete the shard document
		}
		return &next, nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return len(gone), errors.Join(errs...)
}
