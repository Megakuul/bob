package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// Job download tokens (SPEC §05.9.1 GetCheckoutCredentialResponse.download_token, §6.9.6): a
// bob_dt_ token of scope download whose TokenRecord owner is "job:<project>/<jobID>". It
// authenticates as a principal without bindings that reads the private cache paths of that
// one project (api.Principal.CacheProject), so a job's Nix substitutes what its project built
// and nothing of other projects. The orchestrator revokes it when the job ends (decision A211).

const jobOwnerPrefix = "job:"

// parseJobOwner splits the owner value "<project>/<jobID>" (after "job:"); both parts are
// key segments, which contain no "/".
func parseJobOwner(owner string) (project, job string, ok bool) {
	project, job, ok = strings.Cut(owner, "/")
	if !ok {
		return "", "", false
	}
	if _, err := store.Segment(project); err != nil {
		return "", "", false
	}
	if _, err := store.Segment(job); err != nil {
		return "", "", false
	}
	return project, job, true
}

// IssueJobDownload creates the download token of job jobID, valid for project's cache paths
// until expires. handle is the token's record key (its SHA-256), which names it for
// RevokeJobDownload without being the token.
func (id *Identity) IssueJobDownload(ctx context.Context, jobID, project string, expires time.Time) (token, handle string, err error) {
	owner := project + "/" + jobID
	if _, _, ok := parseJobOwner(owner); !ok {
		return "", "", fmt.Errorf("job download token: invalid project %q or job id %q", project, jobID)
	}
	dt := newToken(PrefixDownload)
	rec := store.TokenKey(dt.reveal())
	rec.Owner = store.Set(jobOwnerPrefix + owner)
	rec.Kind, rec.Scope = store.Set[string]("dt"), store.Set[string]("download")
	rec.Expires, rec.Created = store.Set(expires.UTC()), store.Set(id.now().UTC())
	if err := store.Create(ctx, id.control, rec); err != nil {
		return "", "", fmt.Errorf("job download token: %w", err)
	}
	return dt.reveal(), rec.Hash.Value(), nil
}

// RevokeJobDownload deletes the job download token named by handle (absent is fine). It only
// deletes job tokens: a handle can never revoke a user's or service account's token.
func (id *Identity) RevokeJobDownload(ctx context.Context, handle string) error {
	if _, err := store.Segment(handle); err != nil {
		return fmt.Errorf("job download token: handle: %w", err)
	}
	id.tokens.drop(handle)
	err := store.Change(ctx, id.control, &store.TokenRecord{Hash: store.ID(handle)}, func(cur *store.TokenRecord) (*store.TokenRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if !strings.HasPrefix(cur.Owner.Value(), jobOwnerPrefix) {
			return nil, errors.New("job download token: the handle names a token that is not a job's")
		}
		return nil, nil
	})
	id.tokens.drop(handle)
	return err
}
