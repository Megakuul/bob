package fetch

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Archive limits of SPEC R-NIX-014.
const (
	maxEntries      = 1_000_000
	maxUnpacked     = 8 << 30
	maxSymlinkBytes = 4096
)

// entry kinds after classification.
const (
	kindDir = iota + 1
	kindFile
	kindSymlink
	kindHardlink
	kindSkip
)

// entry is one archive member in a format-independent form.
type entry struct {
	name string // as recorded in the archive
	kind int
	link string // symlink target or hard link name
	exec bool
	size int64
	bad  string // reason the entry type is refused
	open func() (io.Reader, error)
}

// writeErr is a local I/O failure while extracting (not the archive's fault).
type writeErr struct{ err error }

func (e *writeErr) Error() string { return e.err.Error() }

func (e *writeErr) Unwrap() error { return e.err }

func wrote(err error) error {
	if err == nil {
		return nil
	}
	var ue *unpackErr
	if errors.As(err, &ue) {
		return err
	}
	return &writeErr{err: err}
}

// unpackErr marks a refusal (BOB-E-3004) of entry path for reason.
type unpackErr struct{ path, reason string }

func (e *unpackErr) Error() string { return e.path + ": " + e.reason }

// unpack extracts the archive in file into root with the safety rules of R-NIX-014. It reads
// the archive twice: a first pass validates every entry without writing anything, so an unsafe
// archive leaves no files behind, and the second pass extracts.
func unpack(ctx context.Context, file, root string, strip int, label, url string) error {
	for _, extract := range []bool{false, true} {
		u := &unpacker{ctx: ctx, root: root, strip: strip, extract: extract, kinds: map[string]int{},
			files: map[string]fileMeta{}, links: map[string]string{}}
		if extract {
			if err := os.MkdirAll(root, 0o755); err != nil {
				return fmt.Errorf("fetch %s: %w", label, err)
			}
		}
		err := walkArchive(file, u.add)
		if err == nil && !extract {
			err = u.checkLinks()
		}
		if err != nil {
			var ue *unpackErr
			var we *writeErr
			if errors.As(err, &ue) {
				return unsafeEntry(label, url, ue.path, ue.reason)
			}
			if errors.As(err, &we) {
				return fmt.Errorf("fetch %s: unpack: %w", label, we.err)
			}
			if isDiag(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return unsafeEntry(label, url, "-", "the archive cannot be read: "+err.Error())
		}
	}
	return nil
}

// walkArchive detects the format of file from its first bytes and calls fn for every entry.
func walkArchive(file string, fn func(entry) error) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	magic, _ := br.Peek(6)
	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")), bytes.HasPrefix(magic, []byte("PK\x05\x06")):
		st, err := f.Stat()
		if err != nil {
			return err
		}
		return walkZip(f, st.Size(), fn)
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		return walkTar(gz, fn)
	case bytes.HasPrefix(magic, []byte("BZh")):
		return walkTar(bzip2.NewReader(br), fn)
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		xr, err := xz.NewReader(br)
		if err != nil {
			return err
		}
		return walkTar(xr, fn)
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return err
		}
		defer zr.Close()
		return walkTar(zr, fn)
	}
	return walkTar(br, fn)
}

func walkTar(r io.Reader, fn func(entry) error) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		e := entry{name: h.Name, link: h.Linkname, exec: h.Mode&0o111 != 0, size: h.Size,
			open: func() (io.Reader, error) { return tr, nil }}
		switch h.Typeflag {
		case tar.TypeDir:
			e.kind = kindDir
		case tar.TypeReg, tar.TypeGNUSparse:
			e.kind = kindFile
		case tar.TypeSymlink:
			e.kind = kindSymlink
		case tar.TypeLink:
			e.kind = kindHardlink
		case tar.TypeXGlobalHeader:
			e.kind = kindSkip
		case tar.TypeChar, tar.TypeBlock:
			e.bad = "device node"
		case tar.TypeFifo:
			e.bad = "fifo"
		default:
			e.bad = fmt.Sprintf("unsupported entry type %q", h.Typeflag)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}

func walkZip(r io.ReaderAt, size int64, fn func(entry) error) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	if len(zr.File) > maxEntries {
		return &unpackErr{path: zr.File[maxEntries].Name, reason: fmt.Sprintf("more than %d entries", maxEntries)}
	}
	for _, zf := range zr.File {
		m := zf.Mode()
		e := entry{name: zf.Name, exec: m&0o111 != 0, size: int64(zf.UncompressedSize64),
			open: func() (io.Reader, error) { return zf.Open() }}
		switch {
		case m.IsDir():
			e.kind = kindDir
		case m.IsRegular():
			e.kind = kindFile
		case m&fs.ModeSymlink != 0:
			e.kind = kindSymlink
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(rc, maxSymlinkBytes+1))
			_ = rc.Close()
			if err != nil {
				return err
			}
			if len(b) > maxSymlinkBytes {
				e.bad = "symlink target too long"
			}
			e.link = string(b)
		case m&fs.ModeDevice != 0:
			e.bad = "device node"
		case m&fs.ModeNamedPipe != 0:
			e.bad = "fifo"
		default:
			e.bad = "unsupported entry type"
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// unpacker validates entries and, with extract set, writes them below root.
type unpacker struct {
	ctx     context.Context
	root    string
	strip   int
	extract bool
	count   int
	total   int64
	kinds   map[string]int      // relative path → kind of what is there (hard links count as files)
	files   map[string]fileMeta // relative path of a file → its mode and size
	links   map[string]string   // relative path of a symlink → its target
}

type fileMeta struct {
	exec bool
	size int64
}

// rel cleans an archive name and drops the stripped prefix. ok is false for entries above the
// stripped level.
func (u *unpacker) rel(name string) (string, bool, string) {
	if name == "" {
		return "", false, "empty name"
	}
	if strings.HasPrefix(name, "/") {
		return "", false, "absolute path"
	}
	if strings.ContainsRune(name, 0) {
		return "", false, "NUL byte in name"
	}
	var parts []string
	for p := range strings.SplitSeq(name, "/") {
		switch p {
		case "", ".":
		case "..":
			return "", false, "path leaves the archive root"
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) <= u.strip {
		return "", false, ""
	}
	return strings.Join(parts[u.strip:], "/"), true, ""
}

func (u *unpacker) add(e entry) error {
	u.count++
	if u.count > maxEntries {
		return &unpackErr{path: e.name, reason: fmt.Sprintf("more than %d entries", maxEntries)}
	}
	if u.count%4096 == 0 {
		if err := u.ctx.Err(); err != nil {
			return err
		}
	}
	if e.kind == kindSkip {
		return nil
	}
	if e.bad != "" {
		return &unpackErr{path: e.name, reason: e.bad}
	}
	rel, ok, why := u.rel(e.name)
	if why != "" {
		return &unpackErr{path: e.name, reason: why}
	}
	if !ok {
		if e.kind == kindDir {
			return nil
		}
		return &unpackErr{path: e.name, reason: "entry above the stripped top-level directory"}
	}
	// Every parent must be a directory this archive created: never write through a symlink.
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		switch u.kinds[rel[:i]] {
		case 0:
			u.kinds[rel[:i]] = kindDir
		case kindDir:
		case kindSymlink:
			return &unpackErr{path: e.name, reason: "path through a symlink"}
		default:
			return &unpackErr{path: e.name, reason: "path through a file"}
		}
	}
	if prev := u.kinds[rel]; prev != 0 {
		if prev == kindDir && e.kind == kindDir {
			return nil
		}
		return &unpackErr{path: e.name, reason: "duplicate entry"}
	}
	switch e.kind {
	case kindDir:
		u.kinds[rel] = kindDir
		if u.extract {
			return wrote(os.MkdirAll(u.abs(rel), 0o755))
		}
	case kindSymlink:
		if e.link == "" || strings.HasPrefix(e.link, "/") {
			return &unpackErr{path: e.name, reason: "symlink to an absolute path"}
		}
		if t := path.Join(path.Dir(rel), e.link); t == ".." || strings.HasPrefix(t, "../") {
			return &unpackErr{path: e.name, reason: "symlink pointing outside the archive"}
		}
		u.kinds[rel] = kindSymlink
		u.links[rel] = e.link
		if u.extract {
			if err := u.parents(rel); err != nil {
				return wrote(err)
			}
			return wrote(os.Symlink(e.link, u.abs(rel)))
		}
	case kindHardlink:
		target, ok, why := u.rel(e.link)
		if why != "" || !ok {
			return &unpackErr{path: e.name, reason: "hard link leaving the archive"}
		}
		if u.kinds[target] != kindFile {
			return &unpackErr{path: e.name, reason: "hard link to a missing or non-regular entry"}
		}
		t := u.files[target]
		return wrote(u.file(e.name, rel, t.exec, func() (io.Reader, error) {
			// A copy, read without following symlinks (the target is a regular file of ours).
			return os.OpenFile(u.abs(target), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		}, t.size))
	case kindFile:
		return wrote(u.file(e.name, rel, e.exec, e.open, e.size))
	}
	return nil
}

func (u *unpacker) file(name, rel string, exec bool, open func() (io.Reader, error), size int64) error {
	if size < 0 {
		return &unpackErr{path: name, reason: "invalid entry size"}
	}
	u.kinds[rel] = kindFile
	u.files[rel] = fileMeta{exec: exec, size: size}
	u.total += size
	if u.total > maxUnpacked {
		return &unpackErr{path: name, reason: "unpacked size above 8 GiB"}
	}
	if !u.extract {
		return nil
	}
	if err := u.parents(rel); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	out, err := os.OpenFile(u.abs(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	in, err := open()
	if err != nil {
		return err
	}
	if c, ok := in.(io.Closer); ok {
		defer c.Close()
	}
	// The declared size may lie (zip); never write more than it, nor past the total cap.
	n, err := io.Copy(out, io.LimitReader(in, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return &unpackErr{path: name, reason: "entry size differs from its header"}
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	return out.Close()
}

// checkLinks refuses the archive when any of its symlinks resolves outside the root. It runs
// after the whole archive was seen, because a link may go through links listed after it.
func (u *unpacker) checkLinks() error {
	names := make([]string, 0, len(u.links))
	for rel := range u.links {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		if !linkInside(u.links, rel) {
			return &unpackErr{path: rel, reason: "symlink pointing outside the archive"}
		}
	}
	return nil
}

// maxLinkHops bounds the symlinks followed while resolving one target (as Linux's 40).
const maxLinkHops = 40

// linkInside reports whether the symlink rel of links (relative path → target) resolves inside
// the root. The target is walked component by component from the link's directory and every
// component that is itself a symlink of the tree is substituted, so a chain such as
// "x -> d/up/../outside" with "d/up -> .." is caught: a lexical check of the target alone is not
// enough. The walk fails as soon as it leaves the root at any step, on an absolute target, and
// after maxLinkHops substitutions (loops).
func linkInside(links map[string]string, rel string) bool {
	var cur []string
	if d := path.Dir(rel); d != "." {
		cur = strings.Split(d, "/")
	}
	if t := links[rel]; t == "" || strings.HasPrefix(t, "/") {
		return false
	}
	todo := strings.Split(links[rel], "/")
	hops := 0
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return false
			}
			cur = cur[:len(cur)-1]
			continue
		}
		next := append(cur[:len(cur):len(cur)], c)
		t, ok := links[strings.Join(next, "/")]
		if !ok {
			cur = next
			continue
		}
		if hops++; hops > maxLinkHops || t == "" || strings.HasPrefix(t, "/") {
			return false
		}
		todo = append(strings.Split(t, "/"), todo...)
	}
	return true
}

func (u *unpacker) abs(rel string) string { return filepath.Join(u.root, filepath.FromSlash(rel)) }

func (u *unpacker) parents(rel string) error {
	if d := path.Dir(rel); d != "." {
		return os.MkdirAll(u.abs(d), 0o755)
	}
	return nil
}
