package release

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/project"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/store"
)

// Downloads serves GET/HEAD /r/<project>/<name>/<version>/<file> (SPEC §6.8): public
// releases anonymously, private ones with ReleaseService/GetRelease on the project (Basic
// with any user name or Bearer); everything else 404. /latest/<file> redirects (302) to the
// highest published version that has the file.
type Downloads struct {
	R *Releases
	// Principal authenticates the request ((nil, nil) = anonymous; an error = invalid).
	Principal func(*http.Request) (*api.Principal, error)
	Scope     project.Scoper
	slots     chan struct{}
}

// NewDownloads returns the /r/ handler (max parallel downloads as for the cache).
func NewDownloads(r *Releases, principal func(*http.Request) (*api.Principal, error), scope project.Scoper, maxDownloads int) *Downloads {
	if maxDownloads <= 0 {
		maxDownloads = 256
	}
	return &Downloads{R: r, Principal: principal, Scope: scope, slots: make(chan struct{}, maxDownloads)}
}

func (d *Downloads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/r/"), "/")
	if len(parts) != 4 || !project.ValidID(parts[0]) || !nameRE.MatchString(parts[1]) || !fileRE.MatchString(parts[3]) ||
		parts[2] == "" || len(parts[2]) > 128 {
		http.NotFound(w, r)
		return
	}
	pid, name, version, file := parts[0], parts[1], parts[2], parts[3]
	p, err := d.Principal(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
		http.Error(w, "unauthorized: token invalid or expired (run 'bob login')", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	allowed, err := allowedFor(ctx, d.Scope, p, methodGet)
	if err != nil {
		unavailable(w)
		return
	}
	if version == "latest" {
		d.latest(w, r, pid, name, file, allowed)
		return
	}
	rec, err := d.R.byVersion(ctx, pid, name, version)
	if err != nil {
		unavailable(w)
		return
	}
	if rec == nil || rec.State.Value() != store.ReleasePublished || !visibleTo(rec.State.Value(), rec.Public.Value(), pid, allowed) {
		http.NotFound(w, r)
		return
	}
	var asset *store.ReleaseAsset
	for _, a := range rec.Assets.Value() {
		if a.File == file {
			asset = &a
		}
	}
	if asset == nil {
		http.NotFound(w, r)
		return
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many parallel downloads, try again", http.StatusServiceUnavailable)
		return
	}
	scope := "private"
	if rec.Public.Value() {
		scope = "public"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	w.Header().Set("ETag", `"`+asset.SHA256+`"`)
	w.Header().Set("Cache-Control", scope+", max-age=31536000, immutable")
	registry.Stream(w, r, d.R.c.Objects, assetKey(pid, numOf(rec), file), "application/octet-stream", d.R.c.Log, func() {
		http.NotFound(w, r)
	})
}

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "storage unavailable, try again", http.StatusServiceUnavailable)
}

// byVersion reads release name@version of a project through relv (nil when absent).
func (s *Releases) byVersion(ctx context.Context, pid, name, version string) (*store.ReleaseRecord, error) {
	key, err := store.ReleaseVersionKey(pid, name, version)
	if err != nil {
		return nil, nil
	}
	v, err := store.Get(ctx, s.c.Control, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.N.Value() == 0 {
		return nil, nil
	}
	rec, err := store.Get(ctx, s.c.Control, store.ReleaseRecordKey(pid, v.N.Value()))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return rec, err
}

// latest redirects to the highest visible published release (no pre-release) with file.
func (d *Downloads) latest(w http.ResponseWriter, r *http.Request, pid, name, file string, allowed func(string) bool) {
	es, err := store.ProjectReleases(r.Context(), d.R.c.Control, pid)
	if err != nil {
		unavailable(w)
		return
	}
	var best *store.ReleaseEntry
	var bestV registry.Version
	for i := range es {
		e := &es[i]
		v, ok := registry.ParseVersion(e.Version)
		if e.Name != name || !ok || v.Pre != "" || e.State != store.ReleasePublished || !visibleTo(e.State, e.Public, pid, allowed) {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			rec, err := store.Get(r.Context(), d.R.c.Control, store.ReleaseRecordKey(pid, e.N))
			if err != nil {
				continue
			}
			has := false
			for _, a := range rec.Assets.Value() {
				has = has || a.File == file
			}
			if has {
				best, bestV = e, v
			}
		}
	}
	if best == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.Redirect(w, r, "/r/"+pid+"/"+name+"/"+url.PathEscape(best.Version)+"/"+file, http.StatusFound)
}

// allowedFor is project.Allowed for a principal authenticated outside the API (downloads).
func allowedFor(ctx context.Context, sc project.Scoper, p *api.Principal, method string) (func(string) bool, error) {
	switch {
	case p == nil:
		return func(string) bool { return false }, nil
	case sc == nil:
		return func(pid string) bool {
			a, _ := api.AdminBindings{}.Access(ctx, p, method, pid)
			return a == api.Allowed
		}, nil
	}
	s, err := sc.Scope(ctx, p, method)
	if err != nil {
		return nil, err
	}
	return s.Contains, nil
}
