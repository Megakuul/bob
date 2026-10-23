package srcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

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
	// (up to keepBudget bytes in all), larger ones are streamed into the store again if missing,
	// so memory stays bounded whatever the tree size (F5).
	paths := make([]string, len(sources))
	sums := make([][32]byte, len(sources))
	nars := make([][]byte, len(sources))
	var kept atomic.Int64
	if err := parallel(ctx, len(sources), func(i int) error {
		sum, b, err := serialise(sources[i], &kept)
		if err != nil {
			return err
		}
		p, err := drv.MakeStorePath("source", sum, sources[i].Name)
		if err != nil {
			return fmt.Errorf("srcs: %s: %w", sources[i].Name, err)
		}
		paths[i], sums[i], nars[i] = p, sum, b
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
	for k, p := range invalid {
		i, ok := first[p]
		if !ok {
			return nil, fmt.Errorf("srcs: the store reported an unknown path %s", p)
		}
		if nars[i] != nil {
			items[k] = store.ExportItem{Path: p, NAR: nars[i]}
			continue
		}
		// The tree may have changed since it was hashed: TreeItem checks the hash again.
		items[k] = store.TreeItem(p, sources[i].Tree.Root, filesOf(sources[i]), &sums[i])
	}
	if err := st.Import(ctx, items); err != nil {
		return nil, err
	}
	return paths, nil
}

// keepBudget bounds the NAR bytes Import holds between hashing and importing; keepMax bounds
// one kept NAR.
const (
	keepBudget = 256 << 20
	keepMax    = 4 << 20
)

func filesOf(s Source) []string {
	if s.Tree.Files == nil {
		return []string{}
	}
	return s.Tree.Files
}

// serialise hashes the NAR of a source tree and returns it too when it is small enough to keep
// (kept counts the bytes kept by all calls).
func serialise(s Source, kept *atomic.Int64) ([32]byte, []byte, error) {
	kw := &keepWriter{max: keepMax}
	if err := nar.Write(kw, s.Tree.Root, filesOf(s)); err != nil {
		return [32]byte{}, nil, fmt.Errorf("srcs: %s: %w", s.Name, err)
	}
	if kw.h != nil {
		var sum [32]byte
		copy(sum[:], kw.h.Sum(nil))
		return sum, nil, nil
	}
	b := kw.buf.Bytes()
	sum := sha256.Sum256(b)
	if kept.Add(int64(len(b))) > keepBudget {
		b = nil
	}
	return sum, b, nil
}

// keepWriter keeps what is written up to max bytes; beyond that it drops the bytes and only
// hashes the stream (h != nil), so a large tree never sits in memory.
type keepWriter struct {
	buf bytes.Buffer
	max int
	h   hash.Hash
}

func (k *keepWriter) Write(p []byte) (int, error) {
	if k.h == nil && k.buf.Len()+len(p) <= k.max {
		return k.buf.Write(p)
	}
	if k.h == nil {
		k.h = sha256.New()
		k.h.Write(k.buf.Bytes())
		k.buf = bytes.Buffer{}
	}
	return k.h.Write(p)
}

// parallel runs f(0..n-1) on GOMAXPROCS workers and returns the first error. Panics in f become
// errors (AGENTS.md §5.3).
func parallel(ctx context.Context, n int, f func(i int) error) error {
	workers := min(runtime.GOMAXPROCS(0), n)
	next := make(chan int)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
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
	for i := range n {
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
			rules = append(slices.Clip(rules), r...)
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
			if name == ".git" || isDir && name == ".bob" { // .git is a file in a linked worktree
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
				inside, err := linkInside(repo, child)
				if err != nil {
					return err
				}
				if !inside {
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

// maxLinkHops bounds the symlinks followed while resolving one link (as Linux's 40).
const maxLinkHops = 40

// linkInside reports whether the symlink at rel (repo-relative, slash form) resolves inside
// repo (R-FW-021). The target is walked component by component from the link's directory and
// every component that is itself a symlink is read and substituted, so "x -> d/up/../y" with
// "d/up -> .." is caught (a lexical check of the target alone is not enough). The walk fails as
// soon as it leaves the repository, on an absolute target and after maxLinkHops links. A
// component that does not exist is taken lexically: nothing can be reached through it.
func linkInside(repo, rel string) (bool, error) {
	var cur []string
	if d := path.Dir(rel); d != "." {
		cur = strings.Split(d, "/")
	}
	target, err := os.Readlink(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		return false, err
	}
	todo := []string{target}
	for hops := 0; ; hops++ {
		t := filepath.ToSlash(todo[0])
		if t == "" || path.IsAbs(t) || hops > maxLinkHops {
			return false, nil
		}
		todo = append(strings.Split(t, "/"), todo[1:]...)
		next := []string(nil)
		for next == nil && len(todo) > 0 {
			c := todo[0]
			todo = todo[1:]
			switch c {
			case "", ".":
				continue
			case "..":
				if len(cur) == 0 {
					return false, nil
				}
				cur = cur[:len(cur)-1]
				continue
			}
			cand := append(cur[:len(cur):len(cur)], c)
			fi, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(strings.Join(cand, "/"))))
			if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
				return false, err
			}
			if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
				cur = cand
				continue
			}
			next = cand
		}
		if next == nil {
			return true, nil
		}
		link, err := os.Readlink(filepath.Join(repo, filepath.FromSlash(strings.Join(next, "/"))))
		if err != nil {
			return false, err
		}
		todo = append([]string{link}, todo...)
	}
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
