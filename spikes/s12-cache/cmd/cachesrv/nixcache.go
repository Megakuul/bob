package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"bobspike/s12/nixfmt"
	"bobspike/s12/store"
)

const cacheInfo = "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n"

func nixUnauthorized(w http.ResponseWriter) {
	// Basic challenge so curl/Nix would retry with netrc creds if it had not sent them.
	w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
	http.Error(w, "unauthorized: token invalid or expired (run 'bob login')", http.StatusUnauthorized)
}

// visibleScopes returns the visibility markers of a store hash (public, p.<project>).
func (s *server) visible(r *http.Request, p *Principal, h string) (bool, error) {
	ctx, cancel := ctxT(r)
	defer cancel()
	if p.Anon {
		return s.st.Exists(ctx, store.NixVisKey(h, store.ScopePublic))
	}
	if p.all() {
		return s.st.Exists(ctx, store.NarInfoKey(h))
	}
	keys, err := s.st.List(ctx, store.NixVisPrefix(h), 1000)
	if err != nil {
		return false, err
	}
	var scopes []string
	for _, k := range keys {
		scopes = append(scopes, strings.TrimPrefix(k, store.NixVisPrefix(h)))
	}
	return p.canRead(scopes), nil
}

func (s *server) nixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, err := s.authenticate(r)
	if err != nil {
		// Invalid (not missing) credentials: 401 everywhere, including nix-cache-info, so that
		// Nix drops the substituter instead of downloading with a broken identity.
		nixUnauthorized(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/nix/")
	switch {
	case rest == "nix-cache-info":
		w.Header().Set("Content-Type", "text/x-nix-cache-info")
		io.WriteString(w, cacheInfo)
	case strings.HasSuffix(rest, ".narinfo") && !strings.Contains(rest, "/"):
		s.serveNarInfo(w, r, p, strings.TrimSuffix(rest, ".narinfo"))
	case strings.HasSuffix(rest, ".ls") && !strings.Contains(rest, "/"):
		h := strings.TrimSuffix(rest, ".ls")
		s.serveObject(w, r, p, h, store.LsKey(h), "application/json")
	case strings.HasPrefix(rest, "nar/") && strings.HasSuffix(rest, ".nar.zst"):
		h := strings.TrimSuffix(strings.TrimPrefix(rest, "nar/"), ".nar.zst")
		s.serveObject(w, r, p, h, store.NarKey(h), "application/zstd")
	default:
		http.NotFound(w, r)
	}
}

func (s *server) serveNarInfo(w http.ResponseWriter, r *http.Request, p *Principal, h string) {
	s.stats.narinfo.Add(1)
	if !nixfmt.ValidHash(h) {
		http.NotFound(w, r)
		return
	}
	ok, err := s.visible(r, p, h)
	if err != nil {
		log.Printf("visibility %s: %v", h, err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		// Not visible to this caller = not there (no existence leak, Nix falls through).
		http.NotFound(w, r)
		return
	}
	s.serveObject(w, r, nil, h, store.NarInfoKey(h), "text/x-nix-narinfo")
}

// serveObject streams an S3 object to the client. Range is passed through; nothing is
// buffered beyond io.Copy's 32 KiB.
func (s *server) serveObject(w http.ResponseWriter, r *http.Request, p *Principal, h, key, ctype string) {
	if !nixfmt.ValidHash(h) {
		http.NotFound(w, r)
		return
	}
	if p != nil {
		ok, err := s.visible(r, p, h)
		if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
	}
	ctx := r.Context()
	if r.Method == http.MethodHead {
		ho, err := s.st.Head(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(*ho.ContentLength, 10))
		return
	}
	rng := r.Header.Get("Range")
	if rng != "" && !validRange(rng) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	out, err := s.st.Get(ctx, key, rng)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		if strings.Contains(err.Error(), "InvalidRange") || strings.Contains(err.Error(), "416") {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		log.Printf("get %s: %v", key, err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	defer out.Body.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Accept-Ranges", "bytes")
	if out.ContentLength != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*out.ContentLength, 10))
	}
	if out.ETag != nil {
		w.Header().Set("ETag", *out.ETag)
	}
	if strings.HasSuffix(key, ".narinfo") {
		w.Header().Set("Cache-Control", "public, max-age=300")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if out.ContentRange != nil && rng != "" {
		w.Header().Set("Content-Range", *out.ContentRange)
		w.WriteHeader(http.StatusPartialContent)
	}
	var src io.Reader = out.Body
	if strings.HasSuffix(key, ".nar.zst") {
		s.stats.nar.Add(1)
		if cut := s.faultCut.Swap(0); cut > 0 {
			n, _ := io.CopyN(w, src, cut)
			s.stats.narBytes.Add(n)
			log.Printf("FAULT: cut %s after %d bytes", key, n)
			panic(http.ErrAbortHandler) // drops the connection mid-body
		}
	}
	n, err := io.Copy(w, src)
	if strings.HasSuffix(key, ".nar.zst") {
		s.stats.narBytes.Add(n)
	}
	if err != nil {
		log.Printf("stream %s: %v after %d bytes", key, err, n)
	}
}

// validRange accepts a single "bytes=a-b" / "bytes=a-" / "bytes=-n" range.
func validRange(v string) bool {
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return false
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok || (a == "" && b == "") {
		return false
	}
	for _, x := range []string{a, b} {
		if x != "" {
			if _, err := strconv.ParseUint(x, 10, 63); err != nil {
				return false
			}
		}
	}
	return true
}

var _ = fmt.Sprint
