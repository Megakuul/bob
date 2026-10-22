package integration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/orchestrator"
)

// Minter is the forge side of GetCheckoutCredential (SPEC §6.9.6): it implements
// orchestrator.ForgeMinter (and JobMinter) with the integration's adapter. The job's repository
// is "<forgeID>/<forge repo id>" (JobSpec.Repo); the handle it returns is
// "<forgeID>/<adapter handle>", so that a revoke finds the adapter again.
type Minter struct {
	Registry *Registry

	mu        sync.Mutex
	lastSweep time.Time
}

// NameSweepEvery is how often the forge-sweeper also sweeps by token name (§6.9.6).
const NameSweepEvery = 5 * time.Minute

// sweepMargin is added to the longest checkout lifetime before a token without a record is
// swept: a token older than orchestrator.CheckoutTTL is past its record's expiry, which the
// forge-sweeper revokes even while the job runs.
const sweepMargin = 10 * time.Minute

func splitRepo(repo string) (string, int64, error) {
	forgeID, rid, ok := strings.Cut(repo, "/")
	id, err := strconv.ParseInt(rid, 10, 64)
	if !ok || forgeID == "" || err != nil || id <= 0 {
		return "", 0, fmt.Errorf("forge: job repository %q is not <integration>/<repository id>", repo)
	}
	return forgeID, id, nil
}

// Mint implements orchestrator.ForgeMinter for callers without a job id.
func (m *Minter) Mint(ctx context.Context, repo string, expires time.Time) (string, orchestrator.Checkout, error) {
	return m.MintForJob(ctx, "", repo, expires)
}

// MintForJob mints the read-only checkout credential of the job's one repository.
func (m *Minter) MintForJob(ctx context.Context, jobID, repo string, expires time.Time) (string, orchestrator.Checkout, error) {
	forgeID, rid, err := splitRepo(repo)
	if err != nil {
		return "", orchestrator.Checkout{}, err
	}
	link, err := m.Registry.Link(ctx, forgeID, rid)
	if err != nil {
		return "", orchestrator.Checkout{}, fmt.Errorf("forge: repository %s: %w", repo, err)
	}
	rec, a, err := m.Registry.Adapter(ctx, forgeID)
	if err != nil {
		return "", orchestrator.Checkout{}, err
	}
	if jobID == "" {
		jobID = "nojob"
	}
	ttl := time.Until(expires)
	if ttl > orchestrator.CheckoutTTL {
		ttl = orchestrator.CheckoutTTL
	}
	co, err := a.IssueCheckout(ctx, forge.CheckoutRequest{JobID: jobID, TTL: ttl,
		Repo: forge.Repo{Forge: forge.Kind(rec.Kind), ID: rid, FullName: link.FullName, CloneURL: link.CloneURL}})
	m.Registry.Report(ctx, forgeID, err)
	if err != nil {
		return "", orchestrator.Checkout{}, err
	}
	return forgeID + "/" + co.Handle, orchestrator.Checkout{CloneURL: co.CloneURL, Username: co.Username,
		Token: co.Token.Reveal(), Expires: co.ExpiresAt}, nil
}

// Revoke implements orchestrator.ForgeMinter: idempotent at the forge.
func (m *Minter) Revoke(ctx context.Context, handle string) error {
	forgeID, h, ok := strings.Cut(handle, "/")
	if !ok || forgeID == "" || h == "" {
		return fmt.Errorf("forge: checkout handle %q", handle)
	}
	_, a, err := m.Registry.Adapter(ctx, forgeID)
	if errors.Is(err, ErrUnknown) {
		return nil // the integration is gone, and its credentials with it
	}
	if err != nil {
		return err
	}
	err = a.RevokeCheckout(ctx, forge.Checkout{Handle: h})
	m.Registry.Report(ctx, forgeID, err)
	return err
}

// SweepNames is the forge-sweeper's name sweep (every NameSweepEvery): SweepCheckouts on every
// integration with cutoff now − CheckoutTTL − 10 min, for tokens whose CheckoutRecord a crash
// between mint and record never wrote. Forges that expire tokens themselves do nothing.
func (m *Minter) SweepNames(ctx context.Context) error {
	now := m.Registry.now()
	m.mu.Lock()
	due := now.Sub(m.lastSweep) >= NameSweepEvery
	if due {
		m.lastSweep = now
	}
	m.mu.Unlock()
	if !due {
		return nil
	}
	ids, err := m.Registry.IDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		_, a, err := m.Registry.Adapter(ctx, id)
		if errors.Is(err, ErrUnknown) {
			continue
		}
		if err == nil {
			_, err = a.SweepCheckouts(ctx, now.Add(-orchestrator.CheckoutTTL-sweepMargin))
			m.Registry.Report(ctx, id, err)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("integration %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

var (
	_ orchestrator.ForgeMinter = (*Minter)(nil)
	_ orchestrator.JobMinter   = (*Minter)(nil)
)
