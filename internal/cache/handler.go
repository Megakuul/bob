package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/megakuul/bob/internal/api"
)

// Keys of the served objects (SPEC §05.2.4).
func NarInfoKey(hash string) string { return "nix/narinfo/" + hash + ".narinfo" }
func NarKey(hash string) string     { return "nix/nar/" + hash + ".nar.zst" }
func ListingKey(hash string) string { return "nix/ls/" + hash + ".ls" }

// GetPathMethod is the permission that makes private paths visible (SPEC §6.6.2).
const GetPathMethod = "bob.cache.v1.CacheService/GetPath"

const (
	// DefaultMaxDownloads is max_parallel_downloads (SPEC §6.6.3).
	DefaultMaxDownloads = 256
	copyBuffer          = 256 << 10
	clientIdle          = 60 * time.Second
	cacheInfo           = "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n"
)

// Config configures the binary cache handler.
type Config struct {
	Objects *Objects
	Vis     *VisStore // NixVisPrefix
	// Principal authenticates the request's Basic or Bearer credential: (nil, nil) is
	// anonymous, an error is a presented credential that is invalid or expired (401).
	Principal func(*http.Request) (*api.Principal, error)
	// Allowed reports whether p holds GetPathMethod on at least one of projects.
	Allowed      func(ctx context.Context, p *api.Principal, projects []string) (bool, error)
	MaxDownloads int // 0 = DefaultMaxDownloads
	Log          *slog.Logger
}

// Handler serves /nix/ (SPEC §6.6.1): every byte streams from S3 through this process (D61).
type Handler struct {
	c     Config
	slots chan struct{}
}

// NewHandler returns the /nix/ handler.
func NewHandler(c Config) *Handler {
	if c.MaxDownloads <= 0 {
		c.MaxDownloads = DefaultMaxDownloads
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Handler{c: c, slots: make(chan struct{}, c.MaxDownloads)}
}

type request struct {
	kind string // info | narinfo | nar | ls
	hash string
}

// route parses the path below /nix/; ok is false for anything that is not a cache object.
func route(p string) (request, bool) {
	rest, ok := strings.CutPrefix(p, "/nix/")
	if !ok {
		return request{}, false
	}
	if rest == "nix-cache-info" {
		return request{kind: "info"}, true
	}
	var r request
	switch {
	case strings.HasPrefix(rest, "nar/") && strings.HasSuffix(rest, ".nar.zst"):
		r = request{kind: "nar", hash: strings.TrimSuffix(strings.TrimPrefix(rest, "nar/"), ".nar.zst")}
	case strings.HasSuffix(rest, ".narinfo"):
		r = request{kind: "narinfo", hash: strings.TrimSuffix(rest, ".narinfo")}
	case strings.HasSuffix(rest, ".ls"):
		r = request{kind: "ls", hash: strings.TrimSuffix(rest, ".ls")}
	default:
		return request{}, false
	}
	return r, ValidHash(r.hash)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
	http.Error(w, "unauthorized: token invalid or expired (run 'bob login')", http.StatusUnauthorized)
}

func notFound(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }

func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() == nil {
		h.c.Log.Warn("binary cache: storage error", "path", r.URL.Path, "err", err.Error())
	}
	w.Header().Set("Retry-After", "1")
	http.Error(w, "storage unavailable, try again", http.StatusServiceUnavailable)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, ok := route(r.URL.Path)
	if !ok {
		notFound(w)
		return
	}
	p, err := h.c.Principal(r)
	if err != nil {
		unauthorized(w)
		return
	}
	if req.kind == "info" {
		w.Header().Set("Content-Type", "text/x-nix-cache-info")
		w.Header().Set("Content-Length", strconv.Itoa(len(cacheInfo)))
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, cacheInfo)
		}
		return
	}
	public, visible, err := h.visible(r.Context(), p, req.hash)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}
	if !visible {
		notFound(w)
		return
	}
	scope := "private"
	if public {
		scope = "public"
	}
	switch req.kind {
	case "narinfo":
		h.narinfo(w, r, req.hash, scope)
	case "nar":
		w.Header().Set("Cache-Control", scope+", max-age=31536000, immutable")
		h.stream(w, r, NarKey(req.hash), "application/x-nix-nar")
	case "ls":
		w.Header().Set("Cache-Control", scope+", max-age=300")
		h.stream(w, r, ListingKey(req.hash), "application/json")
	}
}

// visible decides from the visibility document alone (SPEC §6.6.2, R-REG-150).
func (h *Handler) visible(ctx context.Context, p *api.Principal, hash string) (public, ok bool, err error) {
	doc, err := h.c.Vis.Get(ctx, hash)
	if err != nil || doc == nil {
		return false, false, err
	}
	if len(doc.Public) > 0 {
		return true, true, nil
	}
	if p == nil || len(doc.Projects) == 0 {
		return false, false, nil
	}
	ok, err = h.c.Allowed(ctx, p, doc.Projects)
	return false, ok, err
}

func (h *Handler) narinfo(w http.ResponseWriter, r *http.Request, hash, scope string) {
	body, _, err := h.c.Objects.Read(r.Context(), NarInfoKey(hash), MaxNarInfo)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-nix-narinfo")
	w.Header().Set("Cache-Control", scope+", max-age=300")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// singleRange returns the Range header when it names exactly one byte range; anything else
// (absent, several ranges, malformed) is answered with the full body (SPEC §6.6.3).
func singleRange(v string) string {
	spec, ok := strings.CutPrefix(strings.TrimSpace(v), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return ""
	}
	a, b, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok || (a == "" && b == "") {
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

// stream proxies key to the client: one S3 GET, forwarded single Range, a 256 KiB buffer,
// at most MaxDownloads at once (503 Retry-After beyond), 60 s client idle limit.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request, key, contentType string) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many parallel downloads, try again", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Method == http.MethodHead {
		inf, err := h.c.Objects.Head(ctx, key)
		switch {
		case errors.Is(err, ErrNotFound):
			notFound(w)
		case err != nil:
			h.unavailable(w, r, err)
		default:
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Length", strconv.FormatInt(inf.Size, 10))
		}
		return
	}
	obj, err := h.c.Objects.Get(ctx, key, singleRange(r.Header.Get("Range")), "")
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case errors.Is(err, ErrRange):
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}
	defer obj.Body.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Length, 10))
	status := http.StatusOK
	if obj.PartialAnswer {
		w.Header().Set("Content-Range", obj.ContentRange)
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	rc := http.NewResponseController(w)
	buf := make([]byte, copyBuffer)
	for {
		n, rerr := obj.Body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(clientIdle))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client gone or idle: the connection is closed
			}
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			// The body is short of Content-Length: net/http then closes the connection, so
			// the client sees a short read (and resumes with Range), never a silently
			// truncated object.
			if ctx.Err() == nil {
				h.c.Log.Warn("binary cache: download aborted", "key", key, "err", rerr.Error())
			}
			return
		}
	}
}
