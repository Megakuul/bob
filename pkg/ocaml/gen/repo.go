package gen

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/ocaml/resolve"
)

// The opam-repository snapshot (SPEC §03.5.2): a GitHub tarball of one commit, imported by
// bob's fetcher (narHash in bob.lock) and read by the planner; bob lock resolves against an
// extracted copy of its opam files in the user cache.
const (
	// DefaultCommit is the opam-repository commit a new bob.lock pins (S5's snapshot).
	DefaultCommit = "af5ddb0addf6e75660ee9d7485b27425459d1d66"
	// RepositoryURL is what bob.lock records as the repository.
	RepositoryURL = "github:ocaml/opam-repository"
	// repoEnv names a snapshot tarball <url>/<commit>.tar.gz to use instead of GitHub (test
	// mirrors, tools/regmirror); bob.lock still records the GitHub repository and the commit.
	repoEnv = "BOB_OPAM_REPOSITORY"
	// cacheEnv is an opam archive cache (<url>/<alg>/<first two hex>/<hex>) tried before the
	// package's own URL.
	cacheEnv = "BOB_OPAM_CACHE_URL"
	// opamCache is opam's public archive cache, the last mirror of every source.
	opamCache = "https://opam.ocaml.org/cache"
)

// Bounds of the snapshot download (hostile input).
const (
	maxSnapshotBytes = 1 << 30 // the compressed tarball
	maxOpamFile      = 1 << 20
	maxOpamFiles     = 100000
	maxOpamTotal     = 512 << 20
)

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// snapshot is one opam-repository commit and where its tarball is downloaded from.
type snapshot struct {
	commit string
	url    string
}

// snapshotFor picks the snapshot: the mirror of BOB_OPAM_REPOSITORY when set, else commit
// (bob.lock's, or DefaultCommit) from GitHub.
func snapshotFor(commit string) (snapshot, error) {
	if u := strings.TrimSpace(os.Getenv(repoEnv)); u != "" {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") {
			return snapshot{}, fmt.Errorf("%s=%q is not an http(s) URL", repoEnv, u)
		}
		c := strings.TrimSuffix(path.Base(pu.Path), ".tar.gz")
		if !hex40.MatchString(c) {
			return snapshot{}, fmt.Errorf("%s=%q does not name a <commit>.tar.gz snapshot", repoEnv, u)
		}
		return snapshot{commit: c, url: u}, nil
	}
	if commit == "" {
		commit = DefaultCommit
	}
	return githubSnapshot(commit), nil
}

// snapshotOf is the snapshot of a locked repository: the mirror when it serves that commit.
func snapshotOf(r lock.OpamRepository) snapshot {
	if s, err := snapshotFor(""); err == nil && os.Getenv(repoEnv) != "" && s.commit == r.Commit {
		return s
	}
	return githubSnapshot(r.Commit)
}

func githubSnapshot(commit string) snapshot {
	return snapshot{commit: commit, url: "https://github.com/ocaml/opam-repository/archive/" + commit + ".tar.gz"}
}

// request is the fetcher request of the unpacked snapshot (narHash "" at bob lock).
func (s snapshot) request(narHash string) fetch.Request {
	return fetch.Request{URLs: []string{s.url}, Kind: fetch.Archive, NarHash: narHash, Strip: 1,
		Name: "opam-repository-" + s.commit[:12], Label: "opam-repository@" + s.commit[:12]}
}

// cacheBase is $XDG_CACHE_HOME/bob/opam-repository ("" when there is no home).
func cacheBase() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return ""
		}
		base = filepath.Join(h, ".cache")
	}
	return filepath.Join(base, "bob", "opam-repository")
}

// opamFiles returns a directory holding packages/<name>/<name>.<version>/opam of the snapshot:
// the user cache's extracted copy, downloaded and extracted on first use.
func opamFiles(ctx context.Context, s snapshot) (string, error) {
	base := cacheBase()
	if base == "" {
		return "", errors.New("opam repository: no cache directory (set XDG_CACHE_HOME or HOME)")
	}
	dir := filepath.Join(base, s.commit)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("opam repository cache: %w", err)
	}
	tmp, err := os.MkdirTemp(base, ".tmp-"+s.commit[:12]+"-")
	if err != nil {
		return "", fmt.Errorf("opam repository cache: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := download(ctx, s.url, tmp); err != nil {
		return "", fmt.Errorf("opam repository %s: %w", s.url, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir) // a partial copy of an earlier run
	if err := os.Rename(tmp, dir); err != nil {
		if _, serr := os.Stat(filepath.Join(dir, ".complete")); serr == nil {
			return dir, nil // a parallel bob lock finished first
		}
		return "", fmt.Errorf("opam repository cache: %w", err)
	}
	return dir, nil
}

// download streams the tarball at u and extracts its opam files into dir.
func download(ctx context.Context, u, dir string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, "packages")); err != nil { // a failed attempt's files
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		err = func() error {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s", resp.Status)
			}
			return extractOpamFiles(io.LimitReader(resp.Body, maxSnapshotBytes), dir)
		}()
		if err == nil {
			return nil
		}
		last = err
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return err
		}
	}
	return last
}

// extractOpamFiles writes the regular files <top>/packages/<name>/<name>.<version>/opam of a
// gzipped tarball below dir; everything else is skipped. Names are validated, sizes bounded.
func extractOpamFiles(r io.Reader, dir string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	files, total := 0, int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(h.Name, "/")
		if len(parts) != 5 || parts[1] != "packages" || parts[4] != "opam" {
			continue
		}
		name, ver, ok := strings.Cut(parts[3], ".")
		if !ok || name != parts[2] || !resolve.ValidName(name) || !resolve.ValidVersion(ver) {
			continue
		}
		if h.Size > maxOpamFile {
			continue // the resolver treats an oversized opam file as unparseable anyway
		}
		if files++; files > maxOpamFiles {
			return fmt.Errorf("more than %d opam files", maxOpamFiles)
		}
		if total += h.Size; total > maxOpamTotal {
			return fmt.Errorf("opam files larger than %d bytes in total", maxOpamTotal)
		}
		d := filepath.Join(dir, "packages", name, parts[3])
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(d, "opam"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, maxOpamFile))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if files == 0 {
		return errors.New("the tarball holds no opam files")
	}
	return nil
}

// readOpam reads one opam file of a snapshot directory and checks its digest against the lock.
func readOpam(dir, name, version, digest string) ([]byte, error) {
	if !resolve.ValidName(name) || !resolve.ValidVersion(version) {
		return nil, fmt.Errorf("opam package %q.%q: invalid name", name, version)
	}
	p := filepath.Join(dir, "packages", name, name+"."+version, "opam")
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, fmt.Errorf("opam:%s.%s is not in the pinned opam repository: %w", name, version, err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxOpamFile {
		return nil, fmt.Errorf("opam:%s.%s: the opam file is not a regular file of at most %d bytes", name, version, maxOpamFile)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("opam:%s.%s: the opam file does not match bob.lock (run bob lock)", name, version)
	}
	return b, nil
}
