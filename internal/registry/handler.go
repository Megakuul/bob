package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/store"
)

// GetRepositoryMethod is the permission that makes private OCI objects visible (§6.6.2).
const GetRepositoryMethod = "bob.registry.v1.RegistryService/GetRepository"

const (
	copyBuffer          = 256 << 10
	clientIdle          = 60 * time.Second
	defaultMaxDownloads = 256
	maxScopes           = 32
	// maxVisibleChecks bounds the visibility documents the token endpoint reads per
	// repository to find one visible object.
	maxVisibleChecks = 64
	mediaIndex       = "application/vnd.oci.image.index.v1+json"
	mediaManifest    = "application/vnd.oci.image.manifest.v1+json"
)

// Scoper returns the projects where a principal holds a method (rbac.Authorizer).
type Scoper interface {
	Scope(ctx context.Context, p *api.Principal, method string) (rbac.Scope, error)
}

// Config configures the registry.
type Config struct {
	Control *store.Bucket
	Objects *store.Objects
	Vis     *cache.VisStore // cache.OCIVisPrefix
	// Principal authenticates the Basic/Bearer credential of the token endpoint ((nil, nil)
	// = anonymous; an error = invalid credentials).
	Principal    func(*http.Request) (*api.Principal, error)
	Scope        Scoper
	BaseURL      string // the token realm is <BaseURL>/oci/token ("" = from the request)
	MaxDownloads int
	Now          func() time.Time
	Log          *slog.Logger
}

// Registry is the OCI pull registry (SPEC §6.7, R-REG-003): /v2/ and /oci/token. Every byte
// streams from the artifacts bucket through this process (D61); only promotion writes.
type Registry struct {
	c     Config
	Keys  *Keys
	Repos Repos
	slots chan struct{}
}

// New returns the registry; it performs no I/O.
func New(c Config) *Registry {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.MaxDownloads <= 0 {
		c.MaxDownloads = defaultMaxDownloads
	}
	return &Registry{c: c, Keys: &Keys{Control: c.Control, Now: c.Now}, Repos: Repos{Objects: c.Objects},
		slots: make(chan struct{}, c.MaxDownloads)}
}

type ociError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string][]ociError{"errors": {{Code: code, Message: msg}}})
}

func (g *Registry) realm(r *http.Request) string {
	base := strings.TrimRight(g.c.BaseURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/oci/token"
}

// challenge is the 401 of /v2/: Bearer realm, service and, for a repository, the scope. The
// challenge carries no error="insufficient_scope": with it, the distribution clients (podman,
// skopeo) report "requested access to the resource is denied" instead of the body's
// UNAUTHORIZED code, which R-REG-003 asks for (D-IB18-3).
func (g *Registry) challenge(w http.ResponseWriter, r *http.Request, repo string) {
	c := `Bearer realm="` + g.realm(r) + `",service="bob"`
	if repo != "" {
		c += `,scope="repository:` + repo + `:pull"`
	}
	w.Header().Set("WWW-Authenticate", c)
	writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

func (g *Registry) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() == nil {
		g.c.Log.Warn("registry: storage error", "path", r.URL.Path, "err", err.Error())
	}
	w.Header().Set("Retry-After", "1")
	writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable, try again")
}

// bearer returns the verified claims of the request's Bearer token (nil: none or invalid).
func (g *Registry) bearer(r *http.Request) *Claims {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil
	}
	c, err := g.Keys.Verify(r.Context(), strings.TrimSpace(tok))
	if err != nil {
		return nil
	}
	return c
}

// ServeHTTP serves /v2/ (pull only).
func (g *Registry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErr(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "this registry is pull-only")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/")
	if !ok {
		writeErr(w, http.StatusNotFound, "NAME_UNKNOWN", "not found")
		return
	}
	claims := g.bearer(r)
	if rest == "" {
		if claims == nil {
			g.challenge(w, r, "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
		return
	}
	repo, kind, ref, ok := route(rest)
	if !ok {
		writeErr(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	if !claims.CanPull(repo) {
		g.challenge(w, r, repo)
		return
	}
	switch kind {
	case "manifests":
		g.manifest(w, r, claims, repo, ref)
	case "blobs":
		g.blob(w, r, claims, ref)
	case "tags":
		g.tags(w, r, claims, repo)
	case "referrers":
		w.Header().Set("Content-Type", mediaIndex)
		_, _ = io.WriteString(w, `{"schemaVersion":2,"mediaType":"`+mediaIndex+`","manifests":[]}`)
	}
}

// route splits "<repo>/manifests/<ref>", "<repo>/blobs/<digest>", "<repo>/tags/list",
// "<repo>/referrers/<digest>".
func route(rest string) (repo, kind, ref string, ok bool) {
	if r, found := strings.CutSuffix(rest, "/tags/list"); found {
		return r, "tags", "", ValidRepo(r)
	}
	for _, k := range []string{"manifests", "blobs", "referrers"} {
		i := strings.LastIndex(rest, "/"+k+"/")
		if i <= 0 {
			continue
		}
		repo, ref = rest[:i], rest[i+len(k)+2:]
		if !ValidRepo(repo) || ref == "" || strings.Contains(ref, "/") {
			return "", "", "", false
		}
		if k != "manifests" && !ValidDigest(ref) {
			return "", "", "", false
		}
		if k == "manifests" && !ValidDigest(ref) && !ValidTag(ref) {
			return "", "", "", false
		}
		return repo, k, ref, true
	}
	return "", "", "", false
}

// visible decides from the object's visibility document alone (§6.6.2); it is the rule for
// digest references (manifests and blobs are content-addressed and shared across projects).
func (g *Registry) visible(ctx context.Context, c *Claims, digest string) (bool, error) {
	doc, err := g.c.Vis.Get(ctx, strings.TrimPrefix(digest, "sha256:"))
	if err != nil || doc == nil {
		return false, err
	}
	return len(doc.Public) > 0 || c.sees(doc.Projects), nil
}

// tagVisible is the rule for a tag of repo (A151): the object's visibility document must
// exist, and the bearer sees the repository's project, or the tag is a release tag whose
// release made the object public. Two projects that build the same image share its digest
// and so its document; another project's public release must not expose this project's
// private or build tags.
func (g *Registry) tagVisible(ctx context.Context, repo string, e TagEntry, sees func([]string) bool) (vis, public bool, err error) {
	doc, err := g.c.Vis.Get(ctx, strings.TrimPrefix(e.D, "sha256:"))
	if err != nil || doc == nil {
		return false, false, err
	}
	public = e.R != "" && slices.Contains(doc.Public, e.R)
	return public || sees([]string{RepoProject(repo)}), public, nil
}

func cacheControl(w http.ResponseWriter, public bool, immutable bool) {
	scope := "private"
	if public {
		scope = "public"
	}
	if immutable {
		w.Header().Set("Cache-Control", scope+", max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", scope+", max-age=60")
}

func (g *Registry) isPublic(ctx context.Context, digest string) bool {
	doc, err := g.c.Vis.Get(ctx, strings.TrimPrefix(digest, "sha256:"))
	return err == nil && doc != nil && len(doc.Public) > 0
}

func (g *Registry) manifest(w http.ResponseWriter, r *http.Request, c *Claims, repo, ref string) {
	ctx := r.Context()
	digest, media := ref, ""
	var ok, public bool
	var err error
	if ValidDigest(ref) {
		ok, err = g.visible(ctx, c, digest)
		public = ok && g.isPublic(ctx, digest)
	} else {
		var idx *RepoIndex
		if idx, _, err = g.Repos.Get(ctx, repo); err != nil {
			g.unavailable(w, r, err)
			return
		}
		e, found := idx.lookup(ref)
		if !found {
			writeErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		digest, media = e.D, e.M
		ok, public, err = g.tagVisible(ctx, repo, e, c.sees)
	}
	if err != nil {
		g.unavailable(w, r, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	body, _, err := g.c.Objects.Read(ctx, BlobKey(digest), maxManifest)
	switch {
	case errors.Is(err, store.ErrObjectNotFound), errors.Is(err, store.ErrTooLarge):
		writeErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	case err != nil:
		g.unavailable(w, r, err)
		return
	}
	if media == "" {
		media = MediaTypeOf(body)
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	cacheControl(w, public, ValidDigest(ref))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// lookup finds a tag (nil index: none).
func (x *RepoIndex) lookup(tag string) (TagEntry, bool) {
	if x == nil {
		return TagEntry{}, false
	}
	e, ok := x.Tags[tag]
	return e, ok
}

// MediaTypeOf reads the mediaType of a manifest or index (an index without one is
// recognised by its "manifests" list).
func MediaTypeOf(body []byte) string {
	var m struct {
		MediaType string            `json:"mediaType"`
		Manifests []json.RawMessage `json:"manifests"`
	}
	if json.Unmarshal(body, &m) == nil && m.MediaType != "" && len(m.MediaType) < 256 {
		return m.MediaType
	}
	if m.Manifests != nil {
		return mediaIndex
	}
	return mediaManifest
}

func (g *Registry) blob(w http.ResponseWriter, r *http.Request, c *Claims, digest string) {
	ctx := r.Context()
	ok, err := g.visible(ctx, c, digest)
	if err != nil {
		g.unavailable(w, r, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	cacheControl(w, g.isPublic(ctx, digest), true)
	w.Header().Set("Docker-Content-Digest", digest)
	g.stream(w, r, BlobKey(digest))
}

// stream proxies key: one S3 GET, a forwarded single Range, a 256 KiB buffer, at most
// MaxDownloads at once (503 Retry-After beyond), 60 s client idle limit (§6.6.3).
func (g *Registry) stream(w http.ResponseWriter, r *http.Request, key string) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusServiceUnavailable, "TOOMANYREQUESTS", "too many parallel downloads, try again")
		return
	}
	Stream(w, r, g.c.Objects, key, "application/octet-stream", g.c.Log, func() {
		writeErr(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
	})
}

// Stream proxies an artifacts-bucket object to the client (registry blobs, release assets):
// HEAD answers the size, GET streams with one forwarded Range; notFound writes the 404.
func Stream(w http.ResponseWriter, r *http.Request, obj *store.Objects, key, contentType string, log *slog.Logger, notFound func()) {
	ctx := r.Context()
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Method == http.MethodHead {
		inf, err := obj.Head(ctx, key)
		switch {
		case errors.Is(err, store.ErrObjectNotFound):
			notFound()
		case err != nil:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "storage unavailable, try again", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Length", strconv.FormatInt(inf.Size, 10))
		}
		return
	}
	o, err := obj.Get(ctx, key, singleRange(r.Header.Get("Range")), "")
	switch {
	case errors.Is(err, store.ErrObjectNotFound):
		notFound()
		return
	case errors.Is(err, store.ErrRange):
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	case err != nil:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "storage unavailable, try again", http.StatusServiceUnavailable)
		return
	}
	defer o.Body.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(o.Length, 10))
	status := http.StatusOK
	if o.PartialAnswer {
		w.Header().Set("Content-Range", o.ContentRange)
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	rc := http.NewResponseController(w)
	buf := make([]byte, copyBuffer)
	for {
		n, rerr := o.Body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(clientIdle))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			// short of Content-Length: net/http closes the connection and the client resumes
			if ctx.Err() == nil && log != nil {
				log.Warn("download aborted", "key", key, "err", rerr.Error())
			}
			return
		}
	}
}

// singleRange returns the Range header when it names exactly one byte range (§6.6.3).
func singleRange(v string) string {
	spec, ok := strings.CutPrefix(strings.TrimSpace(v), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return ""
	}
	a, b, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok || a == "" && b == "" {
		return ""
	}
	for _, s := range []string{a, b} {
		if s == "" {
			continue
		}
		if _, err := strconv.ParseUint(s, 10, 63); err != nil {
			return ""
		}
	}
	if a != "" && b != "" {
		x, _ := strconv.ParseUint(a, 10, 63)
		y, _ := strconv.ParseUint(b, 10, 63)
		if y < x {
			return ""
		}
	}
	return "bytes=" + a + "-" + b
}

// visibleTags returns the tags of repo's index idx that the bearer sees (tagVisible, sorted).
func (g *Registry) visibleTags(ctx context.Context, repo string, idx *RepoIndex, sees func(projects []string) bool) ([]string, error) {
	var out []string
	for _, t := range idx.SortedTags() {
		vis, _, err := g.tagVisible(ctx, repo, idx.Tags[t], sees)
		if err != nil {
			return nil, err
		}
		if vis {
			out = append(out, t)
		}
	}
	return out, nil
}

func (g *Registry) tags(w http.ResponseWriter, r *http.Request, c *Claims, repo string) {
	ctx := r.Context()
	idx, _, err := g.Repos.Get(ctx, repo)
	if err != nil {
		g.unavailable(w, r, err)
		return
	}
	tags, err := g.visibleTags(ctx, repo, idx, c.sees)
	if err != nil {
		g.unavailable(w, r, err)
		return
	}
	q := r.URL.Query()
	if last := q.Get("last"); last != "" {
		i, _ := slices.BinarySearch(tags, last)
		for i < len(tags) && tags[i] <= last {
			i++
		}
		tags = tags[i:]
	}
	if n, err := strconv.Atoi(q.Get("n")); err == nil && n >= 0 && n < len(tags) {
		tags = tags[:n]
		w.Header().Set("Link", `</v2/`+repo+`/tags/list?n=`+strconv.Itoa(n)+`&last=`+url.QueryEscape(tags[len(tags)-1])+`>; rel="next"`)
	}
	if tags == nil {
		tags = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
}

// TokenHandler serves GET /oci/token (§6.7).
func (g *Registry) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := g.c.Principal(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			return
		}
		ctx := r.Context()
		claims := Claims{Sub: "anonymous", Acc: map[string][]string{}}
		var sees func([]string) bool = func([]string) bool { return false }
		if p != nil {
			claims.Sub = p.ID
			if g.c.Scope != nil {
				sc, err := g.c.Scope.Scope(ctx, p, GetRepositoryMethod)
				if err != nil {
					g.unavailable(w, r, err)
					return
				}
				switch {
				case sc.All:
					claims.Prj = []string{"*"}
				case len(sc.Projects) > maxTokenProjects:
					claims.Prj = sc.Projects[:maxTokenProjects]
				default:
					claims.Prj = sc.Projects
				}
			}
			sees = claims.sees
		}
		var scopes []string
		for _, s := range r.URL.Query()["scope"] {
			scopes = append(scopes, strings.Fields(s)...)
		}
		if len(scopes) > maxScopes {
			scopes = scopes[:maxScopes]
		}
		for _, s := range scopes {
			repo, ok := pullScope(s)
			if !ok || claims.CanPull(repo) {
				continue
			}
			vis, err := g.repoVisible(ctx, repo, sees)
			if err != nil {
				g.unavailable(w, r, err)
				return
			}
			if vis {
				claims.Acc[repo] = []string{"pull"}
			}
		}
		tok, err := g.Keys.Issue(ctx, claims)
		if err != nil {
			g.unavailable(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "access_token": tok,
			"expires_in": int(TokenTTL / time.Second), "issued_at": g.c.Now().UTC().Format(time.RFC3339)})
	})
}

// pullScope parses "repository:<repo>:<actions>" and reports the repo when pull is asked.
func pullScope(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, "repository:")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		return "", false
	}
	repo, actions := rest[:i], strings.Split(rest[i+1:], ",")
	return repo, ValidRepo(repo) && (slices.Contains(actions, "pull") || slices.Contains(actions, "*"))
}

// repoVisible: the repository has at least one tag visible to the caller (tagVisible).
func (g *Registry) repoVisible(ctx context.Context, repo string, sees func([]string) bool) (bool, error) {
	idx, _, err := g.Repos.Get(ctx, repo)
	if err != nil || idx == nil {
		return false, err
	}
	tags := idx.SortedTags()
	slices.SortStableFunc(tags, func(a, b string) int { // release tags first: they are the public ones
		ra, rb := idx.Tags[a].R != "", idx.Tags[b].R != ""
		switch {
		case ra && !rb:
			return -1
		case rb && !ra:
			return 1
		}
		return 0
	})
	n := 0
	for _, t := range tags {
		if n++; n > maxVisibleChecks {
			break
		}
		vis, _, err := g.tagVisible(ctx, repo, idx.Tags[t], sees)
		if err != nil {
			return false, err
		}
		if vis {
			return true, nil
		}
	}
	return false, nil
}
