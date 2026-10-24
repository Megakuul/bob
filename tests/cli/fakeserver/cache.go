package fakeserver

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Release is a release the registry serves.
type Release struct {
	Project, Name, Version string
	Private                bool
	Program                []byte   // content of every binary; empty: the release has no closure artifacts
	Bins                   []string // binary names (default: Name)
	Images                 []string // names of OCI images the release contains (no closure needed)

	path string // store path of the closure root
}

type storeEntry struct {
	storePath string
	nar       []byte
	narHash   [32]byte
	private   bool
}

// Platform is the platform string of the host, as bob names it ("linux-arm64", "linux-amd64").
func Platform() string { return "linux-" + runtime.GOARCH }

// AddRelease registers r. A release with a Program gets one store path under /nix/store holding
// bin/<bin> (executable), served as a NAR signed by the server's key.
func (s *Server) AddRelease(t testing.TB, r Release) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(r.Program) > 0 {
		if len(r.Bins) == 0 {
			r.Bins = []string{r.Name}
		}
		sum := sha256.Sum256([]byte(r.Project + "/" + r.Name + "@" + r.Version + s.opts.Name))
		name := r.Name + "-" + r.Version
		hash := nix32(sum[:20])
		r.path = "/nix/store/" + hash + "-" + name
		var nar bytes.Buffer
		writeNAR(&nar, r.Bins, r.Program)
		e := &storeEntry{storePath: r.path, nar: nar.Bytes(), narHash: sha256.Sum256(nar.Bytes()), private: r.Private}
		s.store[hash] = e
	}
	s.releases = append(s.releases, &r)
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	var req struct{ ProjectID, Name, Constraint, Platform string }
	if r.Method == http.MethodGet {
		_ = json.Unmarshal([]byte(r.URL.Query().Get("message")), &req)
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		connectError(w, 400, "invalid_argument", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, present, ok := s.authenticate(r)
	if present && !ok {
		connectError(w, 401, "unauthenticated", "invalid or expired credential")
		return
	}
	var best *Release
	for _, rel := range s.releases {
		if rel.Project != req.ProjectID || rel.Name != req.Name || !matches(rel.Version, req.Constraint) {
			continue
		}
		if rel.Private && !(ok && tok.Scope != "") { // no existence leak: NotFound
			continue
		}
		if best == nil || cmpVersion(rel.Version, best.Version) > 0 {
			best = rel
		}
	}
	if best == nil {
		connectError(w, 404, "not_found", "release not found")
		return
	}
	vis := "VISIBILITY_PUBLIC"
	if best.Private {
		vis = "VISIBILITY_PRIVATE"
	}
	var images []map[string]string
	for _, im := range best.Images {
		images = append(images, map[string]string{"repository": best.Project + "/" + im, "tag": best.Version})
	}
	rel := map[string]any{"id": "rel-" + best.Name + "-" + best.Version, "projectId": best.Project, "name": best.Name, "version": best.Version,
		"visibility": vis, "state": "RELEASE_STATE_PUBLISHED", "images": images}
	out := map[string]any{"release": rel, "substituters": []string{s.srv.URL + "/nix"}, "publicKeys": []string{s.pubKeyLocked()}}
	if best.path != "" {
		e := s.store[strings.SplitN(strings.TrimPrefix(best.path, "/nix/store/"), "-", 2)[0]]
		cl := map[string]any{"platform": Platform(), "roots": []string{best.path}, "closureNarBytes": strconv.Itoa(len(e.nar))}
		rel["closures"] = []any{cl}
		out["closure"] = cl
	}
	writeJSON(w, 200, out)
}

func (s *Server) cache(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/nix/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == "nix-cache-info" {
		fmt.Fprint(w, "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n")
		return
	}
	hash, isInfo := strings.CutSuffix(p, ".narinfo")
	nar := false
	if !isInfo {
		hash, nar = strings.CutSuffix(strings.TrimPrefix(p, "nar/"), ".nar")
		if !nar {
			http.NotFound(w, r)
			return
		}
	}
	e, ok := s.store[hash]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if e.private { // downloads of private releases need a valid token
		if _, present, valid := s.authenticate(r); !valid {
			status := 404
			if present {
				status = 401
			}
			http.Error(w, "denied", status)
			return
		}
	}
	if nar {
		w.Header().Set("Content-Type", "application/x-nix-nar")
		_, _ = w.Write(e.nar)
		return
	}
	narHash := "sha256:" + nix32(e.narHash[:])
	fp := fmt.Sprintf("1;%s;%s;%d;", e.storePath, narHash, len(e.nar))
	sig := s.keyName + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(fp)))
	w.Header().Set("Content-Type", "text/x-nix-narinfo")
	fmt.Fprintf(w, "StorePath: %s\nURL: nar/%s.nar\nCompression: none\nFileHash: %s\nFileSize: %d\nNarHash: %s\nNarSize: %d\nReferences: \nSig: %s\n",
		e.storePath, hash, narHash, len(e.nar), narHash, len(e.nar), sig)
}

func matches(version, constraint string) bool {
	if constraint == "" || constraint == "latest" {
		return !strings.Contains(version, "-")
	}
	if version == constraint {
		return true
	}
	return strings.HasPrefix(version, constraint+".") && !strings.Contains(version, "-")
}

func cmpVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}

// nix32 is Nix's base-32 encoding of a hash.
func nix32(h []byte) string {
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
	n := (len(h)*8-1)/5 + 1
	out := make([]byte, 0, n)
	for i := n - 1; i >= 0; i-- {
		b := i * 5
		j, k := b/8, b%8
		c := uint(h[j]) >> k
		if j+1 < len(h) {
			c |= uint(h[j+1]) << (8 - k)
		}
		out = append(out, alphabet[c&0x1f])
	}
	return string(out)
}

func narStr(b *bytes.Buffer, s string) {
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(len(s)))
	b.Write(l[:])
	b.WriteString(s)
	if pad := (8 - len(s)%8) % 8; pad > 0 {
		b.Write(make([]byte, pad))
	}
}

// writeNAR serialises the tree bin/<bin>… (each an executable regular file with content).
func writeNAR(b *bytes.Buffer, bins []string, content []byte) {
	sort.Strings(bins)
	narStr(b, "nix-archive-1")
	narStr(b, "(")
	narStr(b, "type")
	narStr(b, "directory")
	entry := func(name string, body func()) {
		narStr(b, "entry")
		narStr(b, "(")
		narStr(b, "name")
		narStr(b, name)
		narStr(b, "node")
		narStr(b, "(")
		body()
		narStr(b, ")")
		narStr(b, ")")
	}
	entry("bin", func() {
		narStr(b, "type")
		narStr(b, "directory")
		for _, n := range bins {
			entry(n, func() {
				narStr(b, "type")
				narStr(b, "regular")
				narStr(b, "executable")
				narStr(b, "")
				narStr(b, "contents")
				narStr(b, string(content))
			})
		}
	})
	narStr(b, ")")
}
