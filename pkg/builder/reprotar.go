package builder

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reproducible tar archives of SPEC R-OUT-010, shared by the output builders (OCI layers,
// tarballs): entries in lexical order (directories end with "/", so parents come first),
// mtime 1 s after the epoch, uid/gid 0 without names, permissions from the store, symlinks as
// symlinks, hard links as regular files, no xattrs or PAX records (names that do not fit the
// USTAR header use the GNU long-name form). Gzip: compress/gzip DefaultCompression, no name, no
// mtime.

// TarEntry is one archive entry.
type TarEntry struct {
	Name     string // relative, slash-separated; directories end with "/"
	Mode     int64  // permission bits, including the sticky bit
	Type     byte   // tar.TypeReg, tar.TypeDir or tar.TypeSymlink
	Linkname string // symlink target
	Data     []byte // content of a regular file given in memory
	File     string // content of a regular file read from disk (when Data is nil)
	Size     int64  // size of File
}

// reproTime is the mtime of every entry (R-OUT-010).
var reproTime = time.Unix(1, 0)

// TarDir returns a directory entry.
func TarDir(name string, mode int64) TarEntry {
	return TarEntry{Name: strings.TrimSuffix(name, "/") + "/", Mode: mode, Type: tar.TypeDir}
}

// TarFile returns a regular file entry with in-memory content.
func TarFile(name string, mode int64, data []byte) TarEntry {
	return TarEntry{Name: name, Mode: mode, Type: tar.TypeReg, Data: data, Size: int64(len(data))}
}

// TarSymlink returns a symlink entry.
func TarSymlink(name, target string) TarEntry {
	return TarEntry{Name: name, Mode: 0o777, Type: tar.TypeSymlink, Linkname: target}
}

// TreeEntries lists the tree at dir (a store path, a file or a symlink) as entries named
// prefix (the root) and prefix/<rel> below it. skip(rel) leaves an entry out ("" is the root;
// a skipped directory leaves out its subtree).
func TreeEntries(dir, prefix string, skip func(rel string) bool) ([]TarEntry, error) {
	var out []TarEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := prefix
		if rel != "." {
			name = prefix + "/" + rel
		} else {
			rel = ""
		}
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := int64(info.Mode().Perm())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, TarSymlink(name, target))
		case info.IsDir():
			out = append(out, TarDir(name, mode))
		case info.Mode().IsRegular():
			out = append(out, TarEntry{Name: name, Mode: mode, Type: tar.TypeReg, File: p, Size: info.Size()})
		default:
			return fmt.Errorf("%s: unsupported file type %v", p, info.Mode().Type())
		}
		return nil
	})
	return out, err
}

// WriteTar writes entries in lexical order to w. Duplicate names are an error.
func WriteTar(w io.Writer, entries []TarEntry) error {
	es := append([]TarEntry(nil), entries...)
	sort.SliceStable(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	tw := tar.NewWriter(w)
	for i, e := range es {
		if i > 0 && es[i-1].Name == e.Name {
			return fmt.Errorf("tar: entry %s twice", e.Name)
		}
		h := &tar.Header{Name: e.Name, Mode: e.Mode, Typeflag: e.Type, Linkname: e.Linkname, ModTime: reproTime}
		if e.Type == tar.TypeReg {
			h.Size = e.Size
		}
		if len(h.Name) > 100 || len(h.Linkname) > 100 {
			h.Format = tar.FormatGNU // never PAX records (R-OUT-010)
		}
		if err := tw.WriteHeader(h); err != nil {
			return fmt.Errorf("tar: %s: %w", e.Name, err)
		}
		if e.Type != tar.TypeReg {
			continue
		}
		if e.Data != nil || e.File == "" {
			if _, err := tw.Write(e.Data); err != nil {
				return fmt.Errorf("tar: %s: %w", e.Name, err)
			}
			continue
		}
		if err := copySized(tw, e.File, e.Size); err != nil {
			return fmt.Errorf("tar: %s: %w", e.Name, err)
		}
	}
	return tw.Close()
}

func copySized(w io.Writer, file string, size int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(w, io.LimitReader(f, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("%s changed size while archiving", file)
	}
	return nil
}

// TarGz is what WriteTarGz wrote: the digests of the uncompressed (DiffID) and compressed
// (Digest) stream as "sha256:<hex>", and the compressed size.
type TarGz struct {
	DiffID string
	Digest string
	Size   int64
}

// WriteTarGz writes entries as a gzip-compressed tar to file.
func WriteTarGz(file string, entries []TarEntry) (TarGz, error) {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return TarGz{}, err
	}
	defer f.Close()
	res, err := TarGzTo(f, entries)
	if err != nil {
		return TarGz{}, err
	}
	return res, f.Close()
}

// TarGzTo writes entries as a gzip-compressed tar to w.
func TarGzTo(w io.Writer, entries []TarEntry) (TarGz, error) {
	gzHash := sha256.New()
	cw := &countWriter{w: io.MultiWriter(w, gzHash)}
	zw, err := gzip.NewWriterLevel(cw, gzip.DefaultCompression)
	if err != nil {
		return TarGz{}, err
	}
	tarHash := sha256.New()
	if err := WriteTar(io.MultiWriter(zw, tarHash), entries); err != nil {
		return TarGz{}, err
	}
	if err := zw.Close(); err != nil {
		return TarGz{}, err
	}
	return TarGz{DiffID: "sha256:" + hex.EncodeToString(tarHash.Sum(nil)),
		Digest: "sha256:" + hex.EncodeToString(gzHash.Sum(nil)), Size: cw.n}, nil
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
