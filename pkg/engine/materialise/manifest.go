// Package materialise puts build outputs into the working tree (SPEC §03.6.6, R-FW-007,
// R-FW-066, R-FW-067): codegen output directories and an npm importer's node_modules
// (§03.4.3). Every owned directory is recorded in .bob/gen/manifest.json with the sha256 of each
// file bob wrote, so bob never overwrites a file it did not write or that was edited since
// (BOB-E-1016, `E-GEN-CONFLICT`), and an interrupted write is finished or undone by the next run.
package materialise

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ManifestPath is the manifest, relative to the repository root.
const ManifestPath = ".bob/gen/manifest.json"

// maxManifest caps the manifest read (hostile input: it lives in the working tree).
const maxManifest = 64 << 20

// Manifest is .bob/gen/manifest.json (SPEC §03.6.6).
type Manifest struct {
	Version int                     `json:"version"` // 1
	Roots   map[string]ManifestRoot `json:"roots"`   // key: owned dir, repo-relative
}

// ManifestRoot is one owned directory.
type ManifestRoot struct {
	Label         string                  `json:"label"`
	Source        string                  `json:"source"`        // store path of the output
	Files         map[string]ManifestFile `json:"files"`         // path relative to the root
	PendingSource string                  `json:"pendingSource"` // set during a write
	Pending       map[string]ManifestFile `json:"pending"`
}

// ManifestFile is one owned file. A symlink (an npm importer's node_modules is a directory of
// symlinks into the store) records its target in Link and the sha256 of "symlink:<target>".
type ManifestFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
	MTime  int64  `json:"mtime"` // ns; size+mtime only enable the no-hash fast path
	Link   string `json:"link,omitempty"`
}

// readManifest reads the manifest of repo. A missing file is an empty manifest; entries with an
// invalid path are dropped (their files then count as foreign: never overwritten).
func readManifest(repo string) (*Manifest, error) {
	m := &Manifest{Version: 1, Roots: map[string]ManifestRoot{}}
	f, err := os.Open(filepath.Join(repo, filepath.FromSlash(ManifestPath)))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("materialise: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return nil, fmt.Errorf("materialise: %w", err)
	}
	var raw Manifest
	if len(b) > maxManifest || json.Unmarshal(b, &raw) != nil || raw.Version != 1 {
		// A damaged manifest owns nothing: every file on disk becomes foreign, none is lost.
		return m, nil
	}
	for dir, r := range raw.Roots {
		if !validDir(dir) {
			continue
		}
		r.Files = validFiles(r.Files)
		r.Pending = validFiles(r.Pending)
		m.Roots[dir] = r
	}
	return m, nil
}

func validFiles(in map[string]ManifestFile) map[string]ManifestFile {
	out := map[string]ManifestFile{}
	for p, f := range in {
		if validRel(p) && len(f.SHA256) == 64 {
			out[p] = f
		}
	}
	return out
}

// validRel reports whether p is a clean, relative, slash-separated path below its base.
func validRel(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." &&
		!strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00") && len(p) <= 4096
}

// validDir reports whether dir may be an owned directory: a clean relative path that is not
// inside .git or .bob (SPEC §03.6.6).
func validDir(dir string) bool {
	if !validRel(dir) {
		return false
	}
	first, _, _ := strings.Cut(dir, "/")
	return first != ".git" && first != ".bob"
}

// writeManifest writes m atomically (temp file, fsync, rename, fsync of the directory).
func writeManifest(repo string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	return writeAtomic(filepath.Join(repo, filepath.FromSlash(ManifestPath)), append(b, '\n'), 0o644)
}

// writeAtomic writes data to p through a temp file in the same directory.
func writeAtomic(p string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("materialise: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("materialise: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	ok = true
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("materialise: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("materialise: %w", err)
	}
	return nil
}
