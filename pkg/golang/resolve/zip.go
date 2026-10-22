package resolve

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxZipEntries caps the entries of one module zip.
const maxZipEntries = 500_000

// Hash1 is the go.sum "h1:" hash (golang.org/x/mod/sumdb/dirhash.Hash1): sha256 over the
// sorted lines "<hex sha256 of the file>  <name>\n".
func Hash1(files []string, open func(name string) (io.ReadCloser, error)) (string, error) {
	files = append([]string(nil), files...)
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		if strings.Contains(f, "\n") {
			return "", fmt.Errorf("file name %q contains a newline", f)
		}
		r, err := open(f)
		if err != nil {
			return "", err
		}
		hf := sha256.New()
		_, err = io.Copy(hf, r)
		r.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%x  %s\n", hf.Sum(nil), f)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// ModH1 is the go.sum hash of a go.mod file ("<path> <version>/go.mod <h1>").
func ModH1(gomod []byte) string {
	s, _ := Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(gomod))), nil
	})
	return s
}

// ModuleZip is a module zip from the proxy, checked against the module zip rules: every
// entry below "<path>@<version>/", regular files and directories only, no duplicates (also
// not by case), no ".." or empty path elements, bounded size.
type ModuleZip struct {
	zr     *zip.ReadCloser
	prefix string
	files  map[string]*zip.File   // module-relative name → entry (files only)
	dirs   map[string][]*zip.File // module-relative directory → its files
}

// OpenModuleZip opens and checks the zip file of module m.
func OpenModuleZip(file string, m Module) (*ModuleZip, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("%s: the module zip cannot be read: %w", m, err)
	}
	z := &ModuleZip{zr: zr, prefix: m.Path + "@" + m.Version + "/", files: map[string]*zip.File{}, dirs: map[string][]*zip.File{}}
	if err := z.check(m); err != nil {
		zr.Close()
		return nil, err
	}
	return z, nil
}

func (z *ModuleZip) check(m Module) error {
	if len(z.zr.File) > maxZipEntries {
		return fmt.Errorf("%s: the module zip has more than %d entries", m, maxZipEntries)
	}
	var total uint64
	seen := map[string]bool{}
	for _, f := range z.zr.File {
		rel, ok := strings.CutPrefix(f.Name, z.prefix)
		if !ok {
			return fmt.Errorf("%s: module zip entry %q is outside %s", m, f.Name, z.prefix)
		}
		mode := f.Mode()
		isDir := mode.IsDir() || strings.HasSuffix(rel, "/")
		clean := strings.TrimSuffix(rel, "/")
		if isDir && clean == "" {
			continue
		}
		if clean == "" || strings.Contains(clean, "\\") || strings.ContainsRune(clean, 0) {
			return fmt.Errorf("%s: module zip entry %q has an invalid name", m, f.Name)
		}
		for _, e := range strings.Split(clean, "/") {
			if e == "" || e == "." || e == ".." {
				return fmt.Errorf("%s: module zip entry %q has an invalid name", m, f.Name)
			}
		}
		key := strings.ToLower(clean)
		if seen[key] {
			return fmt.Errorf("%s: module zip has the entry %q twice (names differing only in case count as one)", m, clean)
		}
		seen[key] = true
		if isDir {
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%s: module zip entry %q is not a regular file", m, f.Name)
		}
		total += f.UncompressedSize64
		if total > MaxZipBytes {
			return fmt.Errorf("%s: the module zip unpacks to more than %d bytes", m, MaxZipBytes)
		}
		z.files[clean] = f
		d := path.Dir(clean)
		if d == "." {
			d = ""
		}
		z.dirs[d] = append(z.dirs[d], f)
	}
	return nil
}

// Close releases the zip.
func (z *ModuleZip) Close() error { return z.zr.Close() }

// H1 is the go.sum hash of the zip (dirhash.HashZip: every entry by its full name).
func (z *ModuleZip) H1() (string, error) {
	byName := map[string]*zip.File{}
	var names []string
	for _, f := range z.zr.File {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	return Hash1(names, func(n string) (io.ReadCloser, error) { return byName[n].Open() })
}

// HasGoFiles reports whether the module directory dir ("" = root) holds .go files (cmd/go's
// test for "the module provides the package").
func (z *ModuleZip) HasGoFiles(dir string) bool {
	for _, f := range z.dirs[dir] {
		if strings.HasSuffix(f.Name, ".go") {
			return true
		}
	}
	return false
}

// GoFiles returns the .go files of directory dir: base name → reader.
func (z *ModuleZip) GoFiles(dir string) map[string]func() (io.ReadCloser, error) {
	out := map[string]func() (io.ReadCloser, error){}
	for _, f := range z.dirs[dir] {
		if strings.HasSuffix(f.Name, ".go") {
			out[path.Base(f.Name)] = f.Open
		}
	}
	return out
}

// Dirs lists the module directories that hold files.
func (z *ModuleZip) Dirs() []string {
	out := make([]string, 0, len(z.dirs))
	for d := range z.dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Extract writes the module tree below dst as bob's fetcher unpacks the same zip: the prefix
// stripped, directories of directory entries created, files 0644 or 0755 (any execute bit of
// the entry's mode).
func (z *ModuleZip) Extract(dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, f := range z.zr.File {
		rel := strings.TrimSuffix(strings.TrimPrefix(f.Name, z.prefix), "/")
		if rel == "" {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if f.Mode().IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if f.Mode()&0o111 != 0 {
			perm = 0o755
		}
		if err := extractFile(f, p, perm); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, p string, perm os.FileMode) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(r, int64(f.UncompressedSize64)+1)); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(p, perm) // umask-independent
}
