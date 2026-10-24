package release

import (
	"context"
	"errors"
	"slices"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/megakuul/bob/internal/api"
	buildctl "github.com/megakuul/bob/internal/build"
	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
	releasev1 "github.com/megakuul/bob/internal/gen/bob/release/v1"
	"github.com/megakuul/bob/internal/gen/bob/release/v1/releasev1connect"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/store"
)

const (
	methodList    = "bob.release.v1.ReleaseService/ListReleases"
	methodGet     = "bob.release.v1.ReleaseService/GetRelease"
	maxDesc       = 10 << 10
	maxIndexPages = 2000
)

// Service is ReleaseService (SPEC §6.5.11). The PUBLIC methods show a private release only to
// callers with the method's permission on its project (NotFound otherwise, like a missing one).
type Service struct {
	R     *Releases
	Scope project.Scoper
}

var _ releasev1connect.ReleaseServiceHandler = (*Service)(nil)

func stateOf(s string) releasev1.ReleaseState {
	switch s {
	case store.ReleasePending:
		return releasev1.ReleaseState_RELEASE_STATE_PENDING
	case store.ReleasePublishing:
		return releasev1.ReleaseState_RELEASE_STATE_PUBLISHING
	case store.ReleasePublished:
		return releasev1.ReleaseState_RELEASE_STATE_PUBLISHED
	case store.ReleaseFailed:
		return releasev1.ReleaseState_RELEASE_STATE_FAILED
	case store.ReleaseDeleted:
		return releasev1.ReleaseState_RELEASE_STATE_DELETED
	}
	return releasev1.ReleaseState_RELEASE_STATE_UNSPECIFIED
}

func kindOf(k string) releasev1.AssetKind {
	switch k {
	case "appimage":
		return releasev1.AssetKind_ASSET_KIND_APPIMAGE
	case "tarball":
		return releasev1.AssetKind_ASSET_KIND_TARBALL
	}
	return releasev1.AssetKind_ASSET_KIND_FILE
}

// message is the API form of a record.
func (s *Releases) message(r *store.ReleaseRecord) *releasev1.Release {
	project := r.Project.Value()
	out := &releasev1.Release{Id: ID(project, numOf(r)), ProjectId: project, Name: r.Name.Value(), Version: r.Version.Value(),
		Visibility: commonv1.Visibility_VISIBILITY_PRIVATE, State: stateOf(r.State.Value()), BuildId: r.Build.Value(),
		Sha: r.SHA.Value(), Description: r.Description.Value(), Etag: store.ETagOf(r.ETag)}
	if r.Public.Value() {
		out.Visibility = commonv1.Visibility_VISIBILITY_PUBLIC
	}
	if t := r.Created.Value(); !t.IsZero() {
		out.CreatedAt = timestamppb.New(t)
	}
	for _, c := range r.Closures.Value() {
		out.Closures = append(out.Closures, &releasev1.PlatformClosure{Platform: c.Platform, Roots: c.Roots, ClosureNarBytes: c.NarBytes})
	}
	for _, a := range r.Assets.Value() {
		out.Assets = append(out.Assets, &releasev1.Asset{File: a.File, Kind: kindOf(a.Kind), Platform: a.Platform, Size: a.Size,
			Sha256: a.SHA256, Url: s.c.BaseURL + "/r/" + project + "/" + r.Name.Value() + "/" + r.Version.Value() + "/" + a.File})
	}
	for _, im := range r.Images.Value() {
		out.Images = append(out.Images, &releasev1.ImageRef{Repository: im.Repo, Tag: im.Tag, Digest: im.Digest})
	}
	return out
}

// visibleTo reports whether a release may be shown: public and published, or the caller holds
// the method on its project. Deleted releases are never shown.
func visibleTo(state string, public bool, project string, allowed func(string) bool) bool {
	if state == store.ReleaseDeleted {
		return false
	}
	return public && state == store.ReleasePublished || allowed(project)
}

// get reads a release by id for a caller with method (NotFound when hidden).
func (s *Service) get(ctx context.Context, id, method string) (*store.ReleaseRecord, error) {
	pid, n, ok := parseID(id)
	if !ok {
		return nil, api.NotFound("release")
	}
	rec, err := store.Get(ctx, s.R.c.Control, store.ReleaseRecordKey(pid, n))
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("release")
	}
	if err != nil {
		return nil, err
	}
	allowed, err := project.Allowed(ctx, s.Scope, method)
	if err != nil {
		return nil, err
	}
	if !visibleTo(rec.State.Value(), rec.Public.Value(), pid, allowed) {
		return nil, api.NotFound("release")
	}
	return rec, nil
}

// GetRelease implements ReleaseService.
func (s *Service) GetRelease(ctx context.Context, req *connect.Request[releasev1.GetReleaseRequest]) (*connect.Response[releasev1.GetReleaseResponse], error) {
	rec, err := s.get(ctx, req.Msg.GetId(), methodGet)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&releasev1.GetReleaseResponse{Release: s.R.message(rec)}), nil
}

type listed struct {
	project string
	e       store.ReleaseEntry
}

// entries returns the release entries of one project or (projectID "") of the registry.
func (s *Releases) entries(ctx context.Context, projectID string) ([]listed, error) {
	var out []listed
	if projectID != "" {
		es, err := store.ProjectReleases(ctx, s.c.Control, projectID)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			out = append(out, listed{projectID, e})
		}
		return out, nil
	}
	n, err := store.ReleaseIndexSlots(ctx, s.c.Control)
	if err != nil {
		return nil, err
	}
	idx := store.ReleaseIndex(s.c.Control)
	for k := uint64(0); n > 0 && k <= idx.LastPage(n) && k < maxIndexPages; k++ {
		es, err := idx.Page(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.N != 0 {
				out = append(out, listed{e.Project, e.ReleaseEntry})
			}
		}
	}
	return out, nil
}

// newer orders releases newest version first (semantic versions, then text).
func newer(a, b store.ReleaseEntry) bool {
	va, oka := registry.ParseVersion(a.Version)
	vb, okb := registry.ParseVersion(b.Version)
	switch {
	case oka && okb && va.Compare(vb) != 0:
		return va.Compare(vb) > 0
	case oka != okb:
		return oka
	case a.Version != b.Version:
		return a.Version > b.Version
	}
	return a.Created.After(b.Created)
}

// ListReleases implements ReleaseService: newest version first.
func (s *Service) ListReleases(ctx context.Context, req *connect.Request[releasev1.ListReleasesRequest]) (*connect.Response[releasev1.ListReleasesResponse], error) {
	m := req.Msg
	size, err := api.PageSize(m.GetPageSize())
	if err != nil {
		return nil, err
	}
	scope := "releases/" + m.GetProjectId() + "/" + m.GetName()
	pos, err := api.DecodePageToken(scope, m.GetPageToken())
	if err != nil {
		return nil, err
	}
	if p := m.GetProjectId(); p != "" && !project.ValidID(p) {
		return nil, api.InvalidArgument("projectId", "invalid project id")
	}
	all, err := s.R.entries(ctx, m.GetProjectId())
	if err != nil {
		return nil, api.Unavailable("releases are not available, try again")
	}
	allowed, err := project.Allowed(ctx, s.Scope, methodList)
	if err != nil {
		return nil, err
	}
	var rows []listed
	for _, l := range all {
		if (m.GetName() == "" || l.e.Name == m.GetName()) && visibleTo(l.e.State, l.e.Public, l.project, allowed) {
			rows = append(rows, l)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].project != rows[j].project && m.GetProjectId() == "" && rows[i].e.Created != rows[j].e.Created {
			return rows[i].e.Created.After(rows[j].e.Created)
		}
		return newer(rows[i].e, rows[j].e)
	})
	out := &releasev1.ListReleasesResponse{}
	for i := int(min(pos, uint64(len(rows)))); i < len(rows) && len(out.Releases) < size; i++ {
		rec, err := store.Get(ctx, s.R.c.Control, store.ReleaseRecordKey(rows[i].project, rows[i].e.N))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, api.Unavailable("releases are not available, try again")
		}
		if !visibleTo(rec.State.Value(), rec.Public.Value(), rows[i].project, allowed) {
			continue
		}
		out.Releases = append(out.Releases, s.R.message(rec))
		if len(out.Releases) == size && i+1 < len(rows) {
			out.NextPageToken = api.EncodePageToken(scope, uint64(i+1))
		}
	}
	return connect.NewResponse(out), nil
}

// resolve picks the highest published release of project/name matching constraint that the
// caller of ctx may see (allowed = GetRelease on the project).
func (s *Releases) resolve(ctx context.Context, projectID, name, constraint string, allowed func(string) bool) (*store.ReleaseRecord, error) {
	es, err := store.ProjectReleases(ctx, s.c.Control, projectID)
	if err != nil {
		return nil, err
	}
	var best *store.ReleaseEntry
	var bestV registry.Version
	for i := range es {
		e := &es[i]
		if e.Name != name || e.State != store.ReleasePublished || !visibleTo(e.State, e.Public, projectID, allowed) {
			continue
		}
		v, ok := registry.ParseVersion(e.Version)
		if !ok || !registry.Matches(constraint, v) {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = e, v
		}
	}
	if best == nil {
		return nil, nil
	}
	rec, err := store.Get(ctx, s.c.Control, store.ReleaseRecordKey(projectID, best.N))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.State.Value() != store.ReleasePublished || !visibleTo(rec.State.Value(), rec.Public.Value(), projectID, allowed) {
		return nil, nil
	}
	return rec, nil
}

// ResolveRelease implements ReleaseService (bob install).
func (s *Service) ResolveRelease(ctx context.Context, req *connect.Request[releasev1.ResolveReleaseRequest]) (*connect.Response[releasev1.ResolveReleaseResponse], error) {
	m := req.Msg
	switch {
	case !project.ValidID(m.GetProjectId()):
		return nil, api.InvalidArgument("projectId", "invalid project id")
	case !nameRE.MatchString(m.GetName()):
		return nil, api.InvalidArgument("name", "invalid release name")
	case !registry.ValidConstraint(m.GetConstraint()):
		return nil, api.InvalidArgument("constraint", `constraint must be "", "<major>", "<major>.<minor>" or a full version`)
	}
	allowed, err := project.Allowed(ctx, s.Scope, methodGet)
	if err != nil {
		return nil, err
	}
	rec, err := s.R.resolve(ctx, m.GetProjectId(), m.GetName(), m.GetConstraint(), allowed)
	if err != nil {
		return nil, api.Unavailable("releases are not available, try again")
	}
	if rec == nil {
		return nil, api.NotFound("release")
	}
	out := &releasev1.ResolveReleaseResponse{Release: s.R.message(rec)}
	if p := m.GetPlatform(); p != "" {
		for _, c := range out.Release.Closures {
			if c.Platform == p {
				out.Closure = c
			}
		}
		if out.Closure == nil {
			return nil, api.NotFound("release " + m.GetProjectId() + "/" + m.GetName() + "@" + rec.Version.Value() + " for platform " + p)
		}
	}
	if s.R.c.BaseURL != "" {
		out.Substituters = []string{s.R.c.BaseURL + "/nix"}
	}
	if s.R.c.PublicKeys != nil {
		if keys, err := s.R.c.PublicKeys(ctx); err == nil {
			out.PublicKeys = keys
		}
	}
	return connect.NewResponse(out), nil
}

// UpdateRelease implements ReleaseService: visibility and description (the interceptor checked
// UpdateRelease on the release's project). The etag is optional (D-IB4-3); when given it must
// match.
func (s *Service) UpdateRelease(ctx context.Context, req *connect.Request[releasev1.UpdateReleaseRequest]) (*connect.Response[releasev1.UpdateReleaseResponse], error) {
	in := req.Msg.GetRelease()
	if err := api.CheckMask(req.Msg.GetUpdateMask(), in, "visibility", "description"); err != nil {
		return nil, err
	}
	pid, n, ok := parseID(in.GetId())
	if !ok {
		return nil, api.NotFound("release")
	}
	var setVis, setDesc bool
	for _, p := range req.Msg.GetUpdateMask().GetPaths() {
		setVis = setVis || p == "visibility"
		setDesc = setDesc || p == "description"
	}
	public := in.GetVisibility() == commonv1.Visibility_VISIBILITY_PUBLIC
	if setVis && in.GetVisibility() == commonv1.Visibility_VISIBILITY_UNSPECIFIED {
		return nil, api.InvalidArgument("release.visibility", "visibility must be PUBLIC or PRIVATE")
	}
	if setDesc && len(in.GetDescription()) > maxDesc {
		return nil, api.InvalidArgument("release.description", "description must be at most 10 KiB")
	}
	rec, err := store.CAS(ctx, s.R.c.Control, store.ReleaseRecordKey(pid, n), func(cur *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if cur == nil || cur.State.Value() == store.ReleaseDeleted {
			return nil, api.NotFound("release")
		}
		if e := in.GetEtag(); e != "" && e != store.ETagOf(cur.ETag) {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		next := *cur
		changed := false
		if setVis && cur.Public.Value() != public {
			next.Public, changed = store.Set(public), true
		}
		if setDesc && cur.Description.Value() != in.GetDescription() {
			next.Description, changed = store.Set(in.GetDescription()), true
		}
		if !changed {
			return nil, store.ErrNoChange
		}
		return &next, nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.NotFound("release")
	}
	if err != nil {
		return nil, err
	}
	if rec.State.Value() == store.ReleasePublished {
		// idempotent sweep: also repairs documents of an earlier sweep that did not finish
		if err := s.R.applyVisibility(ctx, rec, rec.Public.Value()); err != nil {
			return nil, failToAPI(err)
		}
	}
	if err := s.R.syncEntries(ctx, rec); err != nil {
		return nil, err
	}
	return connect.NewResponse(&releasev1.UpdateReleaseResponse{Release: s.R.message(rec)}), nil
}

// failToAPI maps a release failure of a visibility sweep to FailedPrecondition.
func failToAPI(err error) error {
	var f *buildctl.ReleaseFailure
	if errors.As(err, &f) {
		return api.FailedPrecondition(f.Msg)
	}
	return err
}

// DeleteRelease implements ReleaseService (the only way a release is removed, R-STORE-007):
// state deleted (tombstone; relv stays), release id out of every visibility document and
// repository index, assets deleted by known key. Repeating it finishes an interrupted delete.
func (s *Service) DeleteRelease(ctx context.Context, req *connect.Request[releasev1.DeleteReleaseRequest]) (*connect.Response[releasev1.DeleteReleaseResponse], error) {
	pid, n, ok := parseID(req.Msg.GetId())
	if !ok {
		return nil, api.NotFound("release")
	}
	rec, err := store.CAS(ctx, s.R.c.Control, store.ReleaseRecordKey(pid, n), func(cur *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if cur == nil {
			return nil, api.NotFound("release")
		}
		if e := req.Msg.GetEtag(); e != "" && e != store.ETagOf(cur.ETag) && cur.State.Value() != store.ReleaseDeleted {
			return nil, api.Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
		}
		switch cur.State.Value() {
		case store.ReleaseDeleted:
			return nil, store.ErrNoChange
		case store.ReleasePending, store.ReleasePublishing:
			return nil, api.FailedPrecondition("the release is being published; delete it when it is published or failed")
		}
		next := *cur
		next.State = store.Set(store.ReleaseDeleted)
		return &next, nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.R.remove(ctx, rec); err != nil {
		return nil, err
	}
	return connect.NewResponse(&releasev1.DeleteReleaseResponse{}), nil
}

// remove undoes what publishing did for a deleted release.
func (s *Releases) remove(ctx context.Context, rec *store.ReleaseRecord) error {
	if err := s.applyVisibility(ctx, rec, false); err != nil {
		return err
	}
	id := ID(rec.Project.Value(), numOf(rec))
	var repos []string
	for _, im := range rec.Images.Value() {
		if !slices.Contains(repos, im.Repo) {
			repos = append(repos, im.Repo)
		}
	}
	for _, r := range repos {
		if err := s.c.Repos.RemoveRelease(ctx, r, id); err != nil {
			return err
		}
	}
	for _, a := range rec.Assets.Value() {
		if err := s.c.Objects.Delete(ctx, assetKey(rec.Project.Value(), numOf(rec), a.File)); err != nil {
			return err
		}
	}
	return s.syncEntries(ctx, rec)
}
