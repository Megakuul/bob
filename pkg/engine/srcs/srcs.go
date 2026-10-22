package srcs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Tree is a filtered source tree: Files are slash-separated paths relative to Root.
type Tree struct {
	Root  string
	Files []string
}

// Source is one package source set to import.
type Source struct {
	Name string
	Tree Tree
}

// Path computes makeStorePath("source", sha256(NAR), name) without touching the store.
func Path(name string, t Tree) (string, error) {
	files := t.Files
	if files == nil {
		files = []string{}
	}
	sum, _, err := nar.Hash(t.Root, files)
	if err != nil {
		return "", fmt.Errorf("srcs: %s: %w", name, err)
	}
	return drv.MakeStorePath("source", sum, name)
}

// Import serialises each source as a NAR in Go, checks validity in one batch and imports only
// the missing paths through one `nix-store --import` stream. It returns the store paths in the
// order of sources.
func Import(ctx context.Context, st *store.Store, sources []Source) ([]string, error) {
	// One pass serialises and hashes each tree; the NARs of small trees are kept for the import
	// (up to keepBudget bytes in all), larger ones are serialised again only if missing.
	paths := make([]string, len(sources))
	nars := make([][]byte, len(sources))
	var kept atomic.Int64
	if err := parallel(ctx, len(sources), func(i int) error {
		b, err := serialise(sources[i])
		if err != nil {
			return err
		}
		p, err := drv.MakeStorePath("source", sha256.Sum256(b), sources[i].Name)
		if err != nil {
			return fmt.Errorf("srcs: %s: %w", sources[i].Name, err)
		}
		paths[i] = p
		if kept.Add(int64(len(b))) <= keepBudget {
			nars[i] = b
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// One check for all distinct paths; equal sources share a path.
	first := map[string]int{}
	var uniq []string
	for i, p := range paths {
		if _, ok := first[p]; !ok {
			first[p] = i
			uniq = append(uniq, p)
		}
	}
	invalid, err := st.Invalid(ctx, uniq)
	if err != nil {
		return nil, err
	}
	if len(invalid) == 0 {
		return paths, nil
	}
	items := make([]store.ExportItem, len(invalid))
	if err := parallel(ctx, len(invalid), func(k int) error {
		i, ok := first[invalid[k]]
		if !ok {
			return fmt.Errorf("srcs: the store reported an unknown path %s", invalid[k])
		}
		b := nars[i]
		if b == nil {
			var err error
			if b, err = serialise(sources[i]); err != nil {
				return err
			}
			// The tree may have changed since it was hashed.
			if p, err := drv.MakeStorePath("source", sha256.Sum256(b), sources[i].Name); err != nil || p != invalid[k] {
				return fmt.Errorf("srcs: %s changed while it was imported", sources[i].Name)
			}
		}
		items[k] = store.ExportItem{Path: invalid[k], NAR: b}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := st.Import(ctx, items); err != nil {
		return nil, err
	}
	return paths, nil
}

// keepBudget bounds the NAR bytes Import holds between hashing and importing.
const keepBudget = 256 << 20

// serialise returns the NAR of a source tree.
func serialise(s Source) ([]byte, error) {
	files := s.Tree.Files
	if files == nil {
		files = []string{}
	}
	var b narBuf
	if err := nar.Write(&b, s.Tree.Root, files); err != nil {
		return nil, fmt.Errorf("srcs: %s: %w", s.Name, err)
	}
	return b.b, nil
}

type narBuf struct{ b []byte }

func (n *narBuf) Write(p []byte) (int, error) { n.b = append(n.b, p...); return len(p), nil }

// parallel runs f(0..n-1) on GOMAXPROCS workers and returns the first error. Panics in f become
// errors (AGENTS.md §5.3).
func parallel(ctx context.Context, n int, f func(i int) error) error {
	workers := min(runtime.GOMAXPROCS(0), n)
	next := make(chan int)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					errs <- fmt.Errorf("srcs: panic: %v", p)
				}
			}()
			for i := range next {
				if err := f(i); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	var err error
feed:
	for i := 0; i < n; i++ {
		select {
		case next <- i:
		case err = <-errs:
			break feed
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		}
	}
	close(next)
	go func() {
		defer func() { _ = recover() }()
		wg.Wait()
		close(errs)
	}()
	for e := range errs {
		if err == nil {
			err = e
		}
	}
	return err
}

// Resolve resolves one Sources value (R-FW-021) of the package defined at pos ("dir/bob.go:12")
// to the sorted, repo-relative regular files and symlinks below repo/s.Dir that match
// s.Patterns. Patterns are relative to s.Dir: "*" and "?" stay within one path segment, "**"
// matches any depth, "[…]" as in path.Match, a leading "!" excludes, and a pattern naming a
// directory selects everything below it. Always excluded: .git/ and .bob/ directories, every
// bob.go, zz_bob_cgo.go and .gitignore (VCS metadata: editing an ignore list must not change a
// source path, R-FW-021 (a)), files ignored by a .gitignore on the path or by .git/info/exclude,
// and the repo-relative paths for which skip returns true (the materialisation manifest, §03).
func Resolve(repo string, s ir.Srcs, pos string, skip func(rel string) bool) ([]string, error) {
	dir := path.Clean("/" + filepath.ToSlash(s.Dir))[1:]
	if want := path.Clean(filepath.ToSlash(s.Dir)); s.Dir != "" && want != "." && (path.IsAbs(s.Dir) || dir != want) {
		return nil, diag.New(1025, "variant", "outside", "pos", pos, "pattern", s.Dir)
	}
	type pat struct {
		raw  string
		segs []string
		hits int
	}
	var incl, excl []*pat
	for _, p := range s.Patterns {
		neg := strings.HasPrefix(p, "!")
		body := strings.TrimPrefix(p, "!")
		clean := path.Clean(path.Join(dir, body))
		if body == "" || path.IsAbs(body) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, diag.New(1025, "variant", "outside", "pos", pos, "pattern", p)
		}
		if _, err := path.Match(body, ""); err != nil {
			return nil, diag.New(1025, "variant", "noglob", "pos", pos, "pattern", p)
		}
		if clean == "." {
			clean = "**"
		}
		x := &pat{raw: p, segs: strings.Split(clean, "/")}
		if neg {
			excl = append(excl, x)
		} else {
			incl = append(incl, x)
		}
	}
	rules, err := readIgnore(filepath.Join(repo, ".git", "info", "exclude"), "")
	if err != nil {
		return nil, fmt.Errorf("srcs: %w", err)
	}
	// .gitignore files of the root and of every directory down to s.Dir.
	prefix := ""
	for _, part := range append([]string{""}, splitNonEmpty(dir)...) {
		prefix = path.Join(prefix, part)
		r, err := readIgnore(filepath.Join(repo, filepath.FromSlash(prefix), ".gitignore"), prefix)
		if err != nil {
			return nil, fmt.Errorf("srcs: %w", err)
		}
		if part != "" && ignored(rules, prefix, true) {
			return nil, nil // the whole directory is ignored
		}
		rules = append(rules, r...)
	}
	// matches reports whether rel (repo-relative) is selected by p, or lies below a directory
	// that p names.
	matches := func(p *pat, rel string) bool {
		segs := strings.Split(rel, "/")
		for k := len(segs); k > 0; k-- {
			if matchSegs(p.segs, segs[:k]) {
				return true
			}
		}
		return false
	}
	var out []string
	var walk func(rel string, rules []rule) error
	walk = func(rel string, rules []rule) error {
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		if rel != dir {
			r, err := readIgnore(filepath.Join(abs, ".gitignore"), rel)
			if err != nil {
				return err
			}
			rules = append(rules[:len(rules):len(rules)], r...)
		}
		ents, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, e := range ents {
			name := e.Name()
			child := path.Join(rel, name)
			if rel == "" {
				child = name
			}
			isDir := e.IsDir()
			if isDir && (name == ".git" || name == ".bob") {
				continue
			}
			if !isDir && (name == "bob.go" || name == "zz_bob_cgo.go" || name == ".gitignore") {
				continue
			}
			if ignored(rules, child, isDir) || skip != nil && skip(child) {
				continue
			}
			if isDir {
				if err := walk(child, rules); err != nil {
					return err
				}
				continue
			}
			t := e.Type()
			if t&fs.ModeSymlink == 0 && !t.IsRegular() {
				continue // sockets, fifos, devices
			}
			sel := false
			for _, p := range incl {
				if matches(p, child) {
					p.hits++
					sel = true
				}
			}
			for _, p := range excl {
				if sel && matches(p, child) {
					sel = false
				}
			}
			if !sel {
				continue
			}
			if t&fs.ModeSymlink != 0 {
				target, err := os.Readlink(filepath.Join(abs, name))
				if err != nil {
					return err
				}
				resolved := path.Clean(path.Join(rel, filepath.ToSlash(target)))
				if path.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
					return diag.New(1025, "variant", "symlink", "file", child)
				}
			}
			out = append(out, child)
		}
		return nil
	}
	if fi, err := os.Stat(filepath.Join(repo, filepath.FromSlash(dir))); err != nil || !fi.IsDir() {
		if len(incl) > 0 {
			return nil, diag.New(1025, "variant", "noglob", "pos", pos, "pattern", incl[0].raw)
		}
		return nil, nil
	}
	if err := walk(dir, rules); err != nil {
		var de *diag.Error
		if errors.As(err, &de) {
			return nil, de
		}
		return nil, fmt.Errorf("srcs: %w", err)
	}
	for _, p := range incl {
		if p.hits == 0 {
			return nil, diag.New(1025, "variant", "noglob", "pos", pos, "pattern", p.raw)
		}
	}
	sort.Strings(out)
	return out, nil
}

func splitNonEmpty(p string) []string {
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

// ResolveAll is the union of Resolve over all Sources values of one package: the files of its
// single source path (R-FW-021), sorted and without duplicates.
func ResolveAll(repo string, all []ir.Srcs, pos string, skip func(rel string) bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, s := range all {
		files, err := Resolve(repo, s, pos, skip)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
