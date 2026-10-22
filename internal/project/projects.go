// Package project owns projects on the server (SPEC §6.5.6, §05.4.2): the proj/ records, the
// projects index, ProjectService, the Projects provider of the build controller and the
// pipelines cache behind ListPipelines.
package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/integration"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	projectv1 "github.com/megakuul/bob/internal/gen/bob/project/v1"
	"github.com/megakuul/bob/internal/store"
)

// Linker links repositories to projects (integration.Registry, R-PIPE-002).
type Linker interface {
	LinkRepo(ctx context.Context, forgeID, fullName, project, baseURL string) (integration.Linked, error)
	UnlinkRepo(ctx context.Context, forgeID string, repoID int64) error
	Health(ctx context.Context, id string) (int32, error) // forgev1.Health of the integration
}

// Config configures Projects.
type Config struct {
	Control *store.Bucket
	Links   Linker // nil: projects cannot link repositories
	BaseURL string // webhook URLs (integration.HookURL)
	Now     func() time.Time
	Log     *slog.Logger
}

// Projects is the project store shared by ProjectService, the build controller (Project) and
// its hooks (pipelines cache).
type Projects struct {
	cfg  Config
	idx  *store.PagedIndex[Entry]
	recs store.Cache[Record]
	ctr  store.Cache[IndexCounter]
	ppl  store.Cache[PipelinesRecord]
	run  store.Cache[store.RunningBuilds]
}

// New performs no I/O.
func New(cfg Config) (*Projects, error) {
	if cfg.Control == nil {
		return nil, errors.New("project: the control bucket is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Projects{cfg: cfg, idx: Index(cfg.Control)}, nil
}

func (p *Projects) now() time.Time { return p.cfg.Now().UTC() }

// Errors callers act on; the service maps them to the §6.1 error model.
var (
	ErrNotFound = errors.New("project not found")
	ErrExists   = errors.New("project already exists")
	ErrConflict = errors.New("the project was changed by someone else (etag mismatch); reload and retry")
	ErrRunning  = errors.New("the project has builds that have not finished; cancel them first")
	// ErrReleases: DeleteProject is refused while the project has releases (R-STORE-007).
	ErrReleases = errors.New("the project has releases; delete them first (releases are never removed with their project)")
)

// InvalidError is a request the caller must fix; Field is the protojson path.
type InvalidError struct{ Field, Msg string }

func (e *InvalidError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &InvalidError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// LinkError is a repository link the forge or the link records refused.
type LinkError struct {
	Linked bool // already linked to another project (AlreadyExists)
	Msg    string
}

func (e *LinkError) Error() string { return e.Msg }

// Project ids: 1–40 characters, a lowercase letter first, no trailing hyphen (decision
// D-IB12-1: SPEC's slug minimum of 3 characters would refuse the p1/p2 of the suites and of
// the RBAC bindings, which already accept short ids, D-IB5-7).
var (
	idRE   = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	repoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}(/[A-Za-z0-9_.-]{1,100}){1,8}$`)
)

// Limits of the free-text fields.
const (
	maxDisplayName = 100
	maxDescription = 2000
)

// ValidID reports whether id is a project id.
func ValidID(id string) bool { return idRE.MatchString(id) }

// Spec is what a caller may set on a project.
type Spec struct {
	ID, DisplayName, Description string
	Forge, RepoName              string // both or neither
}

func (s *Spec) validate() error {
	switch {
	case !ValidID(s.ID):
		return invalid("project.id", "project id must be 1-40 characters a-z, 0-9 and '-', starting with a letter")
	case len(s.DisplayName) > maxDisplayName || strings.ContainsAny(s.DisplayName, "\x00\n\r"):
		return invalid("project.displayName", "at most %d characters on one line", maxDisplayName)
	case len(s.Description) > maxDescription || strings.ContainsRune(s.Description, 0):
		return invalid("project.description", "at most %d characters", maxDescription)
	case s.Forge == "" && s.RepoName != "":
		return invalid("project.forgeIntegrationId", "a repository needs the forge integration it lives on")
	case s.Forge != "" && s.RepoName == "":
		return invalid("project.repoFullName", "the repository (owner/name) is required with a forge integration")
	case s.RepoName != "" && !repoRE.MatchString(s.RepoName):
		return invalid("project.repoFullName", "not a repository name (owner/name)")
	case s.Forge != "":
		if _, err := store.Segment(s.Forge); err != nil {
			return invalid("project.forgeIntegrationId", "invalid integration id")
		}
	}
	return nil
}

// Get reads a project (conditional GET against the local copy).
func (p *Projects) Get(ctx context.Context, id string) (*Record, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	rec, _, err := store.GetCached(ctx, p.cfg.Control, &p.recs, &Record{ID: store.ID(id)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return rec, err
}

// Project implements buildctl.Projects: the default branch and the linked repository.
func (p *Projects) Project(ctx context.Context, id string) (*buildctl.Project, error) {
	rec, err := p.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, buildctl.ErrNoProject
	}
	if err != nil {
		return nil, err
	}
	return &buildctl.Project{ID: rec.ID.Value(), DefaultBranch: rec.DefaultBranch.Value(), Repo: rec.Repo()}, nil
}

// ForUpload implements buildctl.UploadProjects: the first `--remote` build of a project id
// creates the project without a repository (decision D-IB13-1). The caller is authorized for
// CreateBuild on that id; the record grants nothing, bindings are by id.
func (p *Projects) ForUpload(ctx context.Context, id string) (*buildctl.Project, error) {
	if _, err := p.Create(ctx, Spec{ID: id, Description: "created by a remote build"}); err != nil && !errors.Is(err, ErrExists) {
		var inv *InvalidError
		if errors.As(err, &inv) {
			return nil, buildctl.ErrNoProject
		}
		return nil, err
	}
	return p.Project(ctx, id)
}

// Health is the link health of a project; it reads the integration's health (one
// conditional GET) when the project has a link.
func (p *Projects) Health(ctx context.Context, rec *Record) projectv1.LinkHealth {
	if rec.Forge.Value() == "" {
		return projectv1.LinkHealth_LINK_HEALTH_UNSPECIFIED
	}
	if p.cfg.Links != nil {
		if h, err := p.cfg.Links.Health(ctx, rec.Forge.Value()); err == nil && h == int32(forgev1.Health_HEALTH_AUTH_FAILED) {
			return projectv1.LinkHealth_LINK_HEALTH_AUTH_FAILED
		}
	}
	return storedHealth(rec)
}

func storedHealth(rec *Record) projectv1.LinkHealth {
	return healthOf(rec.Forge.Value(), rec.HookErr.Value())
}

func healthOf(forgeID, hookErr string) projectv1.LinkHealth {
	switch {
	case forgeID == "":
		return projectv1.LinkHealth_LINK_HEALTH_UNSPECIFIED
	case hookErr != "":
		return projectv1.LinkHealth_LINK_HEALTH_WEBHOOK_MISSING
	}
	return projectv1.LinkHealth_LINK_HEALTH_OK
}

func entryOf(rec *Record, pending bool, at time.Time) Entry {
	return makeEntry(rec.ID.Value(), rec.DisplayName.Value(), rec.Description.Value(), linkOf(rec), rec.Created.Value(), pending, at)
}

func makeEntry(id, displayName, description string, l repoLink, created time.Time, pending bool, at time.Time) Entry {
	return Entry{ID: id, Pending: pending, At: at, DisplayName: displayName, Description: description,
		Forge: l.Forge, RepoName: l.RepoName, RepoID: l.RepoID, DefaultBranch: l.DefaultBranch,
		WebURL: l.WebURL, Health: int32(healthOf(l.Forge, l.HookErr)), Created: created}
}

// repoLink is the linked repository of a project as plain values (the link fields of Record).
type repoLink struct {
	Forge, RepoName       string
	RepoID                int64
	DefaultBranch, WebURL string
	HookErr               string
}

func linkOf(rec *Record) repoLink {
	return repoLink{Forge: rec.Forge.Value(), RepoName: rec.RepoName.Value(), RepoID: rec.RepoID.Value(),
		DefaultBranch: rec.DefaultBranch.Value(), WebURL: rec.WebURL.Value(), HookErr: rec.HookErr.Value()}
}

// apply writes l into rec (update operators; an empty link clears the fields).
func (l repoLink) apply(rec *Record) {
	rec.Forge, rec.RepoName, rec.RepoID = store.Set(l.Forge), store.Set(l.RepoName), store.Set(l.RepoID)
	rec.DefaultBranch, rec.WebURL, rec.HookErr = store.Set(l.DefaultBranch), store.Set(l.WebURL), store.Set(l.HookErr)
}

// link links repository repo of integration forgeID to project id and returns what the forge
// says about it.
func (p *Projects) link(ctx context.Context, id, forgeID, repo string) (repoLink, error) {
	if p.cfg.Links == nil {
		return repoLink{}, invalid("project.forgeIntegrationId", "forge integrations are not available on this server")
	}
	l, err := p.cfg.Links.LinkRepo(ctx, forgeID, repo, id, p.cfg.BaseURL)
	switch {
	case errors.Is(err, integration.ErrLinked):
		return repoLink{}, &LinkError{Linked: true, Msg: "repository " + repo + " is already linked to another project"}
	case errors.Is(err, integration.ErrUnknown):
		return repoLink{}, invalid("project.forgeIntegrationId", "forge integration %s not found", forgeID)
	case errors.Is(err, forge.ErrNotFound):
		return repoLink{}, invalid("project.repoFullName", "repository %s not found on forge integration %s", repo, forgeID)
	case errors.Is(err, forge.ErrAuth):
		return repoLink{}, &LinkError{Msg: fmt.Sprintf("forge: integration %s: forge rejected bob's credentials (HTTP 401); re-enter the credentials in Settings → Forges", forgeID)}
	case err != nil:
		p.cfg.Log.Warn("project: linking the repository failed", "project", id, "integration", forgeID, "repository", repo, "err", err.Error())
		return repoLink{}, &LinkError{Msg: fmt.Sprintf("forge integration %s: the repository could not be linked, try again", forgeID)}
	}
	out := repoLink{Forge: forgeID, RepoName: l.Repo.FullName, RepoID: l.Repo.ID, DefaultBranch: l.Repo.DefaultBranch, WebURL: l.Repo.WebURL}
	if out.RepoName == "" {
		out.RepoName = repo
	}
	if l.HookErr != nil {
		out.HookErr = "bob's webhook could not be created on the repository; add it by hand or give the integration admin rights"
	}
	return out, nil
}

func (p *Projects) unlink(ctx context.Context, forgeID string, repoID int64) {
	if p.cfg.Links == nil || forgeID == "" || repoID == 0 {
		return
	}
	if err := p.cfg.Links.UnlinkRepo(ctx, forgeID, repoID); err != nil {
		p.cfg.Log.Warn("project: unlinking a repository failed", "integration", forgeID, "repo", repoID, "err", err.Error())
	}
}

// allocate takes the next slot of the projects index.
func (p *Projects) allocate(ctx context.Context) (uint64, error) {
	c, err := store.CAS(ctx, p.cfg.Control, &IndexCounter{Name: store.ID("all")}, func(cur *IndexCounter) (*IndexCounter, error) {
		var count uint64
		if cur != nil {
			count = cur.Count.Value()
		}
		return &IndexCounter{Name: store.ID("all"), Count: store.Set(count + 1)}, nil
	})
	if err != nil {
		return 0, fmt.Errorf("pidxm/all: %w", err)
	}
	return c.Count.Value() - 1, nil
}

// Create creates a project, linking its repository first when it names one. Intent first
// (R-SRV-101): a hidden index entry, then the record, then the visible entry.
func (p *Projects) Create(ctx context.Context, s Spec) (*Record, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if _, err := store.Get(ctx, p.cfg.Control, &Record{ID: store.ID(s.ID)}); err == nil {
		return nil, ErrExists
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	now := p.now()
	display := s.DisplayName
	if display == "" {
		display = s.ID
	}
	var l repoLink
	if s.Forge != "" {
		var err error
		if l, err = p.link(ctx, s.ID, s.Forge, s.RepoName); err != nil {
			return nil, err
		}
	}
	slot, err := p.allocate(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.idx.Set(ctx, slot, makeEntry(s.ID, display, s.Description, l, now, true, now)); err != nil {
		return nil, fmt.Errorf("pidx: %w", err)
	}
	rec := &Record{ID: store.ID(s.ID), DisplayName: store.Set(display), Description: store.Set(s.Description), Created: store.Set(now),
		Slot: store.Set(slot + 1)}
	if l.Forge != "" {
		l.apply(rec)
	}
	if err := store.Create(ctx, p.cfg.Control, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			_ = p.idx.Set(ctx, slot, Entry{}) // another create of the same id won
			return nil, ErrExists
		}
		return nil, err
	}
	if err := p.idx.Set(ctx, slot, makeEntry(s.ID, display, s.Description, l, now, false, now)); err != nil {
		p.cfg.Log.Warn("project: index entry left pending; index repair shows it", "project", s.ID, "err", err.Error())
	}
	p.cfg.Log.Info("project created", "project", s.ID, "integration", l.Forge, "repository", l.RepoName)
	return store.Get(ctx, p.cfg.Control, &Record{ID: store.ID(s.ID)})
}

// Update changes the fields named in mask (display_name, description, forge_integration_id,
// repo_full_name). etag "" updates unconditionally (decision D-IB4-3).
func (p *Projects) Update(ctx context.Context, s Spec, mask []string, etag string) (*Record, error) {
	cur, err := store.Get(ctx, p.cfg.Control, &Record{ID: store.ID(s.ID)})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if etag != "" && etag != store.ETagOf(cur.ETag) {
		return nil, ErrConflict
	}
	want := *cur
	relink := false
	next := Spec{ID: cur.ID.Value(), DisplayName: cur.DisplayName.Value(), Description: cur.Description.Value(), Forge: cur.Forge.Value(), RepoName: cur.RepoName.Value()}
	for _, f := range mask {
		switch f {
		case "display_name":
			next.DisplayName = s.DisplayName
		case "description":
			next.Description = s.Description
		case "forge_integration_id":
			next.Forge, relink = s.Forge, true
		case "repo_full_name":
			next.RepoName, relink = s.RepoName, true
		}
	}
	if err := next.validate(); err != nil {
		return nil, err
	}
	if next.DisplayName == "" {
		next.DisplayName = cur.ID.Value()
	}
	want.DisplayName, want.Description = store.Set(next.DisplayName), store.Set(next.Description)
	relink = relink && (next.Forge != cur.Forge.Value() || !strings.EqualFold(next.RepoName, cur.RepoName.Value()))
	newLink := linkOf(cur)
	if relink {
		newLink = repoLink{}
		if next.Forge != "" {
			if newLink, err = p.link(ctx, cur.ID.Value(), next.Forge, next.RepoName); err != nil {
				return nil, err
			}
		}
		newLink.apply(&want)
	}
	rec, err := store.CAS(ctx, p.cfg.Control, &Record{ID: store.ID(s.ID)}, func(c *Record) (*Record, error) {
		if c == nil {
			return nil, ErrNotFound
		}
		if store.ETagOf(c.ETag) != store.ETagOf(cur.ETag) {
			return nil, ErrConflict
		}
		w := want
		return &w, nil
	})
	if err != nil {
		if relink && (newLink.Forge != cur.Forge.Value() || newLink.RepoID != cur.RepoID.Value()) {
			p.unlink(ctx, newLink.Forge, newLink.RepoID) // the new link must not outlive a refused update
		}
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
			return nil, err
		case errors.Is(err, store.ErrConflict):
			return nil, ErrConflict
		}
		return nil, err
	}
	if relink && (newLink.Forge != cur.Forge.Value() || newLink.RepoID != cur.RepoID.Value()) {
		p.unlink(ctx, cur.Forge.Value(), cur.RepoID.Value())
	}
	if rec.Slot.Value() > 0 {
		if err := p.idx.Set(ctx, rec.Slot.Value()-1, entryOf(rec, false, p.now())); err != nil {
			p.cfg.Log.Warn("project: index entry not updated; index repair refreshes it", "project", rec.ID.Value(), "err", err.Error())
		}
	}
	return rec, nil
}

// Delete removes a project that has no unfinished build: its index entry is hidden first,
// then the repository is unlinked and the record deleted, then the entry cleared. Builds,
// logs and releases of the project are left to retention. etag "" deletes unconditionally.
func (p *Projects) Delete(ctx context.Context, id, etag string) error {
	cur, err := store.Get(ctx, p.cfg.Control, &Record{ID: store.ID(id)})
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if etag != "" && etag != store.ETagOf(cur.ETag) {
		return ErrConflict
	}
	running, _, err := store.GetCached(ctx, p.cfg.Control, &p.run, &store.RunningBuilds{Name: store.ID("running")})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if running != nil {
		for _, b := range running.Builds.Value() {
			if b.Project == id {
				return ErrRunning
			}
		}
	}
	if live, err := store.HasLiveReleases(ctx, p.cfg.Control, id); err != nil {
		return err
	} else if live {
		return ErrReleases
	}
	if cur.Slot.Value() > 0 {
		hidden := entryOf(cur, true, p.now())
		if err := p.idx.Set(ctx, cur.Slot.Value()-1, hidden); err != nil {
			return fmt.Errorf("pidx: %w", err)
		}
	}
	p.unlink(ctx, cur.Forge.Value(), cur.RepoID.Value())
	if err := store.Delete(ctx, p.cfg.Control, cur); err != nil {
		if cur.Forge.Value() != "" && p.cfg.Links != nil {
			_, _ = p.cfg.Links.LinkRepo(ctx, cur.Forge.Value(), cur.RepoName.Value(), cur.ID.Value(), p.cfg.BaseURL) // undo
		}
		if errors.Is(err, store.ErrConflict) {
			return ErrConflict
		}
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if cur.Slot.Value() > 0 {
		if err := p.idx.Set(ctx, cur.Slot.Value()-1, Entry{}); err != nil {
			p.cfg.Log.Warn("project: index entry left pending; index repair clears it", "project", id, "err", err.Error())
		}
	}
	if err := store.Change(ctx, p.cfg.Control, &PipelinesRecord{Project: store.ID(id)}, func(*PipelinesRecord) (*PipelinesRecord, error) {
		return nil, nil // deletes the pipelines cache
	}); err != nil {
		p.cfg.Log.Debug("project: pipelines cache not removed", "project", id, "err", err.Error())
	}
	p.cfg.Log.Info("project deleted", "project", id)
	return nil
}

// count is the number of slots of the projects index.
func (p *Projects) count(ctx context.Context) (uint64, error) {
	c, _, err := store.GetCached(ctx, p.cfg.Control, &p.ctr, &IndexCounter{Name: store.ID("all")})
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return c.Count.Value(), nil
}

// List returns visible index entries newest first, starting after skip slots from the end,
// for which keep is true; next is the skip value of the following page (0 = end). It reads
// the counter and the pages it needs (§05.5.2: 1 + pages).
func (p *Projects) List(ctx context.Context, skip uint64, size int, keep func(Entry) bool) (out []Entry, next uint64, err error) {
	n, err := p.count(ctx)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	var page []Entry
	pageNo := ^uint64(0)
	for pos := skip; pos < n; pos++ {
		if len(out) == size {
			return out, pos, nil
		}
		slot := n - 1 - pos
		if k := slot / indexPage; k != pageNo {
			pageNo = k
			page, err = p.idx.Page(ctx, k)
			if errors.Is(err, store.ErrNotFound) {
				page = nil
			} else if err != nil {
				return nil, 0, err
			}
		}
		if page == nil {
			continue
		}
		e := page[slot%indexPage]
		if e.ID == "" || e.Pending || seen[e.ID] || !keep(e) {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out, 0, nil
}

// repairAge is how old a pending entry must be before repair settles it (an index write
// in flight is never touched).
const repairAge = 5 * time.Minute

// Repair settles pending index entries older than 5 minutes (crashed creates, updates and
// deletes): an entry whose record exists at that slot becomes visible again with the
// record's fields, any other is cleared. It runs in the index-repair pass.
func (p *Projects) Repair(ctx context.Context) error {
	n, err := p.count(ctx)
	if err != nil {
		return err
	}
	now := p.now()
	var errs []error
	for k := uint64(0); k*indexPage < n; k++ {
		page, err := p.idx.Page(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		for i, e := range page {
			if !e.Pending || now.Sub(e.At) < repairAge {
				continue
			}
			slot := k*indexPage + uint64(i)
			want := Entry{}
			rec, err := store.Get(ctx, p.cfg.Control, &Record{ID: store.ID(e.ID)})
			switch {
			case err == nil && rec.Slot.Value() == slot+1:
				want = entryOf(rec, false, now)
			case err != nil && !errors.Is(err, store.ErrNotFound):
				errs = append(errs, err)
				continue
			}
			if err := p.idx.Set(ctx, slot, want); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
