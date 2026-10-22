package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	buildctl "github.com/megakuul/bob/internal/build"
	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/store"
)

// Config configures the release store.
type Config struct {
	Control *store.Bucket
	Objects *store.Objects  // artifacts bucket
	NixVis  *cache.VisStore // cache.NixVisPrefix
	OCIVis  *cache.VisStore // cache.OCIVisPrefix
	Repos   registry.Repos
	BaseURL string
	// PublicKeys are the cache's trusted keys (ResolveRelease).
	PublicKeys func(ctx context.Context) ([]string, error)
	// Published sends release.published notifications (may be nil).
	Published func(ctx context.Context, project string, release proto.Message) error
	Now       func() time.Time
	Log       *slog.Logger
}

// Releases publishes releases from release pipelines (SPEC §6.8, the build controller's
// Releaser) and serves ReleaseService and the /r/ downloads. Every step is idempotent and
// every write conditional: a crash anywhere is finished by the next controller pass.
type Releases struct {
	c Config
}

// New returns the release store; it performs no I/O.
func New(c Config) (*Releases, error) {
	if c.Control == nil || c.Objects == nil || c.NixVis == nil || c.OCIVis == nil {
		return nil, errors.New("release: control bucket, artifacts bucket and visibility documents are required")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return &Releases{c: c}, nil
}

var _ buildctl.Releaser = (*Releases)(nil)

func failf(format string, args ...any) error {
	return &buildctl.ReleaseFailure{Msg: fmt.Sprintf(format, args...)}
}

// ID is the API id of release n: <projectID>.<num10>.
func ID(project string, n uint64) string {
	id, _ := store.ReleaseID(project, n)
	return id
}

// parseID splits a release id.
func parseID(id string) (string, uint64, bool) {
	project, num, ok := strings.Cut(id, ".")
	if !ok || len(num) != 10 || project == "" {
		return "", 0, false
	}
	var n uint64
	for _, c := range num {
		if c < '0' || c > '9' {
			return "", 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	if _, err := store.Segment(project); err != nil || n == 0 {
		return "", 0, false
	}
	return project, n, true
}

func assetKey(project string, n uint64, file string) string {
	num, _ := store.Num10(n)
	return "rel/" + project + "/" + num + "/" + file
}

// Publish implements buildctl.Releaser (§6.8 steps 1–5).
func (s *Releases) Publish(ctx context.Context, r buildctl.ReleaseRequest) (string, error) {
	rec := r.Build
	project, build := rec.Project.Value(), rec.ID.Value()
	m, manifestPath, err := s.manifest(ctx, project, r)
	if err != nil {
		return "", err
	}
	if r.Tag != "" {
		if want := strings.TrimPrefix(r.Tag, "v"); m.Version != want {
			return "", failf("release %s was built as version %s, but the tag %s names %s", r.Label, m.Version, r.Tag, want)
		}
	}
	key, err := store.ReleaseVersionKey(project, m.Release, m.Version)
	if err != nil {
		return "", failf("release %s/%s@%s: %v", project, m.Release, m.Version, err)
	}
	exists := failf("release %s/%s@%s already exists; releases are immutable", project, m.Release, m.Version)
	n, err := s.number(ctx, key, project, build, exists)
	if err != nil {
		return "", err
	}
	id := ID(project, n)
	cur, err := store.Get(ctx, s.c.Control, store.ReleaseRecordKey(project, n))
	if errors.Is(err, store.ErrNotFound) {
		cur, err = s.create(ctx, r, m, manifestPath, n)
	}
	if err != nil {
		return "", s.failed(ctx, project, n, err)
	}
	switch cur.State.Value() {
	case store.ReleasePublished:
		return id, nil
	case store.ReleaseDeleted:
		return "", exists
	case store.ReleaseFailed:
		return "", failf("%s", cur.Error.Value())
	}
	if cur.Build.Value() != build {
		return "", exists
	}
	if err := s.publish(ctx, cur); err != nil {
		return "", s.failed(ctx, project, n, err)
	}
	return id, nil
}

// manifest reads and verifies release.json from the release label's node outputs.
func (s *Releases) manifest(ctx context.Context, project string, r buildctl.ReleaseRequest) (*Manifest, string, error) {
	nodes := slices.Clone(r.Nodes)
	// the entry's own label first, then by platform: a node of the same name that is not
	// the release (e.g. the binary //cmd/api:api) is tried last
	sort.SliceStable(nodes, func(i, j int) bool {
		if (nodes[i].Label == r.Label) != (nodes[j].Label == r.Label) {
			return nodes[i].Label == r.Label
		}
		return nodes[i].Platform < nodes[j].Platform
	})
	var firstErr error
	for _, n := range nodes {
		for _, out := range n.Outputs {
			b, err := s.readManifest(ctx, project, out)
			if err == nil {
				m, perr := ParseManifest(b, project, r.Label)
				if perr == nil {
					return m, out, nil
				}
				err = failf("release %s: %v", r.Label, perr)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr == nil {
		firstErr = failf("release %s was not built", r.Label)
	}
	return nil, "", firstErr
}

// number returns the release number of (project, name, version) for build: relv create-only
// (another build's relv = exists), then pctr.r+1 recorded in relv once.
func (s *Releases) number(ctx context.Context, key *store.ReleaseVersion, project, build string, exists error) (uint64, error) {
	v, err := store.Get(ctx, s.c.Control, key)
	if errors.Is(err, store.ErrNotFound) {
		err = store.Create(ctx, s.c.Control, &store.ReleaseVersion{Scope: key.Scope, Version: key.Version, Build: store.Set(build)})
		if err != nil && !errors.Is(err, store.ErrExists) {
			return 0, err
		}
		v, err = store.Get(ctx, s.c.Control, key)
	}
	if err != nil {
		return 0, err
	}
	if v.Build.Value() != build {
		return 0, exists
	}
	if n := v.N.Value(); n != 0 {
		return n, nil
	}
	ctr, err := store.CAS(ctx, s.c.Control, &store.ProjectCounters{Project: store.ID(project)}, func(cur *store.ProjectCounters) (*store.ProjectCounters, error) {
		next, n := store.ProjectCounters{Project: store.ID(project)}, uint64(0)
		if cur != nil {
			next, n = *cur, cur.Releases.Value()
		}
		next.Releases = store.Set(n + 1)
		return &next, nil
	})
	if err != nil {
		return 0, fmt.Errorf("pctr/%s: %w", project, err)
	}
	taken := ctr.Releases.Value()
	got, err := store.CAS(ctx, s.c.Control, key, func(cur *store.ReleaseVersion) (*store.ReleaseVersion, error) {
		if cur == nil || cur.Build.Value() != build || cur.N.Value() != 0 {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.N = store.Set(taken)
		return &next, nil
	})
	if err != nil {
		return 0, err
	}
	if got.Build.Value() != build {
		return 0, exists
	}
	return got.N.Value(), nil
}

// create gathers what the release consists of (closures, assets, images, the visibility
// objects) and creates its record in pending, then its rpage entry.
func (s *Releases) create(ctx context.Context, r buildctl.ReleaseRequest, m *Manifest, manifestPath string, n uint64) (*store.ReleaseRecord, error) {
	rec := r.Build
	project := rec.Project.Value()
	closures := map[string][]string{}
	var assets []store.ReleaseAsset
	var images []store.ReleaseImage
	roots := []string{manifestPath}
	for _, a := range m.Artifacts {
		roots = append(roots, a.StorePath)
		switch a.Kind {
		case "closure":
			closures[a.Platform] = append(closures[a.Platform], a.StorePath)
		case "appimage", "tarball":
			assets = append(assets, store.ReleaseAsset{File: a.File, Kind: a.Kind, Platform: a.Platform, Size: a.Size, SHA256: a.SHA256})
		case "oci":
			media, err := s.servedImage(ctx, project, a.Index)
			if err != nil {
				return nil, err
			}
			images = append(images, store.ReleaseImage{Repo: a.Repo, Tag: m.Version, Digest: a.Index, MediaType: media})
		}
	}
	paths, sizes, err := s.closure(ctx, roots)
	if err != nil {
		return nil, err
	}
	var cl []store.ReleaseClosure
	for _, p := range slices.Sorted(maps.Keys(closures)) {
		c := store.ReleaseClosure{Platform: p, Roots: closures[p]}
		sub, _, err := s.closure(ctx, closures[p])
		if err != nil {
			return nil, err
		}
		for _, h := range sub {
			c.NarBytes += sizes[h]
		}
		cl = append(cl, c)
	}
	var blobs []string
	for _, im := range images {
		b, err := s.imageObjects(ctx, im.Digest)
		if err != nil {
			return nil, err
		}
		blobs = append(blobs, b...)
	}
	slices.Sort(blobs)
	blobs = slices.Compact(blobs)
	staged, err := s.stagedAssets(ctx, rec, project, n, assets)
	if err != nil {
		return nil, err
	}
	now := s.c.Now()
	num, _ := store.Num10(n)
	v := &store.ReleaseRecord{Project: store.ID(project), N: store.ID(num),
		Name: store.Set(m.Release), Version: store.Set(m.Version), Label: store.Set(m.Label),
		State: store.Set(store.ReleasePending), Build: store.Set(rec.ID.Value()), Pipeline: store.Set(r.Pipeline),
		SHA: store.Set(rec.Commit.Value()), Manifest: store.Set(manifestPath), Created: store.Set(now)}
	if m.Public {
		v.Public = store.Set(true)
	}
	if len(cl) > 0 {
		v.Closures = store.Set(cl)
	}
	if len(assets) > 0 {
		v.Assets = store.Set(assets)
	}
	if len(images) > 0 {
		v.Images = store.Set(images)
	}
	if len(paths) > 0 {
		v.Paths = store.Set(paths)
	}
	if len(blobs) > 0 {
		v.Blobs = store.Set(blobs)
	}
	if len(staged) > 0 {
		v.Staged = store.Set(staged)
	}
	if err := store.Create(ctx, s.c.Control, v); err != nil && !errors.Is(err, store.ErrExists) {
		return nil, err
	}
	cur, err := store.Get(ctx, s.c.Control, store.ReleaseRecordKey(project, n))
	if err != nil {
		return nil, err
	}
	// the ridx slot right away: Retained (cache GC) finds every release through ridx
	if cur, err = s.indexSlot(ctx, cur); err != nil {
		return nil, err
	}
	if err := s.syncEntries(ctx, cur); err != nil {
		return nil, err
	}
	return cur, nil
}

// servedImage checks that an image index was promoted for project and returns its media type.
func (s *Releases) servedImage(ctx context.Context, project, digest string) (string, error) {
	doc, err := s.c.OCIVis.Get(ctx, strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		return "", err
	}
	if doc == nil || !slices.Contains(doc.Projects, project) {
		return "", failf("release image %s is not in the registry (promotion incomplete)", digest)
	}
	body, _, err := s.c.Objects.Read(ctx, registry.BlobKey(digest), 4<<20)
	if errors.Is(err, store.ErrObjectNotFound) {
		return "", failf("release image %s is not in the registry (promotion incomplete)", digest)
	}
	if err != nil {
		return "", err
	}
	return registry.MediaTypeOf(body), nil
}

// imageObjects returns the hex digests of an image's objects: index, manifests, configs and
// layers (served manifests, bounded).
func (s *Releases) imageObjects(ctx context.Context, digest string) ([]string, error) {
	var out []string
	var walk func(d string, depth int) error
	walk = func(d string, depth int) error {
		out = append(out, strings.TrimPrefix(d, "sha256:"))
		if depth > 2 || len(out) > 4096 {
			return failf("release image %s has too many objects", digest)
		}
		body, _, err := s.c.Objects.Read(ctx, registry.BlobKey(d), 4<<20)
		if errors.Is(err, store.ErrObjectNotFound) {
			return failf("release image object %s is not in the registry", d)
		}
		if err != nil {
			return err
		}
		var m struct {
			Manifests []struct{ Digest string } `json:"manifests"`
			Config    *struct{ Digest string }  `json:"config"`
			Layers    []struct{ Digest string } `json:"layers"`
		}
		if json.Unmarshal(body, &m) != nil {
			return failf("release image object %s is not a manifest", d)
		}
		for _, c := range m.Manifests {
			if registry.ValidDigest(c.Digest) {
				if err := walk(c.Digest, depth+1); err != nil {
					return err
				}
			}
		}
		if m.Config != nil && registry.ValidDigest(m.Config.Digest) {
			out = append(out, strings.TrimPrefix(m.Config.Digest, "sha256:"))
		}
		for _, l := range m.Layers {
			if registry.ValidDigest(l.Digest) {
				out = append(out, strings.TrimPrefix(l.Digest, "sha256:"))
			}
		}
		return nil
	}
	if err := walk(digest, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// closure walks the served narinfos from roots: the bob-served part of the closure (paths
// served upstream have no narinfo here and are skipped, R-REG-005). sizes are NarSize.
func (s *Releases) closure(ctx context.Context, roots []string) ([]string, map[string]int64, error) {
	seen := map[string]bool{}
	sizes := map[string]int64{}
	var queue []string
	for _, r := range roots {
		if cache.ValidStorePath(r) && !seen[cache.HashOf(r)] {
			seen[cache.HashOf(r)] = true
			queue = append(queue, cache.HashOf(r))
		}
	}
	var out []string
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		body, _, err := s.c.Objects.Read(ctx, cache.NarInfoKey(h), cache.MaxNarInfo)
		if errors.Is(err, store.ErrObjectNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ni, err := cache.ParseNarInfo(body)
		if err != nil {
			return nil, nil, fmt.Errorf("narinfo %s: %w", h, err)
		}
		out = append(out, h)
		sizes[h] = int64(ni.NarSize)
		if len(out) > store.MaxReleasePaths {
			return nil, nil, failf("release closure has more than %d bob-built paths", store.MaxReleasePaths)
		}
		for _, ref := range ni.References {
			rh := cache.HashOf(ref)
			if rh != "" && !seen[rh] {
				seen[rh] = true
				queue = append(queue, rh)
			}
		}
	}
	slices.Sort(out)
	return out, sizes, nil
}

// stagedAssets finds the staged copy of every asset among the build's succeeded jobs (the
// staging keys, then the COMPLETE.json files of those jobs). An asset that is neither staged
// nor copied already is lost (staging expired).
func (s *Releases) stagedAssets(ctx context.Context, rec *store.BuildRecord, project string, n uint64, assets []store.ReleaseAsset) ([]string, error) {
	var keys, completes []string
	for _, a := range assets {
		if _, err := s.c.Objects.Head(ctx, assetKey(project, n, a.File)); err == nil {
			continue
		} else if !errors.Is(err, store.ErrObjectNotFound) {
			return nil, err
		}
		found := ""
		for _, row := range rec.Jobs.Value() {
			if row.State != orchestrator.StateSucceeded {
				continue
			}
			prefix := jobspec.StagingPrefix("staging/"+rec.ID.Value()+"/"+row.ID+"/", row.Attempts)
			k := prefix + "assets/" + a.File
			if _, err := s.c.Objects.Head(ctx, k); err == nil {
				found = k
				if c := prefix + "COMPLETE.json"; !slices.Contains(completes, c) {
					completes = append(completes, c)
				}
				break
			} else if !errors.Is(err, store.ErrObjectNotFound) {
				return nil, err
			}
		}
		if found == "" {
			return nil, failf("release asset %s expired; re-run the release pipeline", a.File)
		}
		keys = append(keys, found)
	}
	return append(keys, completes...), nil
}

// failed records a release failure in the record (state failed, error text) and returns err.
func (s *Releases) failed(ctx context.Context, project string, n uint64, err error) error {
	var rf *buildctl.ReleaseFailure
	if !errors.As(err, &rf) {
		return err
	}
	cur, cerr := store.CAS(ctx, s.c.Control, store.ReleaseRecordKey(project, n), func(cur *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if cur == nil || cur.State.Value() == store.ReleasePublished || cur.State.Value() == store.ReleaseDeleted || cur.State.Value() == store.ReleaseFailed {
			return nil, store.ErrNoChange
		}
		next := *cur
		next.State, next.Error = store.Set(store.ReleaseFailed), store.Set(rf.Msg)
		return &next, nil
	})
	if cerr == nil && cur != nil {
		if serr := s.syncEntries(ctx, cur); serr != nil {
			s.c.Log.Warn("release: index entries", "release", ID(project, n), "err", serr.Error())
		}
	}
	return err
}

// setState CASes the record's state from one of from to to.
func (s *Releases) setState(ctx context.Context, cur *store.ReleaseRecord, to string, from ...string) (*store.ReleaseRecord, error) {
	return store.CAS(ctx, s.c.Control, &store.ReleaseRecord{Project: cur.Project, N: cur.N}, func(c *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if c == nil {
			return nil, store.ErrNotFound
		}
		if !slices.Contains(from, c.State.Value()) {
			return nil, store.ErrNoChange
		}
		next := *c
		next.State = store.Set(to)
		return &next, nil
	})
}

// publish runs §6.8 steps 2–5 for a pending or publishing release.
func (s *Releases) publish(ctx context.Context, cur *store.ReleaseRecord) error {
	project := cur.Project.Value()
	n := numOf(cur)
	id := ID(project, n)
	cur, err := s.setState(ctx, cur, store.ReleasePublishing, store.ReleasePending)
	if err != nil {
		return err
	}
	if err := s.copyAssets(ctx, cur); err != nil {
		return err
	}
	if cur.Public.Value() {
		if err := s.applyVisibility(ctx, cur, true); err != nil {
			return err
		}
	}
	for _, im := range cur.Images.Value() {
		err := s.c.Repos.SetReleaseTag(ctx, im.Repo, im.Tag, im.Digest, im.MediaType, id, cur.Created.Value().Unix())
		if errors.Is(err, registry.ErrTagExists) {
			return failf("image tag %s:%s exists already", im.Repo, im.Tag)
		}
		if err != nil {
			return err
		}
	}
	if cur, err = s.indexSlot(ctx, cur); err != nil { // records created before a crash may lack it
		return err
	}
	moved := false
	next, err := store.CAS(ctx, s.c.Control, &store.ReleaseRecord{Project: cur.Project, N: cur.N}, func(c *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if c == nil {
			return nil, store.ErrNotFound
		}
		if c.State.Value() != store.ReleasePublishing {
			moved = false
			return nil, store.ErrNoChange
		}
		moved = true
		r := *c
		r.State, r.Staged = store.Set(store.ReleasePublished), nil
		return &r, nil
	})
	if err != nil {
		return err
	}
	if err := s.syncEntries(ctx, next); err != nil {
		return err
	}
	if moved {
		s.cleanStaging(ctx, cur.Staged.Value())
		s.c.Log.Info("release published", "release", id, "name", next.Name.Value(), "version", next.Version.Value())
		if s.c.Published != nil {
			if err := s.c.Published(ctx, project, s.message(next)); err != nil {
				s.c.Log.Warn("release: release.published notification", "release", id, "err", err.Error())
			}
		}
	}
	return nil
}

func numOf(r *store.ReleaseRecord) uint64 {
	var n uint64
	for _, c := range r.N.Value() {
		n = n*10 + uint64(c-'0')
	}
	return n
}

// copyAssets copies every asset from staging to rel/<project>/<num10>/<file> (sha256 and size
// verified while streaming, ETag guard, create-only).
func (s *Releases) copyAssets(ctx context.Context, cur *store.ReleaseRecord) error {
	project, n := cur.Project.Value(), numOf(cur)
	staged := cur.Staged.Value()
	for _, a := range cur.Assets.Value() {
		dst := assetKey(project, n, a.File)
		if inf, err := s.c.Objects.Head(ctx, dst); err == nil {
			if inf.Size != a.Size {
				return failf("release asset %s exists with another size", a.File)
			}
			continue
		} else if !errors.Is(err, store.ErrObjectNotFound) {
			return err
		}
		src := ""
		for _, k := range staged {
			if strings.HasSuffix(k, "/assets/"+a.File) {
				src = k
			}
		}
		if src == "" {
			return failf("release asset %s expired; re-run the release pipeline", a.File)
		}
		o, err := s.c.Objects.Get(ctx, src, "", "")
		if errors.Is(err, store.ErrObjectNotFound) {
			return failf("release asset %s expired; re-run the release pipeline", a.File)
		}
		if err != nil {
			return err
		}
		h := sha256.New()
		size, err := io.Copy(h, o.Body)
		_ = o.Body.Close()
		if err != nil {
			return fmt.Errorf("release: read %s: %w", src, err)
		}
		if hex.EncodeToString(h.Sum(nil)) != a.SHA256 || size != a.Size {
			return failf("release asset %s does not match the release manifest", a.File)
		}
		err = s.c.Objects.Copy(ctx, src, o.ETag, dst, map[string]string{"bob-sha256": a.SHA256}, "")
		if errors.Is(err, store.ErrPrecondition) {
			if _, herr := s.c.Objects.Head(ctx, dst); herr == nil {
				continue // a concurrent copy won
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// cleanStaging deletes the staged assets and their jobs' COMPLETE.json by known key once the
// release copied them (R-STORE-105); a failure leaves them to the lifecycle rule.
func (s *Releases) cleanStaging(ctx context.Context, keys []string) {
	for _, k := range keys {
		if err := s.c.Objects.Delete(ctx, k); err != nil {
			s.c.Log.Warn("release: staged object not deleted (expires with the lifecycle rule)", "key", k, "err", err.Error())
		}
	}
}

// applyVisibility adds (public) or removes the release id in the public list of every
// visibility document of the release's served paths and image objects (idempotent sweep).
func (s *Releases) applyVisibility(ctx context.Context, cur *store.ReleaseRecord, public bool) error {
	id := ID(cur.Project.Value(), numOf(cur))
	for _, h := range cur.Paths.Value() {
		if err := s.c.NixVis.SetPublic(ctx, h, id, public); err != nil {
			if errors.Is(err, cache.ErrTooManyReleases) {
				return failf("too many releases share %s", h)
			}
			return err
		}
	}
	for _, d := range cur.Blobs.Value() {
		if err := s.c.OCIVis.SetPublic(ctx, d, id, public); err != nil {
			if errors.Is(err, cache.ErrTooManyReleases) {
				return failf("too many releases share sha256:%s", d)
			}
			return err
		}
	}
	return nil
}

// indexSlot gives the release its registry-wide index slot once (ridxm + record).
func (s *Releases) indexSlot(ctx context.Context, cur *store.ReleaseRecord) (*store.ReleaseRecord, error) {
	if cur.Slot.Value() != 0 {
		return cur, nil
	}
	slot, err := store.TakeReleaseIndexSlot(ctx, s.c.Control)
	if err != nil {
		return nil, err
	}
	return store.CAS(ctx, s.c.Control, &store.ReleaseRecord{Project: cur.Project, N: cur.N}, func(c *store.ReleaseRecord) (*store.ReleaseRecord, error) {
		if c == nil {
			return nil, store.ErrNotFound
		}
		if c.Slot.Value() != 0 {
			return nil, store.ErrNoChange // a concurrent pass took one; ours stays empty
		}
		next := *c
		next.Slot = store.Set(slot)
		return &next, nil
	})
}

// entry is the rpage/ridx entry of a record.
func entry(r *store.ReleaseRecord) store.ReleaseEntry {
	e := store.ReleaseEntry{N: numOf(r), Name: r.Name.Value(), Version: r.Version.Value(), State: r.State.Value(),
		Public: r.Public.Value(), Created: r.Created.Value()}
	for _, im := range r.Images.Value() {
		if !slices.Contains(e.Repos, im.Repo) {
			e.Repos = append(e.Repos, im.Repo)
		}
	}
	return e
}

// syncEntries writes the record's rpage entry and, once it has a slot, its ridx entry.
func (s *Releases) syncEntries(ctx context.Context, r *store.ReleaseRecord) error {
	e := entry(r)
	if err := store.ReleasePages(s.c.Control, r.Project.Value()).Set(ctx, e.N-1, e); err != nil {
		return fmt.Errorf("rpage: %w", err)
	}
	if slot := r.Slot.Value(); slot > 0 {
		if err := store.ReleaseIndex(s.c.Control).Set(ctx, slot-1, store.ReleaseIndexEntry{Project: r.Project.Value(), ReleaseEntry: e}); err != nil {
			return fmt.Errorf("ridx: %w", err)
		}
	}
	return nil
}
