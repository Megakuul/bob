package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bobspike/s12/store"
)

const ociTokenTTL = 15 * time.Minute

func ociError(w http.ResponseWriter, code int, ecode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": ecode, "message": msg}}})
}

func (s *server) challenge(w http.ResponseWriter, scope, errText string) {
	v := fmt.Sprintf(`Bearer realm="%s/oci/token",service="bob"`, s.publicURL)
	if scope != "" {
		v += fmt.Sprintf(`,scope="%s"`, scope)
	}
	if errText != "" {
		v += `,error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", v)
	ociError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

// repoReadable: does p have pull on repo? Visibility markers decide (public / p.<project>).
func (s *server) repoReadable(r *http.Request, p *Principal, repo string) (bool, error) {
	if !p.Anon && p.all() {
		return true, nil
	}
	ctx, cancel := ctxT(r)
	defer cancel()
	keys, err := s.st.List(ctx, store.RepoVisPrefix(repo), 1000)
	if err != nil {
		return false, err
	}
	var scopes []string
	for _, k := range keys {
		scopes = append(scopes, strings.TrimPrefix(k, store.RepoVisPrefix(repo)))
	}
	return p.canRead(scopes), nil
}

// ociToken implements the distribution token flow: GET ?service=bob&scope=repository:R:pull.
// Credentials: Basic (service-account name : token) or none (anonymous, public repos only).
// Push is never granted (R-REG-003: only promotion writes).
func (s *server) ociToken(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
		ociError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid service account credentials")
		return
	}
	acc := map[string][]string{}
	for _, sc := range r.URL.Query()["scope"] {
		parts := strings.Split(sc, ":")
		if len(parts) != 3 || parts[0] != "repository" || !reRepo.MatchString(parts[1]) {
			continue
		}
		wantPull := false
		for _, a := range strings.Split(parts[2], ",") {
			if a == "pull" {
				wantPull = true
			}
		}
		if !wantPull {
			continue
		}
		ok, err := s.repoReadable(r, p, parts[1])
		if err != nil {
			ociError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable")
			return
		}
		if ok {
			acc[parts[1]] = []string{"pull"}
		}
		// Not readable: token is issued without the scope (spec behaviour); the registry then
		// answers 401, so clients cannot tell private from missing.
	}
	now := time.Now()
	c := ociClaims{Sub: p.Name, Exp: now.Add(ociTokenTTL).Unix(), Access: acc}
	if !p.Anon {
		c.Projs = p.Projects
	}
	tok := s.signOCIToken(c)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"token": tok, "access_token": tok,
		"expires_in": int(ociTokenTTL.Seconds()), "issued_at": now.UTC().Format(time.RFC3339)})
}

func (s *server) ociHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "this registry is pull-only")
		return
	}
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	rest := strings.TrimPrefix(r.URL.Path, "/v2/")
	// Only tokens from our token endpoint (or raw bob tokens as Bearer/Basic) are accepted.
	var p *Principal
	if r.Header.Get("Authorization") != "" {
		var err error
		if p, err = s.authenticate(r); err != nil {
			s.challenge(w, "", "invalid_token")
			return
		}
	}
	if rest == "" {
		if p == nil {
			s.challenge(w, "", "")
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	repo, kind, ref, ok := splitOCIPath(rest)
	if !ok || !reRepo.MatchString(repo) {
		ociError(w, http.StatusNotFound, "NAME_INVALID", "invalid repository name")
		return
	}
	scope := "repository:" + repo + ":pull"
	if p == nil {
		s.challenge(w, scope, "")
		return
	}
	allowed := false
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer bob-oci-v1.") {
		// issued token: scope list is authoritative
		c := s.mustClaims(r)
		_, allowed = c.Access[repo]
	} else {
		var err error
		if allowed, err = s.repoReadable(r, p, repo); err != nil {
			ociError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable")
			return
		}
	}
	if !allowed {
		s.challenge(w, scope, "insufficient_scope")
		return
	}
	switch kind {
	case "manifests":
		s.ociManifest(w, r, repo, ref)
	case "blobs":
		s.ociBlob(w, r, repo, ref, "application/octet-stream")
	case "tags":
		s.ociTags(w, r, repo)
	case "referrers":
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		io.WriteString(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	default:
		ociError(w, http.StatusNotFound, "UNSUPPORTED", "unknown endpoint")
	}
}

func (s *server) mustClaims(r *http.Request) ociClaims {
	t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, _ := strings.CutPrefix(t, "bob-oci-v1.")
	p, _, _ = strings.Cut(p, ".")
	var c ociClaims
	if b, err := decodeB64(p); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// splitOCIPath: <name>/manifests/<ref>, <name>/blobs/<digest>, <name>/tags/list,
// <name>/referrers/<digest>. Repo names may contain slashes.
func splitOCIPath(rest string) (repo, kind, ref string, ok bool) {
	for _, k := range []string{"/manifests/", "/blobs/", "/referrers/"} {
		if i := strings.LastIndex(rest, k); i > 0 {
			return rest[:i], strings.Trim(k, "/"), rest[i+len(k):], true
		}
	}
	if r, found := strings.CutSuffix(rest, "/tags/list"); found {
		return r, "tags", "", true
	}
	return "", "", "", false
}

func (s *server) ociManifest(w http.ResponseWriter, r *http.Request, repo, ref string) {
	digest := ref
	if !strings.HasPrefix(ref, "sha256:") {
		if !reTag.MatchString(ref) {
			ociError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "bad tag")
			return
		}
		b, err := s.st.GetSmall(r.Context(), store.RepoTagKey(repo, ref), 128)
		if errors.Is(err, store.ErrNotFound) {
			ociError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		} else if err != nil {
			ociError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable")
			return
		}
		digest = strings.TrimSpace(string(b))
	}
	s.ociBlob(w, r, repo, digest, "")
}

func (s *server) ociBlob(w http.ResponseWriter, r *http.Request, repo, digest, defType string) {
	hx, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || !reHex.MatchString(hx) {
		ociError(w, http.StatusNotFound, "DIGEST_INVALID", "bad digest")
		return
	}
	ctx := r.Context()
	// Blob must belong to this repo (blobs are shared bucket-wide, access is per repo).
	if ok, err := s.st.Exists(ctx, store.RepoBlobKey(repo, hx)); err != nil {
		ociError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable")
		return
	} else if !ok {
		code := "BLOB_UNKNOWN"
		if defType == "" {
			code = "MANIFEST_UNKNOWN"
		}
		ociError(w, http.StatusNotFound, code, "unknown")
		return
	}
	key := store.BlobKey(hx)
	setHdr := func(ct *string, cl *int64) {
		t := defType
		if t == "" && ct != nil {
			t = *ct
		}
		if t == "" {
			t = "application/octet-stream"
		}
		w.Header().Set("Content-Type", t)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("ETag", `"`+digest+`"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if cl != nil {
			w.Header().Set("Content-Length", strconv.FormatInt(*cl, 10))
		}
	}
	if r.Method == http.MethodHead {
		ho, err := s.st.Head(ctx, key)
		if err != nil {
			ociError(w, http.StatusNotFound, "BLOB_UNKNOWN", "unknown")
			return
		}
		setHdr(ho.ContentType, ho.ContentLength)
		return
	}
	rng := r.Header.Get("Range")
	if rng != "" && !validRange(rng) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	out, err := s.st.Get(ctx, key, rng)
	if err != nil {
		log.Printf("oci get %s: %v", key, err)
		ociError(w, http.StatusNotFound, "BLOB_UNKNOWN", "unknown")
		return
	}
	defer out.Body.Close()
	setHdr(out.ContentType, out.ContentLength)
	if rng != "" && out.ContentRange != nil {
		w.Header().Set("Content-Range", *out.ContentRange)
		w.WriteHeader(http.StatusPartialContent)
	}
	io.Copy(w, out.Body)
}

func (s *server) ociTags(w http.ResponseWriter, r *http.Request, repo string) {
	keys, err := s.st.List(r.Context(), store.RepoTagPrefix(repo), 10000)
	if err != nil {
		ociError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "storage unavailable")
		return
	}
	tags := []string{}
	for _, k := range keys {
		tags = append(tags, strings.TrimPrefix(k, store.RepoTagPrefix(repo)))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
}

func decodeB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
