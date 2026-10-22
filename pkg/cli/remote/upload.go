package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/engine/srcs"
)

// MaxSnapshot is the size cap of a compressed source snapshot (SPEC §6.9.7, O-06-2).
const MaxSnapshot = 512 << 20

// SourceFiles lists the files of a source snapshot (SPEC §02.6 step 2): what git tracks plus
// untracked files git does not ignore, relative to root (slash-separated). Outside a git work
// tree every file is taken. .git/ and .bob/ are never part of it.
func SourceFiles(ctx context.Context, root string) ([]string, error) {
	var files []string
	if git, err := exec.LookPath("git"); err == nil && insideWorkTree(ctx, git, root) {
		cmd := exec.CommandContext(ctx, git, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		cmd.Env = gitEnv()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("source snapshot: git ls-files: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			if d.IsDir() {
				if rel != "." && (d.Name() == ".git" || d.Name() == ".bob") {
					return filepath.SkipDir
				}
				return nil
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("source snapshot: %w", err)
		}
	}
	out := files[:0]
	for _, f := range files {
		if f == ".bob" || strings.HasPrefix(f, ".bob/") || strings.HasPrefix(f, ".git/") || strings.Contains(f, "/.git/") {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func insideWorkTree(ctx context.Context, git, root string) bool {
	cmd := exec.CommandContext(ctx, git, "-C", root, "rev-parse", "--is-inside-work-tree")
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// gitEnv is the caller's environment without GIT_* variables that would point git at another
// repository.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") || strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// Snapshot is a packed source snapshot in a temporary file.
type Snapshot struct {
	Path    string
	SHA256  string // hex of the compressed bytes
	Size    int64
	Entries int
}

// Remove deletes the temporary file.
func (s *Snapshot) Remove() { _ = os.Remove(s.Path) }

// PackSource writes the snapshot of root to a temporary file and hashes it.
func PackSource(ctx context.Context, root string) (*Snapshot, error) {
	files, err := SourceFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "bob-snapshot-*.tar.zst")
	if err != nil {
		return nil, fmt.Errorf("source snapshot: %w", err)
	}
	snap := &Snapshot{Path: f.Name()}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	n, err := srcs.Snapshot(ctx, root, files, cw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		snap.Remove()
		return nil, err
	}
	snap.SHA256, snap.Size, snap.Entries = hex.EncodeToString(h.Sum(nil)), cw.n, n
	if snap.Size > MaxSnapshot {
		snap.Remove()
		return nil, fmt.Errorf("the source snapshot is %d MiB; a bob server accepts at most %d MiB", snap.Size>>20, MaxSnapshot>>20)
	}
	return snap, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// UploadMethod names the upload in errors.
const UploadMethod = "PUT /upload/source"

// Upload sends the snapshot with PUT /upload/source/sha256/<hex> (SPEC §6.9.7). The request
// carries `Expect: 100-continue`: a server that already has the hash answers 200 before the
// body is sent.
func (s *Server) Upload(ctx context.Context, project string, snap *Snapshot) error {
	f, err := os.Open(snap.Path)
	if err != nil {
		return fmt.Errorf("source snapshot: %w", err)
	}
	defer f.Close()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPut, s.URL+"/upload/source/sha256/"+snap.SHA256, f)
	if err != nil {
		return &Error{Method: UploadMethod, Transport: err}
	}
	hr.ContentLength = snap.Size
	hr.Header.Set("Content-Type", "application/zstd")
	hr.Header.Set("Expect", "100-continue")
	hr.Header.Set("Bob-Project", project)
	if s.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+s.Token)
	}
	if s.UserAgent != "" {
		hr.Header.Set("User-Agent", s.UserAgent)
	}
	client := s.HTTP
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ExpectContinueTimeout = 2 * time.Second
		client = &http.Client{Transport: tr, Timeout: 30 * time.Minute}
	}
	r, err := client.Do(hr)
	if err != nil {
		return &Error{Method: UploadMethod, Transport: err}
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if r.StatusCode/100 == 2 {
		return nil
	}
	e := httpError(UploadMethod, r.StatusCode, b)
	if r.StatusCode == http.StatusRequestEntityTooLarge {
		e.Message = "the source snapshot is larger than the server accepts"
	}
	return e
}
