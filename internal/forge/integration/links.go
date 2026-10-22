package integration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/store"
)

// ErrLinked is a repository that is already linked to a project (R-PIPE-002: AlreadyExists).
var ErrLinked = errors.New("forge: repository already linked to a project")

// ErrNotLinked is a repository without a project link.
var ErrNotLinked = errors.New("forge: repository not linked")

// HookURL is the webhook URL of integration id under the server's base URL.
func HookURL(baseURL, id string) string {
	return strings.TrimSuffix(baseURL, "/") + "/hooks/" + id
}

// Linked is the outcome of LinkRepo.
type Linked struct {
	Repo forge.Repo
	// HookErr is why bob's webhook could not be ensured (e.g. Forgejo serves hooks only to
	// repository admins). The link stands; pushes trigger nothing until the hook exists, and
	// ProjectService reports it.
	HookErr error
}

// LinkRepo links repository fullName of integration forgeID to project (ProjectService,
// R-PIPE-002): it resolves the repository at the forge, ensures bob's webhook on it (an
// app-level GitHub hook needs none), and writes the link: the integration's Repos entry first
// (intent-first, R-SRV-101), then the create-only link record. A repository linked to another
// project is ErrLinked.
func (r *Registry) LinkRepo(ctx context.Context, forgeID, fullName, project, baseURL string) (Linked, error) {
	rec, a, err := r.Adapter(ctx, forgeID)
	if err != nil {
		return Linked{}, err
	}
	repo, err := a.LookupRepo(ctx, fullName)
	r.Report(ctx, forgeID, err)
	if err != nil {
		return Linked{}, err
	}
	if p, ok := rec.Repos.Value()[strconv.FormatInt(repo.ID, 10)]; ok && p != project {
		if _, err := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repo.ID)); err == nil {
			return Linked{}, fmt.Errorf("%w (project %s)", ErrLinked, p)
		}
	}
	var hookErr error
	if err := a.EnsureWebhook(ctx, repo, HookURL(baseURL, forgeID), forge.NewSecret(rec.Secret.Value())); err != nil && !errors.Is(err, forge.ErrUnsupported) {
		hookErr = err
		r.log().Warn("forge: webhook not ensured; the repository is linked but pushes trigger nothing", "integration", forgeID,
			"repository", repo.FullName, "err", err)
	}
	rid := strconv.FormatInt(repo.ID, 10)
	if _, err := store.CAS(ctx, r.Control, ForgeKey(forgeID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, ErrUnknown
		}
		if cur.Repos.Value()[rid] == project {
			return nil, store.ErrNoChange
		}
		if len(cur.Repos.Value()) >= MaxLinkedRepos {
			return nil, fmt.Errorf("forge: integration %s links %d repositories already", forgeID, len(cur.Repos.Value()))
		}
		if cur.Repos.Value() == nil {
			cur.Repos = store.Set(map[string]string{})
		}
		if _, taken := cur.Repos.Value()[rid]; !taken {
			cur.Repos.Value()[rid] = project
		}
		return cur, nil
	}); err != nil {
		return Linked{}, err
	}
	link := RepoLinkKey(forgeID, repo.ID)
	link.Project, link.FullName, link.CloneURL, link.Created = store.Set(project), store.Set(repo.FullName), store.Set(repo.CloneURL), store.Set(r.now().UTC())
	err = store.Create(ctx, r.Control, link)
	if errors.Is(err, store.ErrExists) {
		cur, gerr := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repo.ID))
		if gerr == nil && cur.Project.Value() == project {
			return Linked{Repo: repo, HookErr: hookErr}, nil
		}
		return Linked{}, ErrLinked
	}
	if err != nil {
		return Linked{}, err
	}
	// The Repos entry may still name a stale project of a crashed link; make it this one.
	_, err = store.CAS(ctx, r.Control, ForgeKey(forgeID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil || cur.Repos.Value()[rid] == project {
			return nil, store.ErrNoChange
		}
		cur.Repos.Value()[rid] = project
		return cur, nil
	})
	return Linked{Repo: repo, HookErr: hookErr}, err
}

// UnlinkRepo removes the link of repository repoID (project deletion or relinking): the link
// record first, then the integration's Repos entry.
func (r *Registry) UnlinkRepo(ctx context.Context, forgeID string, repoID int64) error {
	link, err := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repoID))
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	default:
		if err := store.Delete(ctx, r.Control, link); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	rid := strconv.FormatInt(repoID, 10)
	_, err = store.CAS(ctx, r.Control, ForgeKey(forgeID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		if _, ok := cur.Repos.Value()[rid]; !ok {
			return nil, store.ErrNoChange
		}
		delete(cur.Repos.Value(), rid)
		return cur, nil
	})
	return err
}

// Link returns the link of repository repoID of integration forgeID, or ErrNotLinked.
func (r *Registry) Link(ctx context.Context, forgeID string, repoID int64) (*RepoLinkRecord, error) {
	link, err := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repoID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotLinked
	}
	return link, err
}

// SetStatus writes a commit status on linked repository repoID of integration forgeID with the
// integration's credentials (the status controller, §6.9.4); ErrAuth also marks the
// integration AUTH_FAILED. Status credentials never leave the orchestrator.
func (r *Registry) SetStatus(ctx context.Context, forgeID string, repoID int64, s forge.Status) error {
	link, err := r.Link(ctx, forgeID, repoID)
	if err != nil {
		return err
	}
	rec, a, err := r.Adapter(ctx, forgeID)
	if err != nil {
		return err
	}
	err = a.SetStatus(ctx, forge.Repo{Forge: forge.Kind(rec.Kind.Value()), ID: repoID, FullName: link.FullName.Value(), CloneURL: link.CloneURL.Value()}, s)
	r.Report(ctx, forgeID, err)
	return err
}
