// Package protected keeps the hash manifest of the protected paths and
// reports changes against it (HARNESS.md §2.1, AGENTS.md §5.2).
//
// Changes are reported, never blocked (owner decision D68): the CI stage
// prints them and goes on.
package protected

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestPath is the manifest, relative to the repository root.
const ManifestPath = "tests/protected.sha256"

// Marker in the package doc of a *_test.go file outside the globs makes it
// protected (acceptance tests such as pkg/layering_test.go).
const Marker = "//bob:protected"

// DefaultGlobs is the protected set of HARNESS.md §2.1. It seeds a new
// manifest; afterwards the manifest header is the source of truth, so a
// change to the set is itself a reported manifest change.
var DefaultGlobs = []string{
	"tests/**",
	"!tests/*/scratch/**",
	"pkg/diag/codes.lock",
	"tools/ci/**",
	"tools/lint/**",
	"tools/s3count/**",
	"tools/golden/**",
	"tools/pins.json",
	"tools/go.mod",
	"tools/go.sum",
	"schema/events-v1.json",
}

// skipDirs are never walked: VCS data, dependency trees, build output and
// the throwaway spikes.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "out": true, "target": true, "spikes": true, ".bob": true}

// Manifest is the parsed manifest: the glob header and the hashes.
type Manifest struct {
	Globs  []string
	Hashes map[string]string // path → hex sha256
}

// Parse reads a manifest. Header lines are "# glob <pattern>"; other '#'
// lines are comments; data lines are "<sha256>  <path>".
func Parse(r io.Reader) (*Manifest, error) {
	m := &Manifest{Hashes: map[string]string{}}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# glob "):
			m.Globs = append(m.Globs, strings.TrimSpace(strings.TrimPrefix(line, "# glob ")))
		case strings.HasPrefix(line, "#"), strings.TrimSpace(line) == "":
		default:
			sum, p, ok := strings.Cut(line, "  ")
			if !ok || len(sum) != 64 {
				return nil, fmt.Errorf("manifest line %d: want \"<sha256>  <path>\", got %q", n, line)
			}
			m.Hashes[p] = sum
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return m, nil
}

// Format renders the manifest, sorted, in the form Parse reads.
func (m *Manifest) Format() []byte {
	var b bytes.Buffer
	b.WriteString("# bob protected-path manifest (HARNESS.md §2.1). Changes are reported by CI, not blocked (D68).\n")
	b.WriteString("# Regenerate with: go run ./tools/ci protect -write\n")
	for _, g := range m.Globs {
		fmt.Fprintf(&b, "# glob %s\n", g)
	}
	paths := make([]string, 0, len(m.Hashes))
	for p := range m.Hashes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(&b, "%s  %s\n", m.Hashes[p], p)
	}
	return b.Bytes()
}

// Match reports whether slash path p matches the glob pattern; "**"
// matches any number of path segments, other segments use path.Match.
func Match(pattern, p string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && matchSegs(pat[1:], segs[1:])
}

// Protected reports whether p is in the set described by globs ("!" globs
// exclude). The manifest never lists itself.
func Protected(globs []string, p string) bool {
	if p == ManifestPath {
		return false
	}
	in := false
	for _, g := range globs {
		if ex, ok := strings.CutPrefix(g, "!"); ok {
			if Match(ex, p) {
				return false
			}
		} else if Match(g, p) {
			in = true
		}
	}
	return in
}

// Hash computes the current manifest of root for the given globs.
func Hash(ctx context.Context, root string, globs []string) (*Manifest, error) {
	m := &Manifest{Globs: globs, Hashes: map[string]string{}}
	err := filepath.WalkDir(root, func(full string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() {
			if full != root && skipDirs[e.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		in := Protected(globs, rel)
		if !in && strings.HasSuffix(rel, "_test.go") {
			if in, err = hasMarker(full); err != nil {
				return err
			}
		}
		if !in {
			return nil
		}
		sum, err := hashFile(full, e)
		if err != nil {
			return err
		}
		m.Hashes[rel] = sum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hash protected paths: %w", err)
	}
	return m, nil
}

// hasMarker looks for the marker in the lines before the package clause.
func hasMarker(file string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == Marker {
			return true, nil
		}
		if strings.HasPrefix(line, "package ") {
			return false, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("read %s: %w", file, err)
	}
	return false, nil
}

// hashFile hashes the content, or the target of a symlink, so a fixture
// swapped for a link is reported.
func hashFile(full string, e fs.DirEntry) (string, error) {
	h := sha256.New()
	if e.Type()&fs.ModeSymlink != 0 {
		t, err := os.Readlink(full)
		if err != nil {
			return "", fmt.Errorf("readlink %s: %w", full, err)
		}
		io.WriteString(h, "symlink:"+t)
	} else {
		f, err := os.Open(full)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", full, err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("hash %s: %w", full, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Change is one difference between the tree and a manifest.
type Change struct {
	Kind    string // added, removed, changed
	Path    string
	Against string // "manifest" or "main"
}

func (c Change) String() string { return fmt.Sprintf("%-7s %s (vs %s)", c.Kind, c.Path, c.Against) }

// Diff lists the changes of cur relative to base.
func Diff(base, cur *Manifest, against string) []Change {
	var out []Change
	for p, sum := range cur.Hashes {
		switch old, ok := base.Hashes[p]; {
		case !ok:
			out = append(out, Change{"added", p, against})
		case old != sum:
			out = append(out, Change{"changed", p, against})
		}
	}
	for p := range base.Hashes {
		if _, ok := cur.Hashes[p]; !ok {
			out = append(out, Change{"removed", p, against})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Report is the result of the protected stage.
type Report struct {
	Changes []Change
	Notes   []string // e.g. no manifest at main yet
}

// Load reads the manifest of the working tree.
func Load(root string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(root, ManifestPath))
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	return Parse(f)
}

// Check hashes the tree and diffs it against the working-tree manifest and
// the manifest at the given git ref, so a branch that rewrites both the
// files and the manifest is still reported.
func Check(ctx context.Context, root, ref string) (*Report, error) {
	local, err := Load(root)
	if err != nil {
		return nil, err
	}
	cur, err := Hash(ctx, root, local.Globs)
	if err != nil {
		return nil, err
	}
	r := &Report{Changes: Diff(local, cur, "manifest")}
	base, err := gitManifest(ctx, root, ref)
	switch {
	case errors.Is(err, errNoManifest):
		r.Notes = append(r.Notes, fmt.Sprintf("no manifest at %s yet; comparison against %s skipped", ref, ref))
	case err != nil:
		return nil, err
	default:
		r.Changes = append(r.Changes, Diff(base, cur, ref)...)
	}
	return r, nil
}

var errNoManifest = errors.New("no manifest at ref")

func gitManifest(ctx context.Context, root, ref string) (*Manifest, error) {
	// A missing ref, a missing file at the ref or no repository at all mean
	// there is nothing to compare against yet (before the first merge).
	if exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-e", ref+":"+ManifestPath).Run() != nil {
		return nil, errNoManifest
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "show", ref+":"+ManifestPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w: %s", ref, ManifestPath, err, strings.TrimSpace(stderr.String()))
	}
	return Parse(bytes.NewReader(out))
}

// Write regenerates the manifest. It keeps the glob header of an existing
// manifest and seeds DefaultGlobs otherwise.
func Write(ctx context.Context, root string) error {
	globs := DefaultGlobs
	if old, err := Load(root); err == nil && len(old.Globs) > 0 {
		globs = old.Globs
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	m, err := Hash(ctx, root, globs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ManifestPath), m.Format(), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
