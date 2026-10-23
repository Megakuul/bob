package builder

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Every path a job writes is under an output or the build directory (R-NIX-016). The check is
// lexical first (the cleaned path must lie in a writable root), then structural: no existing
// component from the root down to the parent may be a symlink, and the last component is
// opened without following symlinks. Steps run one after another, so nothing changes the tree
// between the check and the write except the job's own exec steps, which Nix's sandbox
// confines.

// within reports whether p lies in root (or is root).
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator)) || root == "/"
}

// root returns the writable root containing the clean absolute path p.
func (c *Ctx) root(p string) (string, bool) {
	best := ""
	for _, r := range c.roots() {
		if within(p, r) && len(r) > len(best) {
			best = r
		}
	}
	return best, best != ""
}

func (c *Ctx) roots() []string {
	rs := []string{c.BuildTop}
	for _, o := range c.Job.Outputs {
		rs = append(rs, c.Outputs[o])
	}
	return rs
}

// Writable checks that the job may write p and returns it cleaned.
func (c *Ctx) Writable(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return "", Invalidf("path %q is not an absolute path", p)
	}
	clean := filepath.Clean(p)
	root, ok := c.root(clean)
	if !ok {
		return "", Invalidf("path %s is outside the outputs and the build directory", p)
	}
	// From the root down to the parent: existing components must not be symlinks.
	cur := root
	rest := strings.TrimPrefix(strings.TrimPrefix(clean, root), string(filepath.Separator))
	parts := strings.Split(rest, string(filepath.Separator))
	if rest == "" {
		parts = nil
	}
	for i := 0; ; i++ {
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 && i < len(parts) {
			return "", Invalidf("path %s goes through the symlink %s", p, cur)
		}
		if i >= len(parts)-1 {
			break
		}
		cur = filepath.Join(cur, parts[i])
	}
	return clean, nil
}

// MkdirAll creates the directory p (checked with Writable) and its missing parents below the
// writable root, never following symlinks.
func (c *Ctx) MkdirAll(p string) error {
	clean, err := c.Writable(p)
	if err != nil {
		return err
	}
	root, _ := c.root(clean)
	rest := strings.TrimPrefix(strings.TrimPrefix(clean, root), string(filepath.Separator))
	cur := root
	parts := []string{""}
	if rest != "" {
		parts = append(parts, strings.Split(rest, string(filepath.Separator))...)
	}
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			continue
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			return Invalidf("path %s goes through the symlink %s", p, cur)
		case err == nil:
			return fmt.Errorf("mkdir %s: %s exists and is not a directory", p, cur)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			// Kinds create directories from several goroutines (go-compile writes the
			// packages of a module in parallel): another one may have made it in between.
			if fi, lerr := os.Lstat(cur); errors.Is(err, fs.ErrExist) && lerr == nil && fi.IsDir() {
				continue
			}
			return err
		}
	}
	return nil
}

// parents creates the parent directory of the writable path p (none when p is a root itself:
// a single-file output).
func (c *Ctx) parents(p string) error {
	root, _ := c.root(p)
	if p == root {
		return nil
	}
	return c.MkdirAll(filepath.Dir(p))
}

// writeFile creates or truncates the writable file p (never through a symlink) and fills it
// from r, at most size bytes when size >= 0 (and then exactly size bytes).
func writeFile(p string, mode os.FileMode, r io.Reader, size int64) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if size >= 0 {
		r = io.LimitReader(r, size)
	}
	n, err := io.Copy(f, r)
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("%s: copied %d of %d bytes", p, n, size)
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Close()
}

// fileMode normalises permission bits the way Nix stores them: executable or not.
func fileMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// Copy copies the file or tree from (followed if it is a symlink itself) to the writable path
// to; inside a tree symlinks stay symlinks. Ecosystem kinds use it like the copy op of "run".
func (c *Ctx) Copy(from, to string) error { return c.copyAny(from, to) }

// copyAny copies the file or tree from (followed if it is a symlink itself) to the writable
// path to. Inside a tree symlinks are copied as symlinks and modes are normalised.
func (c *Ctx) copyAny(from, to string) error {
	if from == "" || strings.ContainsRune(from, 0) || !filepath.IsAbs(from) {
		return Invalidf("copy source %q is not an absolute path", from)
	}
	to, err := c.Writable(to)
	if err != nil {
		return err
	}
	if err := c.parents(to); err != nil {
		return err
	}
	realFrom, err := filepath.EvalSymlinks(from)
	if err != nil {
		return err
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(to))
	if err != nil {
		return err
	}
	if realTo := filepath.Join(realParent, filepath.Base(to)); within(realTo, realFrom) {
		return Invalidf("copy of %s into itself (%s)", from, to)
	}
	fi, err := os.Stat(realFrom)
	if err != nil {
		return err
	}
	switch {
	case fi.IsDir():
		return c.copyTree(realFrom, to)
	case fi.Mode().IsRegular():
		return copyRegular(realFrom, to, fi)
	}
	return fmt.Errorf("copy %s: not a regular file or directory", from)
}

func copyRegular(from, to string, fi fs.FileInfo) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	// Store inputs never change; copying the size seen keeps pseudo files (size 0) bounded.
	return writeFile(to, fileMode(fi.Mode()), in, fi.Size())
}

// copyTree copies the directory from into the writable directory to (created or merged).
func (c *Ctx) copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		switch t := d.Type(); {
		case t.IsDir():
			return c.MkdirAll(dst)
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case t.IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return copyRegular(p, dst, fi)
		}
		return fmt.Errorf("copy %s: special file %s", from, p)
	})
}
