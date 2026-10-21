package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"bobspike/s12/nar"
	"bobspike/s12/nixfmt"
	"bobspike/s12/store"

	"github.com/klauspost/compress/zstd"
)

// Hostile-input caps for promotion [proposal].
const (
	maxNarSize   = 64 << 30
	maxPaths     = 100_000
	maxManifest  = 4 << 20
	maxBlobSize  = 32 << 30
	maxSmallBlob = 4 << 20
)

type complete struct {
	Build  string        `json:"build"`
	Paths  []string      `json:"paths"`
	Images []stagedImage `json:"images"`
}

type stagedImage struct {
	Repo      string   `json:"repo"`
	Tag       string   `json:"tag"`
	Manifest  string   `json:"manifest"`
	MediaType string   `json:"mediaType"`
	Blobs     []string `json:"blobs"`
}

var (
	reBuild = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reRepo  = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)
	reTag   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	reHex   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type promoteResult struct {
	Promoted     int      `json:"promoted"`
	AlreadyThere int      `json:"alreadyThere"`
	NonRepro     []string `json:"nonReproducible,omitempty"`
	Images       int      `json:"images"`
	Seconds      float64  `json:"seconds"`
	NarMiB       float64  `json:"narMiB"`
}

func (s *server) promoteHandler(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil || p.Anon || !p.all() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	build, project := q.Get("build"), q.Get("project")
	if !reBuild.MatchString(build) || !reBuild.MatchString(project) {
		http.Error(w, "bad build/project", http.StatusBadRequest)
		return
	}
	scopes := []string{store.ProjectScope(project)}
	if q.Get("public") == "1" {
		scopes = append(scopes, store.ScopePublic)
	}
	res, err := s.promote(r.Context(), build, scopes)
	if err != nil {
		log.Printf("promote %s: %v", build, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	json.NewEncoder(w).Encode(res)
}

// promote runs the promotion protocol. Every step is idempotent, so a crashed orchestrator's
// promotion is simply re-run by another one (the reconciler sees build state "succeeded,
// not promoted").
func (s *server) promote(ctx context.Context, build string, scopes []string) (*promoteResult, error) {
	t0 := time.Now()
	b, err := s.st.GetSmall(ctx, store.StagingComplete(build), maxManifest)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("staging incomplete: %s missing (upload not finished or worker died)", store.StagingComplete(build))
	} else if err != nil {
		return nil, err
	}
	var c complete
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("COMPLETE.json: %w", err)
	}
	if len(c.Paths) > maxPaths {
		return nil, errors.New("too many paths")
	}
	// Read and validate all claims first.
	claims := map[string]*nixfmt.NarInfo{}
	for _, sp := range c.Paths {
		if !nixfmt.ValidStorePath(sp) {
			return nil, fmt.Errorf("bad store path %q", sp)
		}
		h := nixfmt.HashPart(sp)
		raw, err := s.st.GetSmall(ctx, store.StagingNarInfo(build, h), 1<<20)
		if err != nil {
			return nil, fmt.Errorf("claim %s: %w", sp, err)
		}
		ni, err := nixfmt.ParseNarInfo(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("claim %s: %w", sp, err)
		}
		if ni.StorePath != sp || ni.NarSize > maxNarSize || ni.Compression != "zstd" {
			return nil, fmt.Errorf("claim %s: inconsistent", sp)
		}
		claims[h] = ni
	}
	// Topological order: references inside the set are published first, so a visible
	// narinfo never points to a reference that is not yet substitutable.
	levels, err := topo(claims)
	if err != nil {
		return nil, err
	}
	res := &promoteResult{}
	var mu sync.Mutex
	for _, lvl := range levels {
		var wg sync.WaitGroup
		errs := make(chan error, len(lvl))
		sem := make(chan struct{}, 4)
		for _, h := range lvl {
			wg.Add(1)
			sem <- struct{}{}
			go func(h string) {
				defer wg.Done()
				defer func() { <-sem }()
				st, narSize, err := s.promotePath(ctx, build, h, claims[h], scopes)
				mu.Lock()
				defer mu.Unlock()
				switch st {
				case "promoted":
					res.Promoted++
					res.NarMiB += float64(narSize) / (1 << 20)
				case "exists":
					res.AlreadyThere++
				case "nonrepro":
					res.AlreadyThere++
					res.NonRepro = append(res.NonRepro, claims[h].StorePath)
				}
				if err != nil {
					errs <- fmt.Errorf("%s: %w", claims[h].StorePath, err)
				}
			}(h)
		}
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return nil, err // nothing of this level's failed path is visible; re-run is safe
		}
	}
	for _, im := range c.Images {
		if err := s.promoteImage(ctx, build, im, scopes); err != nil {
			return nil, fmt.Errorf("image %s:%s: %w", im.Repo, im.Tag, err)
		}
		res.Images++
	}
	res.Seconds = time.Since(t0).Seconds()
	log.Printf("promoted build %s: %+v", build, *res)
	return res, nil
}

func topo(claims map[string]*nixfmt.NarInfo) ([][]string, error) {
	depth := map[string]int{}
	state := map[string]int{}
	var visit func(h string) (int, error)
	visit = func(h string) (int, error) {
		switch state[h] {
		case 1:
			return 0, errors.New("reference cycle")
		case 2:
			return depth[h], nil
		}
		state[h] = 1
		d := 0
		for _, ref := range claims[h].References {
			rh := nixfmt.HashPart(ref)
			if rh == h {
				continue
			}
			if _, ok := claims[rh]; ok {
				rd, err := visit(rh)
				if err != nil {
					return 0, err
				}
				d = max(d, rd+1)
			}
		}
		state[h], depth[h] = 2, d
		return d, nil
	}
	var levels [][]string
	keys := make([]string, 0, len(claims))
	for h := range claims {
		keys = append(keys, h)
	}
	sort.Strings(keys)
	for _, h := range keys {
		d, err := visit(h)
		if err != nil {
			return nil, err
		}
		for len(levels) <= d {
			levels = append(levels, nil)
		}
		levels[d] = append(levels[d], h)
	}
	return levels, nil
}

type hashCount struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n uint64
}

func (c *hashCount) Write(p []byte) (int, error) { c.n += uint64(len(p)); return c.h.Write(p) }

type lsNode struct {
	Type       string             `json:"type"`
	Size       *uint64            `json:"size,omitempty"`
	Executable bool               `json:"executable,omitempty"`
	NarOffset  *uint64            `json:"narOffset,omitempty"`
	Target     string             `json:"target,omitempty"`
	Entries    map[string]*lsNode `json:"entries,omitempty"`
}

func (s *server) promotePath(ctx context.Context, build, h string, claim *nixfmt.NarInfo, scopes []string) (string, uint64, error) {
	// 1. Already published? First publication wins (paths are immutable).
	if raw, err := s.st.GetSmall(ctx, store.NarInfoKey(h), 1<<20); err == nil {
		have, err := nixfmt.ParseNarInfo(bytes.NewReader(raw))
		if err != nil {
			return "", 0, fmt.Errorf("published narinfo corrupt: %w", err)
		}
		status := "exists"
		if have.NarHash != claim.NarHash {
			status = "nonrepro" // R-NFR-006: report, keep the published one
		}
		return status, 0, s.markVisible(ctx, h, scopes)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", 0, err
	}
	// 2. Verify the staged NAR by streaming it once: zstd decode, NAR structure, hashes, sizes.
	src := store.StagingNar(build, h)
	tVerify := time.Now()
	obj, err := s.st.Get(ctx, src, "")
	if err != nil {
		return "", 0, fmt.Errorf("staged NAR: %w", err)
	}
	etag := ""
	if obj.ETag != nil {
		etag = *obj.ETag
	}
	fileH := &hashCount{h: sha256.New()}
	body := io.TeeReader(io.LimitReader(obj.Body, maxNarSize), fileH)
	zr, err := zstd.NewReader(body,
		zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		obj.Body.Close()
		return "", 0, err
	}
	narH := &hashCount{h: sha256.New()}
	root := &lsNode{}
	nodes := map[string]*lsNode{"": root}
	werr := nar.Walk(io.TeeReader(io.LimitReader(zr, maxNarSize), narH), func(e nar.Entry) error {
		n := root
		if e.Path != "" {
			i := strings.LastIndexByte(e.Path, '/')
			parent := nodes[e.Path[:i]]
			n = &lsNode{}
			if parent.Entries == nil {
				parent.Entries = map[string]*lsNode{}
			}
			parent.Entries[e.Path[i+1:]] = n
		}
		n.Type = string(e.Kind)
		switch e.Kind {
		case nar.Regular:
			sz, off := e.Size, e.NarOffset
			n.Size, n.NarOffset, n.Executable = &sz, &off, e.Executable
		case nar.Symlink:
			n.Target = e.Target
		case nar.Directory:
			nodes[e.Path] = n
		}
		return nil
	})
	if werr == nil {
		// The NAR must end exactly here: trailing bytes would change NarHash.
		if extra, _ := io.Copy(narH, zr); extra != 0 {
			werr = errors.New("trailing data after NAR")
		}
	}
	io.Copy(io.Discard, body) // finish FileHash over the full compressed object
	zr.Close()
	obj.Body.Close()
	if werr != nil {
		return "", 0, fmt.Errorf("NAR invalid: %w", werr)
	}
	gotNar := "sha256:" + nixfmt.Base32(narH.h.Sum(nil))
	gotFile := "sha256:" + nixfmt.Base32(fileH.h.Sum(nil))
	if gotNar != claim.NarHash || narH.n != claim.NarSize {
		return "", 0, fmt.Errorf("NarHash/NarSize mismatch: claimed %s/%d, got %s/%d", claim.NarHash, claim.NarSize, gotNar, narH.n)
	}
	if gotFile != claim.FileHash || fileH.n != claim.FileSize {
		return "", 0, fmt.Errorf("FileHash/FileSize mismatch")
	}
	// 3. Copy the verified bytes to the served prefix. The ETag guard makes sure a worker
	//    cannot swap the staged object between verification and copy.
	dVerify := time.Since(tVerify)
	tCopy := time.Now()
	if err := s.st.Copy(ctx, src, store.NarKey(h), etag, "application/zstd"); err != nil {
		return "", 0, fmt.Errorf("copy NAR: %w", err)
	}
	log.Printf("promote %s: nar=%dB verify=%s copy=%s", h, narH.n, dVerify.Round(time.Millisecond), time.Since(tCopy).Round(time.Millisecond))
	// 4. Listing (optional, used by `nix store ls --store <cache>`).
	ls, _ := json.Marshal(map[string]any{"version": 1, "root": root})
	if err := s.st.PutSmall(ctx, store.LsKey(h), ls, "application/json", false); err != nil {
		return "", 0, err
	}
	// 5. Visibility marker before narinfo (narinfo is what makes a path exist).
	if err := s.markVisible(ctx, h, scopes); err != nil {
		return "", 0, err
	}
	// 6. Sign and write the narinfo LAST, create-only.
	ni := *claim
	ni.URL = "nar/" + h + ".nar.zst"
	ni.Sigs = []string{s.key.Sign(&ni)}
	err = s.st.PutSmall(ctx, store.NarInfoKey(h), []byte(ni.Format()), "text/x-nix-narinfo", true)
	if errors.Is(err, store.ErrPrecondition) {
		return "exists", 0, nil // a concurrent promotion of the same path won
	}
	return "promoted", narH.n, err
}

func (s *server) markVisible(ctx context.Context, h string, scopes []string) error {
	for _, sc := range scopes {
		if err := s.st.PutSmall(ctx, store.NixVisKey(h, sc), nil, "", false); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) promoteImage(ctx context.Context, build string, im stagedImage, scopes []string) error {
	if !reRepo.MatchString(im.Repo) || len(im.Repo) > 255 || !reTag.MatchString(im.Tag) {
		return errors.New("bad repo or tag")
	}
	var digests []string // manifest last
	for _, d := range im.Blobs {
		if d != im.Manifest {
			digests = append(digests, d)
		}
	}
	digests = append(digests, im.Manifest)
	for _, d := range digests {
		hx, ok := strings.CutPrefix(d, "sha256:")
		if !ok || !reHex.MatchString(hx) {
			return fmt.Errorf("bad digest %q", d)
		}
		dst := store.BlobKey(hx)
		if ok, err := s.st.Exists(ctx, dst); err != nil {
			return err
		} else if !ok {
			src := store.StagingBlob(build, hx)
			obj, err := s.st.Get(ctx, src, "")
			if err != nil {
				return fmt.Errorf("staged blob %s: %w", d, err)
			}
			hh := sha256.New()
			n, err := io.Copy(hh, io.LimitReader(obj.Body, maxBlobSize))
			obj.Body.Close()
			if err != nil {
				return err
			}
			if hex.EncodeToString(hh.Sum(nil)) != hx {
				return fmt.Errorf("blob %s: digest mismatch (%d bytes)", d, n)
			}
			ct := ""
			if obj.ContentType != nil {
				ct = *obj.ContentType
			}
			if d == im.Manifest {
				ct = im.MediaType
			}
			if err := s.st.Copy(ctx, src, dst, *obj.ETag, ct); err != nil {
				return err
			}
		}
		if err := s.st.PutSmall(ctx, store.RepoBlobKey(im.Repo, hx), nil, "", false); err != nil {
			return err
		}
	}
	for _, sc := range scopes {
		if err := s.st.PutSmall(ctx, store.RepoVisKey(im.Repo, sc), nil, "", false); err != nil {
			return err
		}
	}
	// Tag last: the tag is what makes the image pullable by name.
	return s.st.PutSmall(ctx, store.RepoTagKey(im.Repo, im.Tag), []byte(im.Manifest), "text/plain", false)
}
