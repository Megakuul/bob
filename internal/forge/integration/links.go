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

// LinkRepo links repository fullName of integration forgeID to project (ProjectService,
// R-PIPE-002): it resolves the repository at the forge, ensures bob's webhook on it (an
// app-level GitHub hook needs none), and writes the link: the integration's Repos entry first
// (intent-first, R-SRV-101), then the create-only link record. A repository linked to another
// project is ErrLinked.
func (r *Registry) LinkRepo(ctx context.Context, forgeID, fullName, project, baseURL string) (forge.Repo, error) {
	rec, a, err := r.Adapter(ctx, forgeID)
	if err != nil {
		return forge.Repo{}, err
	}
	repo, err := a.LookupRepo(ctx, fullName)
	r.Report(ctx, forgeID, err)
	if err != nil {
		return forge.Repo{}, err
	}
	if p, ok := rec.Repos[strconv.FormatInt(repo.ID, 10)]; ok && p != project {
		if _, err := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repo.ID)); err == nil {
			return forge.Repo{}, fmt.Errorf("%w (project %s)", ErrLinked, p)
		}
	}
	err = a.EnsureWebhook(ctx, repo, HookURL(baseURL, forgeID), forge.NewSecret(rec.Secret))
	r.Report(ctx, forgeID, err)
	if err != nil && !errors.Is(err, forge.ErrUnsupported) {
		return forge.Repo{}, err
	}
	rid := strconv.FormatInt(repo.ID, 10)
	if _, err := store.CAS(ctx, r.Control, ForgeKey(forgeID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil {
			return nil, ErrUnknown
		}
		if cur.Repos[rid] == project {
			return nil, store.ErrNoChange
		}
		if len(cur.Repos) >= MaxLinkedRepos {
			return nil, fmt.Errorf("forge: integration %s links %d repositories already", forgeID, len(cur.Repos))
		}
		if cur.Repos == nil {
			cur.Repos = map[string]string{}
		}
		if _, taken := cur.Repos[rid]; !taken {
			cur.Repos[rid] = project
		}
		return cur, nil
	}); err != nil {
		return forge.Repo{}, err
	}
	link := RepoLinkKey(forgeID, repo.ID)
	link.Project, link.FullName, link.CloneURL, link.Created = project, repo.FullName, repo.CloneURL, r.now().UTC()
	err = store.Create(ctx, r.Control, link)
	if errors.Is(err, store.ErrExists) {
		cur, gerr := store.Get(ctx, r.Control, RepoLinkKey(forgeID, repo.ID))
		if gerr == nil && cur.Project == project {
			return repo, nil
		}
		return forge.Repo{}, ErrLinked
	}
	if err != nil {
		return forge.Repo{}, err
	}
	// The Repos entry may still name a stale project of a crashed link; make it this one.
	_, err = store.CAS(ctx, r.Control, ForgeKey(forgeID), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil || cur.Repos[rid] == project {
			return nil, store.ErrNoChange
		}
		cur.Repos[rid] = project
		return cur, nil
	})
	return repo, err
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
		if _, ok := cur.Repos[rid]; !ok {
			return nil, store.ErrNoChange
		}
		delete(cur.Repos, rid)
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
