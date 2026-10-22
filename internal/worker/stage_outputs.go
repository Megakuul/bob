package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/registry"
	"github.com/megakuul/bob/internal/safe"
)

// Bounds of the image and release metadata the worker reads from built outputs (written by
// the job, so hostile like everything out of a container).
const (
	maxOutputJSON  = 4 << 20 // index.json, manifests, configs, release.json
	maxImageBlobs  = 4096    // objects of one image (promote.ParseComplete's limit)
	maxImages      = 1024    // images and assets of one job (COMPLETE.json limits)
	maxSymlinkHops = 8
)

var assetFileRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,255}$`)

// outStager stages the OCI images and release assets of one job's outputs (SPEC R-OUT-013,
// §6.6.4: <staging>oci/blobs/sha256/<hex>, <staging>assets/<file>). It runs in the worker
// process after the container is gone and reads the outputs from the worker's store on the
// host; every file it reads must resolve inside a store path of the job's closure.
type outStager struct {
	p       *Process
	staging string
	project string
	root    string          // <store>/store, symlinks resolved
	closure map[string]bool // store path names (<hash>-<name>) of the job's closure

	mu     sync.Mutex
	staged map[string]bool // blob digests uploaded by this job
	done   *promote.Complete
}

// stageOutputs lists the images and release assets of res in done and uploads their objects.
// A release output (release.json) contributes its images (tagged with the release version)
// and its AppImage and tarball files; any other image output is staged for a build tag in the
// repository <project>/<package name>.
func (p *Process) stageOutputs(ctx context.Context, staging, project string, res RunResult, done *promote.Complete) error {
	if len(res.Outputs) == 0 {
		return nil
	}
	root, err := filepath.EvalSymlinks(filepath.Join(p.store, "store"))
	if err != nil {
		return err
	}
	s := &outStager{p: p, staging: staging, project: project, root: root, closure: map[string]bool{},
		staged: map[string]bool{}, done: done}
	for _, pi := range res.Paths {
		s.closure[strings.TrimPrefix(pi.Path, cache.StoreDir+"/")] = true
	}
	var plain []BuiltOutput
	for _, o := range res.Outputs {
		dir, err := s.dir(o.Path)
		if err != nil {
			return err
		}
		switch {
		case isRegular(filepath.Join(dir, "release.json")):
			if err := s.release(ctx, o, dir); err != nil {
				return fmt.Errorf("release %s: %w", o.Label, err)
			}
		case isRegular(filepath.Join(dir, "oci-layout")) && isRegular(filepath.Join(dir, "index.json")):
			plain = append(plain, o)
		}
	}
	// plain images after the releases: an image a release lists already is not staged twice
	for _, o := range plain {
		repo := imageRepo(project, o.Label)
		if repo == "" {
			continue // no valid repository name: the image stays a cache path only
		}
		dir, _ := s.dir(o.Path)
		if err := s.image(ctx, dir, repo, "", ""); err != nil {
			return fmt.Errorf("image %s: %w", o.Label, err)
		}
	}
	if len(done.Images) > maxImages || len(done.Assets) > maxImages {
		return fmt.Errorf("more than %d images or assets", maxImages)
	}
	return nil
}

// imageRepo is the repository of a plain (non-release) image: <project>/<package name> in
// lower case, or "" when that is not a valid repository name.
func imageRepo(project, label string) string {
	name := label
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	name, _, _ = strings.Cut(name, "@")
	repo := project + "/" + strings.ToLower(name)
	if !registry.ValidRepo(repo) {
		return ""
	}
	return repo
}

func isRegular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// dir is the host directory of a store path of the closure.
func (s *outStager) dir(storePath string) (string, error) {
	if !cache.ValidStorePath(storePath) {
		return "", fmt.Errorf("output path %.300q", storePath)
	}
	name := strings.TrimPrefix(storePath, cache.StoreDir+"/")
	if !s.closure[name] {
		return "", fmt.Errorf("output %s is not in the job's closure", storePath)
	}
	return filepath.Join(s.root, name), nil
}

// resolve follows symlinks (relative ones, and absolute ones into /nix/store) from p to a
// regular file that lies inside a store path of the job's closure. Anything else (a link out
// of the store, to a path the job did not build, a directory) is refused: the worker holds
// credentials and must not upload a host file a hostile build pointed at.
func (s *outStager) resolve(p string) (string, os.FileInfo, error) {
	for hop := 0; hop <= maxSymlinkHops; hop++ {
		if err := s.inside(p); err != nil {
			return "", nil, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return "", nil, err
		}
		switch {
		case fi.Mode().IsRegular():
			real, err := filepath.EvalSymlinks(p) // intermediate directories may be links too
			if err != nil {
				return "", nil, err
			}
			if err := s.inside(real); err != nil {
				return "", nil, err
			}
			return real, fi, nil
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return "", nil, err
			}
			if filepath.IsAbs(t) {
				rest, ok := strings.CutPrefix(t, cache.StoreDir+"/")
				if !ok {
					return "", nil, fmt.Errorf("%s links outside the store", s.rel(p))
				}
				p = filepath.Join(s.root, rest)
			} else {
				p = filepath.Join(filepath.Dir(p), t)
			}
		default:
			return "", nil, fmt.Errorf("%s is not a regular file", s.rel(p))
		}
	}
	return "", nil, fmt.Errorf("%s: too many symbolic links", s.rel(p))
}

// inside checks that p is below a store path of the closure.
func (s *outStager) inside(p string) error {
	rel, err := filepath.Rel(s.root, filepath.Clean(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("%s is outside the store", p)
	}
	top, _, _ := strings.Cut(rel, "/")
	if !s.closure[top] {
		return fmt.Errorf("%s is not in the job's closure", cache.StoreDir+"/"+top)
	}
	return nil
}

func (s *outStager) rel(p string) string {
	if rel, err := filepath.Rel(s.root, p); err == nil {
		return cache.StoreDir + "/" + rel
	}
	return p
}

// readJSON reads a bounded metadata file of an output.
func (s *outStager) readJSON(p string) ([]byte, error) {
	real, fi, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxOutputJSON {
		return nil, fmt.Errorf("%s is larger than %d bytes", s.rel(p), maxOutputJSON)
	}
	return readCapped(real, maxOutputJSON)
}

// releaseManifest is the part of release.json (bob.release/v1, R-OUT-023) the worker stages
// from. The orchestrator verifies the whole manifest again from the promoted output.
type releaseManifest struct {
	Project   string `json:"project"`
	Version   string `json:"version"`
	Artifacts []struct {
		Kind      string   `json:"kind"`
		Name      string   `json:"name"`
		StorePath string   `json:"storePath"`
		File      string   `json:"file"`
		SHA256    string   `json:"sha256"`
		Size      int64    `json:"size"`
		Repo      string   `json:"repo"`
		Index     string   `json:"index"`
		Tags      []string `json:"tags"`
	} `json:"artifacts"`
}

// release stages the images and asset files a release output lists.
func (s *outStager) release(ctx context.Context, o BuiltOutput, dir string) error {
	b, err := s.readJSON(filepath.Join(dir, "release.json"))
	if err != nil {
		return err
	}
	var m releaseManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("release.json: %w", err)
	}
	if m.Project != "" && m.Project != s.project {
		return fmt.Errorf("release.json belongs to project %.100q, not %s", m.Project, s.project)
	}
	if !registry.ValidTag(m.Version) {
		return fmt.Errorf("release.json: invalid version %.80q", m.Version)
	}
	if len(m.Artifacts) > maxImages {
		return fmt.Errorf("release.json lists more than %d artifacts", maxImages)
	}
	for _, a := range m.Artifacts {
		switch a.Kind {
		case "oci":
			repo := releaseRepo(m.Project, s.project, a.Repo)
			if !registry.ValidRepo(repo) || registry.RepoProject(repo) != s.project || !registry.ValidDigest(a.Index) {
				return fmt.Errorf("image %s: repository %.300q or index %.80q", a.Name, repo, a.Index)
			}
			idir, err := s.dir(a.StorePath)
			if err != nil {
				return err
			}
			if err := s.image(ctx, idir, repo, m.Version, a.Index); err != nil {
				return fmt.Errorf("image %s: %w", a.Name, err)
			}
		case "appimage", "tarball":
			if err := s.asset(ctx, a.Kind, a.StorePath, a.File, a.SHA256, a.Size); err != nil {
				return fmt.Errorf("%s %s: %w", a.Kind, a.Name, err)
			}
		}
	}
	return nil
}

// releaseRepo is the registry repository of a release image: release.json names it relative to
// the project when the release was built without a server project ("" project, decision
// D-IB20-1), so the server project is put in front.
func releaseRepo(manifestProject, project, repo string) string {
	if manifestProject == "" {
		return project + "/" + repo
	}
	return repo
}

// image stages the OCI image layout in dir: every object reachable from index.json's single
// image index (index, manifests, configs, layers), each verified against its digest while it
// is uploaded. want, when set, is the index digest the release manifest names.
func (s *outStager) image(ctx context.Context, dir, repo, tag, want string) error {
	top, err := s.readJSON(filepath.Join(dir, "index.json"))
	if err != nil {
		return err
	}
	var layout struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(top, &layout); err != nil || len(layout.Manifests) != 1 || !registry.ValidDigest(layout.Manifests[0].Digest) {
		return errors.New("index.json does not name one image index")
	}
	index := layout.Manifests[0].Digest
	if want != "" && index != want {
		return fmt.Errorf("index.json names %s, the release manifest %s", index, want)
	}
	for _, im := range s.done.Images {
		if im.Repo == repo && im.Manifest == index {
			return nil
		}
	}
	blobs := filepath.Join(dir, "blobs", "sha256")
	var order []string
	seen := map[string]bool{}
	var media string
	var walk func(d string, depth int) error
	walk = func(d string, depth int) error {
		if seen[d] {
			return nil
		}
		seen[d] = true
		order = append(order, d)
		if len(order) > maxImageBlobs {
			return fmt.Errorf("more than %d objects", maxImageBlobs)
		}
		if depth > 2 {
			return fmt.Errorf("image index nesting too deep at %s", d)
		}
		body, err := s.readJSON(filepath.Join(blobs, strings.TrimPrefix(d, "sha256:")))
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(body); "sha256:"+hex.EncodeToString(sum[:]) != d {
			return fmt.Errorf("blob %s does not match its digest", d)
		}
		var m struct {
			MediaType string `json:"mediaType"`
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
			Config *struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("manifest %s: %w", d, err)
		}
		if depth == 0 {
			media = m.MediaType
		}
		leaf := func(x string) error {
			if !registry.ValidDigest(x) {
				return fmt.Errorf("manifest %s references %.80q", d, x)
			}
			if !seen[x] {
				seen[x] = true
				order = append(order, x)
			}
			return nil
		}
		for _, c := range m.Manifests {
			if !registry.ValidDigest(c.Digest) {
				return fmt.Errorf("index %s references %.80q", d, c.Digest)
			}
			if err := walk(c.Digest, depth+1); err != nil {
				return err
			}
		}
		if m.Config != nil {
			if err := leaf(m.Config.Digest); err != nil {
				return err
			}
		}
		for _, l := range m.Layers {
			if err := leaf(l.Digest); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(index, 0); err != nil {
		return err
	}
	if len(order) > maxImageBlobs {
		return fmt.Errorf("more than %d objects", maxImageBlobs)
	}
	if media == "" {
		media = "application/vnd.oci.image.index.v1+json"
	}
	sem := make(chan struct{}, 4)
	var runs []<-chan error
	for _, d := range order {
		sem <- struct{}{}
		runs = append(runs, safe.Go(func() error {
			defer func() { <-sem }()
			return s.blob(ctx, filepath.Join(blobs, strings.TrimPrefix(d, "sha256:")), d)
		}))
	}
	var errs []error
	for _, r := range runs {
		if err := <-r; err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	rest := slices.Clone(order[1:])
	slices.Sort(rest)
	s.done.Images = append(s.done.Images, promote.Image{Repo: repo, Tag: tag, Manifest: index, MediaType: media, Blobs: rest})
	return nil
}

// blob uploads one image object to <staging>oci/blobs/sha256/<hex> (once per job).
func (s *outStager) blob(ctx context.Context, p, digest string) error {
	s.mu.Lock()
	if s.staged[digest] {
		s.mu.Unlock()
		return nil
	}
	s.staged[digest] = true
	s.mu.Unlock()
	hx := strings.TrimPrefix(digest, "sha256:")
	_, _, err := s.upload(ctx, p, s.staging+"oci/blobs/sha256/"+hx, "application/octet-stream", hx, -1)
	if err != nil {
		s.mu.Lock()
		delete(s.staged, digest)
		s.mu.Unlock()
		return fmt.Errorf("blob %s: %w", digest, err)
	}
	return nil
}

// asset stages the AppImage or tarball file of a release artifact as <staging>assets/<file>:
// the one file of its kind in the artifact's output, published under the versioned name of
// the release manifest, with the manifest's sha256 and size.
func (s *outStager) asset(ctx context.Context, kind, storePath, file, sum string, size int64) error {
	if !assetFileRE.MatchString(file) || file == "." || file == ".." || len(sum) != 64 || size <= 0 {
		return fmt.Errorf("invalid file %.300q", file)
	}
	for _, a := range s.done.Assets {
		if a.File == file {
			if a.SHA256 != sum || a.Size != size {
				return fmt.Errorf("file %s is listed twice with different contents", file)
			}
			return nil
		}
	}
	dir, err := s.dir(storePath)
	if err != nil {
		return err
	}
	ext := ".tar.gz"
	if kind == "appimage" {
		ext = ".AppImage"
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var match []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ext) {
			match = append(match, e.Name())
		}
	}
	if len(match) != 1 {
		return fmt.Errorf("want exactly one *%s in %s, found %d", ext, storePath, len(match))
	}
	got, n, err := s.upload(ctx, filepath.Join(dir, match[0]), s.staging+"assets/"+file, "application/octet-stream", sum, size)
	if err != nil {
		return err
	}
	s.done.Assets = append(s.done.Assets, promote.Asset{File: file, SHA256: got, Size: n})
	return nil
}

// upload streams the file p (resolved inside the closure) to key while hashing it; the hash
// must be wantHex (and the size wantSize, unless -1). It returns the hash and the size.
func (s *outStager) upload(ctx context.Context, p, key, contentType, wantHex string, wantSize int64) (string, int64, error) {
	real, fi, err := s.resolve(p)
	if err != nil {
		return "", 0, err
	}
	if wantSize >= 0 && fi.Size() != wantSize {
		return "", 0, fmt.Errorf("%s has %d bytes, want %d", s.rel(p), fi.Size(), wantSize)
	}
	f, err := os.Open(real)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hr := &hashReader{f: f, h: sha256.New(), size: fi.Size()}
	if err := s.p.cfg.Artifacts.PutFile(ctx, key, hr, fi.Size(), contentType); err != nil {
		return "", 0, &uploadError{fmt.Errorf("upload %s: %w", key, err)}
	}
	got, ok := hr.sum()
	if !ok {
		return "", 0, fmt.Errorf("upload %s: the file was not read completely", key)
	}
	if got != wantHex {
		return "", 0, fmt.Errorf("%s hashes to sha256:%s, want sha256:%s", s.rel(p), got, wantHex)
	}
	return got, fi.Size(), nil
}

// uploadError is a failed upload (infrastructure: the next attempt retries it); every other
// staging error of the outputs is the job's own (its outputs are not what bob builds).
type uploadError struct{ err error }

func (e *uploadError) Error() string { return e.err.Error() }
func (e *uploadError) Unwrap() error { return e.err }

// hashReader hashes what the uploader reads; a seek to the start (a retry, or the SDK's
// payload signing pass) starts the hash again, so the hash of the last complete read is the
// hash of the bytes sent.
type hashReader struct {
	f        *os.File
	h        hash.Hash
	n, size  int64
	complete []byte
}

func (r *hashReader) Read(b []byte) (int, error) {
	n, err := r.f.Read(b)
	r.h.Write(b[:n])
	r.n += int64(n)
	if n > 0 && r.n == r.size {
		r.complete = r.h.Sum(nil)
	}
	return n, err
}

func (r *hashReader) Seek(off int64, whence int) (int64, error) {
	cur, err := r.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	switch {
	case whence == io.SeekStart && off == 0:
		r.h.Reset()
		r.n = 0
	case whence == io.SeekCurrent && off == 0:
		return cur, nil
	case whence == io.SeekEnd && off == 0:
		// a length probe: the position is restored by the next seek to the start
	default:
		return 0, fmt.Errorf("hashReader: unsupported seek (%d, %d)", off, whence)
	}
	return r.f.Seek(off, whence)
}

// sum returns the hash of the last complete read.
func (r *hashReader) sum() (string, bool) {
	if r.complete == nil {
		if r.size != 0 {
			return "", false
		}
		r.complete = r.h.Sum(nil) // an empty file is never read
	}
	return hex.EncodeToString(r.complete), true
}
