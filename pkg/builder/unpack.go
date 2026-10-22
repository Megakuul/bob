package builder

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Archive limits (as R-NIX-014 for the fetcher).
const (
	maxEntries  = 1_000_000
	maxUnpacked = 8 << 30
)

// unpackKind is kind "unpack": extract a tar (plain, gzip, bzip2) or zip archive into To,
// dropping Strip leading components, with the fetcher's safety rules: names confined to To,
// no writes through symlinks, symlinks only to targets inside the tree, hard links become
// copies, no device or fifo entries, modes normalised to 0644/0755.
func unpackKind(c *Ctx, raw json.RawMessage) error {
	var a bspec.UnpackArgs
	if err := decodeStrict(raw, &a); err != nil {
		return Invalidf("unpack args: %v", err)
	}
	if a.Archive == "" || !filepath.IsAbs(a.Archive) || strings.ContainsRune(a.Archive, 0) {
		return Invalidf("archive %q is not an absolute path", a.Archive)
	}
	if a.Strip < 0 {
		return Invalidf("negative strip %d", a.Strip)
	}
	to, err := c.Writable(a.To)
	if err != nil {
		return err
	}
	c.Phase("unpack")
	if err := c.MkdirAll(to); err != nil {
		return err
	}
	u := &unpacker{c: c, root: to, strip: a.Strip, kinds: map[string]entryKind{}}
	if err := u.walk(a.Archive); err != nil {
		return fmt.Errorf("unpack %s: %w", a.Archive, err)
	}
	return nil
}

type entryKind int

const (
	kDir entryKind = iota + 1
	kFile
	kSymlink
)

type unpacker struct {
	c     *Ctx
	root  string
	strip int
	count int
	total int64
	kinds map[string]entryKind // relative path → what the archive put there
}

// walk detects the format from the first bytes.
func (u *unpacker) walk(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	magic, _ := br.Peek(6)
	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")), bytes.HasPrefix(magic, []byte("PK\x05\x06")):
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		return u.zip(f, fi.Size())
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		return u.tar(gz)
	case bytes.HasPrefix(magic, []byte("BZh")):
		return u.tar(bzip2.NewReader(br))
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}), bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		// xz and zstd archives are unpacked by bob's fetcher in Go before the build; a
		// derivation never needs to (the builder stays dependency-free).
		return Invalidf("xz/zstd archives are not unpacked inside derivations")
	}
	return u.tar(br)
}

func (u *unpacker) tar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = u.dir(h.Name)
		case tar.TypeReg, tar.TypeGNUSparse:
			err = u.file(h.Name, h.Mode&0o111 != 0, tr, h.Size)
		case tar.TypeSymlink:
			err = u.symlink(h.Name, h.Linkname)
		case tar.TypeLink:
			err = u.hardlink(h.Name, h.Linkname)
		case tar.TypeXGlobalHeader:
		default:
			err = fmt.Errorf("%s: unsupported entry type %q", h.Name, h.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}

func (u *unpacker) zip(r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, zf := range zr.File {
		m := zf.Mode()
		switch {
		case m.IsDir():
			err = u.dir(zf.Name)
		case m.IsRegular():
			err = func() error {
				rc, err := zf.Open()
				if err != nil {
					return err
				}
				defer rc.Close()
				return u.file(zf.Name, m&0o111 != 0, rc, int64(zf.UncompressedSize64))
			}()
		case m&fs.ModeSymlink != 0:
			err = func() error {
				rc, err := zf.Open()
				if err != nil {
					return err
				}
				defer rc.Close()
				b, err := io.ReadAll(io.LimitReader(rc, 4097))
				if err != nil {
					return err
				}
				if len(b) > 4096 {
					return fmt.Errorf("%s: symlink target too long", zf.Name)
				}
				return u.symlink(zf.Name, string(b))
			}()
		default:
			err = fmt.Errorf("%s: unsupported entry type", zf.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// clean cleans an archive name and drops the stripped prefix; ok is false above the strip level.
func (u *unpacker) clean(name string) (string, bool, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", false, fmt.Errorf("%q: absolute or empty name", name)
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		switch p {
		case "", ".":
		case "..":
			return "", false, fmt.Errorf("%s: path leaves the archive root", name)
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) <= u.strip {
		return "", false, nil
	}
	return strings.Join(parts[u.strip:], "/"), true, nil
}

// rel counts one entry, cleans its name and creates its parent directories.
func (u *unpacker) rel(name string) (string, bool, error) {
	u.count++
	if u.count > maxEntries {
		return "", false, fmt.Errorf("more than %d entries", maxEntries)
	}
	rel, ok, err := u.clean(name)
	if err != nil || !ok {
		return rel, ok, err
	}
	// Every parent must be a directory of this archive: never write through a symlink.
	for i := range len(rel) {
		if rel[i] != '/' {
			continue
		}
		switch u.kinds[rel[:i]] {
		case 0:
			if err := u.c.MkdirAll(u.abs(rel[:i])); err != nil {
				return "", false, err
			}
			u.kinds[rel[:i]] = kDir
		case kDir:
		default:
			return "", false, fmt.Errorf("%s: path through a symlink or file", name)
		}
	}
	if k := u.kinds[rel]; k != 0 && k != kDir {
		return "", false, fmt.Errorf("%s: duplicate entry", name)
	}
	return rel, true, nil
}

func (u *unpacker) abs(rel string) string { return filepath.Join(u.root, filepath.FromSlash(rel)) }

func (u *unpacker) dir(name string) error {
	rel, ok, err := u.rel(name)
	if err != nil || !ok {
		return err
	}
	if u.kinds[rel] == kDir {
		return nil
	}
	u.kinds[rel] = kDir
	return u.c.MkdirAll(u.abs(rel))
}

func (u *unpacker) file(name string, exec bool, r io.Reader, size int64) error {
	rel, ok, err := u.rel(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: file above the stripped top-level directory", name)
	}
	if u.kinds[rel] == kDir || size < 0 {
		return fmt.Errorf("%s: invalid entry", name)
	}
	u.total += size
	if u.total > maxUnpacked {
		return fmt.Errorf("%s: unpacked size above 8 GiB", name)
	}
	u.kinds[rel] = kFile
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	return writeFile(u.abs(rel), mode, r, size)
}

func (u *unpacker) symlink(name, target string) error {
	rel, ok, err := u.rel(name)
	if err != nil || !ok {
		return err
	}
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, 0) {
		return fmt.Errorf("%s: symlink to an absolute path", name)
	}
	if t := path.Join(path.Dir(rel), target); t == ".." || strings.HasPrefix(t, "../") {
		return fmt.Errorf("%s: symlink pointing outside the archive", name)
	}
	if u.kinds[rel] == kDir {
		return fmt.Errorf("%s: duplicate entry", name)
	}
	u.kinds[rel] = kSymlink
	return os.Symlink(target, u.abs(rel))
}

// hardlink copies an earlier regular file of the archive.
func (u *unpacker) hardlink(name, link string) error {
	target, ok, err := u.clean(link)
	if err != nil || !ok || u.kinds[target] != kFile {
		return errors.Join(fmt.Errorf("%s: hard link to a missing or non-regular entry", name), err)
	}
	src := u.abs(target)
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return u.file(name, fi.Mode()&0o111 != 0, f, fi.Size())
}
