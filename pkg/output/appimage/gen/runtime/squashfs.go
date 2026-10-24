package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// A minimal, read-only squashfs 4.0 reader: exactly what mksquashfs writes for a bundle
// (zstd, any block size, fragments, directories, regular files, symlinks). The image is
// untrusted input as far as memory goes: every size is bounded before it is allocated.

const (
	sqMagic        = 0x73717368
	sqZstd         = 6
	sqInvalidFrag  = 0xffffffff
	sqUncompressed = 1 << 24 // data/fragment size flag
	sqMetaMax      = 8192
	maxBlockSize   = 1 << 20
	maxName        = 256
	maxSymlink     = 4096
	maxDepth       = 256
)

// inode types
const (
	tDir     = 1
	tFile    = 2
	tSymlink = 3
	tExtDir  = 8
	tExtFile = 9
	tExtLink = 10
)

type squashfs struct {
	r         io.ReaderAt
	size      int64 // bytes available after the image start
	blockSize uint32
	flags     uint16
	rootRef   uint64
	inodes    int64 // inode table start
	dirs      int64 // directory table start
	frags     []fragment
	dec       *zstd.Decoder
	meta      map[int64]metaBlock // decompressed metadata blocks by position
	fragCache map[uint32][]byte   // recently decompressed fragment blocks (small files share them)
	buf       []byte              // data block buffer
}

type metaBlock struct {
	data []byte
	next int64 // position of the following block
}

type fragment struct {
	start uint64
	size  uint32
}

// inode is the part of an inode the extractor needs.
type inode struct {
	typ    uint16
	mode   uint16
	size   uint64   // file size or directory listing size
	blocks []uint32 // file data block sizes
	start  uint64   // file: first data block; dir: directory block (relative)
	frag   uint32
	fragOf uint32 // offset in the fragment
	offset uint32 // dir: offset in the directory block
	target string // symlink
}

func openSquashfs(r io.ReaderAt, size int64) (*squashfs, error) {
	var sb [96]byte
	if _, err := r.ReadAt(sb[:], 0); err != nil {
		return nil, fmt.Errorf("squashfs: superblock: %w", err)
	}
	le := binary.LittleEndian
	if le.Uint32(sb[0:]) != sqMagic {
		return nil, errors.New("squashfs: bad magic")
	}
	if v := le.Uint16(sb[28:]); v != 4 {
		return nil, fmt.Errorf("squashfs: version %d is not supported", v)
	}
	if c := le.Uint16(sb[20:]); c != sqZstd {
		return nil, fmt.Errorf("squashfs: compression %d is not supported (zstd only)", c)
	}
	s := &squashfs{r: r, size: size, blockSize: le.Uint32(sb[12:]), flags: le.Uint16(sb[24:]),
		rootRef: le.Uint64(sb[32:]), inodes: int64(le.Uint64(sb[64:])), dirs: int64(le.Uint64(sb[72:])),
		meta: map[int64]metaBlock{}, fragCache: map[uint32][]byte{}}
	if s.blockSize == 0 || s.blockSize > maxBlockSize {
		return nil, fmt.Errorf("squashfs: block size %d", s.blockSize)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return nil, err
	}
	s.dec = dec
	if n := le.Uint32(sb[16:]); n > 0 {
		if err := s.readFragments(int64(le.Uint64(sb[80:])), n); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *squashfs) close() { s.dec.Close() }

// readAt reads n bytes at off, refusing reads outside the image.
func (s *squashfs) readAt(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > s.size {
		return nil, fmt.Errorf("squashfs: read of %d bytes at %d is outside the image", n, off)
	}
	b := make([]byte, n)
	if _, err := s.r.ReadAt(b, off); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *squashfs) decompress(src []byte, max int) ([]byte, error) {
	return s.decompressInto(src, make([]byte, 0, max), max)
}

func (s *squashfs) decompressInto(src, dst []byte, max int) ([]byte, error) {
	out, err := s.dec.DecodeAll(src, dst[:0])
	if err != nil {
		return nil, fmt.Errorf("squashfs: zstd: %w", err)
	}
	if len(out) > max {
		return nil, errors.New("squashfs: block larger than allowed")
	}
	return out, nil
}

// metaAt returns the metadata block at pos (absolute).
func (s *squashfs) metaAt(pos int64) (metaBlock, error) {
	if m, ok := s.meta[pos]; ok {
		return m, nil
	}
	h, err := s.readAt(pos, 2)
	if err != nil {
		return metaBlock{}, err
	}
	hdr := binary.LittleEndian.Uint16(h)
	n := int(hdr & 0x7fff)
	if n == 0 || n > sqMetaMax {
		return metaBlock{}, fmt.Errorf("squashfs: metadata block at %d has size %d", pos, n)
	}
	raw, err := s.readAt(pos+2, n)
	if err != nil {
		return metaBlock{}, err
	}
	data := raw
	if hdr&0x8000 == 0 {
		if data, err = s.decompress(raw, sqMetaMax); err != nil {
			return metaBlock{}, err
		}
	}
	m := metaBlock{data: data, next: pos + 2 + int64(n)}
	s.meta[pos] = m
	return m, nil
}

// metaReader reads a byte stream that continues across metadata blocks.
type metaReader struct {
	s   *squashfs
	pos int64 // position of the current block
	off int   // offset in it
}

func (m *metaReader) read(n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		b, err := m.s.metaAt(m.pos)
		if err != nil {
			return nil, err
		}
		if m.off >= len(b.data) {
			m.pos, m.off = b.next, m.off-len(b.data)
			continue
		}
		k := min(n-len(out), len(b.data)-m.off)
		out = append(out, b.data[m.off:m.off+k]...)
		m.off += k
	}
	return out, nil
}

func (m *metaReader) u16() (uint16, error) {
	b, err := m.read(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (m *metaReader) u32() (uint32, error) {
	b, err := m.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (m *metaReader) u64() (uint64, error) {
	b, err := m.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

func (s *squashfs) readFragments(table int64, n uint32) error {
	if n > 1<<24 {
		return errors.New("squashfs: too many fragments")
	}
	blocks := (int(n)*16 + sqMetaMax - 1) / sqMetaMax
	idx, err := s.readAt(table, blocks*8)
	if err != nil {
		return err
	}
	s.frags = make([]fragment, 0, n)
	for i := 0; i < blocks && len(s.frags) < int(n); i++ {
		m := &metaReader{s: s, pos: int64(binary.LittleEndian.Uint64(idx[i*8:]))}
		for j := 0; j < sqMetaMax/16 && len(s.frags) < int(n); j++ {
			b, err := m.read(16)
			if err != nil {
				return err
			}
			s.frags = append(s.frags, fragment{start: binary.LittleEndian.Uint64(b), size: binary.LittleEndian.Uint32(b[8:])})
		}
	}
	return nil
}

// inodeAt reads the inode referenced by ref (block << 16 | offset).
func (s *squashfs) inodeAt(ref uint64) (*inode, error) {
	m := &metaReader{s: s, pos: s.inodes + int64(ref>>16), off: int(ref & 0xffff)}
	hdr, err := m.read(16)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	in := &inode{typ: le.Uint16(hdr), mode: le.Uint16(hdr[2:])}
	switch in.typ {
	case tDir:
		b, err := m.read(16)
		if err != nil {
			return nil, err
		}
		in.start, in.size, in.offset = uint64(le.Uint32(b)), uint64(le.Uint16(b[8:])), uint32(le.Uint16(b[10:]))
	case tExtDir:
		b, err := m.read(24)
		if err != nil {
			return nil, err
		}
		in.size, in.start, in.offset = uint64(le.Uint32(b[4:])), uint64(le.Uint32(b[8:])), uint32(le.Uint16(b[18:]))
	case tFile, tExtFile:
		if in.typ == tFile {
			b, err := m.read(16)
			if err != nil {
				return nil, err
			}
			in.start, in.frag, in.fragOf, in.size = uint64(le.Uint32(b)), le.Uint32(b[4:]), le.Uint32(b[8:]), uint64(le.Uint32(b[12:]))
		} else {
			b, err := m.read(40)
			if err != nil {
				return nil, err
			}
			in.start, in.size, in.frag, in.fragOf = le.Uint64(b), le.Uint64(b[8:]), le.Uint32(b[28:]), le.Uint32(b[32:])
		}
		if in.size > 1<<40 {
			return nil, errors.New("squashfs: file too large")
		}
		n := in.size / uint64(s.blockSize)
		if in.frag == sqInvalidFrag && in.size%uint64(s.blockSize) != 0 {
			n++
		}
		in.blocks = make([]uint32, n)
		for i := range in.blocks {
			if in.blocks[i], err = m.u32(); err != nil {
				return nil, err
			}
		}
	case tSymlink, tExtLink:
		b, err := m.read(8)
		if err != nil {
			return nil, err
		}
		n := le.Uint32(b[4:])
		if n > maxSymlink {
			return nil, errors.New("squashfs: symlink target too long")
		}
		t, err := m.read(int(n))
		if err != nil {
			return nil, err
		}
		in.target = string(t)
	default:
		return nil, fmt.Errorf("squashfs: inode type %d is not supported", in.typ)
	}
	return in, nil
}

type dirent struct {
	name string
	ref  uint64
}

// readDir lists a directory inode.
func (s *squashfs) readDir(in *inode) ([]dirent, error) {
	if in.size < 3 {
		return nil, nil
	}
	m := &metaReader{s: s, pos: s.dirs + int64(in.start), off: int(in.offset)}
	left := int64(in.size) - 3
	var out []dirent
	for left > 0 {
		count, err := m.u32()
		if err != nil {
			return nil, err
		}
		start, err := m.u32()
		if err != nil {
			return nil, err
		}
		if _, err := m.u32(); err != nil { // inode number base
			return nil, err
		}
		left -= 12
		if count >= 256 {
			return nil, errors.New("squashfs: bad directory header")
		}
		for i := uint32(0); i <= count; i++ {
			b, err := m.read(8)
			if err != nil {
				return nil, err
			}
			off := binary.LittleEndian.Uint16(b)
			n := int(binary.LittleEndian.Uint16(b[6:])) + 1
			if n > maxName {
				return nil, errors.New("squashfs: name too long")
			}
			name, err := m.read(n)
			if err != nil {
				return nil, err
			}
			left -= 8 + int64(n)
			nm := string(name)
			if nm == "." || nm == ".." || nm == "" || path.Base(nm) != nm {
				return nil, fmt.Errorf("squashfs: bad name %q", nm)
			}
			out = append(out, dirent{name: nm, ref: uint64(start)<<16 | uint64(off)})
		}
	}
	return out, nil
}

// copyFile writes the contents of a file inode to w.
func (s *squashfs) copyFile(in *inode, w io.Writer) error {
	pos := int64(in.start)
	left := in.size
	for _, b := range in.blocks {
		want := min(uint64(s.blockSize), left)
		n := int(b &^ sqUncompressed)
		var data []byte
		switch {
		case n == 0: // sparse block
			data = make([]byte, want)
		case b&sqUncompressed != 0:
			raw, err := s.readAt(pos, n)
			if err != nil {
				return err
			}
			data = raw
		default:
			raw, err := s.readAt(pos, n)
			if err != nil {
				return err
			}
			if cap(s.buf) < int(s.blockSize) {
				s.buf = make([]byte, 0, s.blockSize)
			}
			if data, err = s.decompressInto(raw, s.buf, int(s.blockSize)); err != nil {
				return err
			}
			s.buf = data
		}
		pos += int64(n)
		if uint64(len(data)) < want {
			return errors.New("squashfs: short data block")
		}
		if _, err := w.Write(data[:want]); err != nil {
			return err
		}
		left -= want
	}
	if left == 0 {
		return nil
	}
	if in.frag == sqInvalidFrag || int(in.frag) >= len(s.frags) {
		return errors.New("squashfs: file tail without fragment")
	}
	data, err := s.fragment(in.frag)
	if err != nil {
		return err
	}
	end := uint64(in.fragOf) + left
	if end > uint64(len(data)) {
		return errors.New("squashfs: fragment too short")
	}
	_, err = w.Write(data[in.fragOf:end])
	return err
}

// fragment returns the contents of fragment block i.
func (s *squashfs) fragment(i uint32) ([]byte, error) {
	if b, ok := s.fragCache[i]; ok {
		return b, nil
	}
	f := s.frags[i]
	n := int(f.size &^ sqUncompressed)
	data, err := s.readAt(int64(f.start), n)
	if err != nil {
		return nil, err
	}
	if f.size&sqUncompressed == 0 {
		if data, err = s.decompress(data, int(s.blockSize)); err != nil {
			return nil, err
		}
	}
	if len(s.fragCache) >= 4 {
		clear(s.fragCache)
	}
	s.fragCache[i] = data
	return data, nil
}

// walk calls fn for every entry below the root in depth-first, name order; rel is the
// slash-separated path. fn returning skip=true does not descend into a directory.
func (s *squashfs) walk(fn func(rel string, in *inode) (skip bool, err error)) error {
	root, err := s.inodeAt(s.rootRef)
	if err != nil {
		return err
	}
	return s.walkDir("", root, 0, fn)
}

func (s *squashfs) walkDir(rel string, dir *inode, depth int, fn func(string, *inode) (bool, error)) error {
	if depth > maxDepth {
		return errors.New("squashfs: directories nested too deeply")
	}
	ents, err := s.readDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		in, err := s.inodeAt(e.ref)
		if err != nil {
			return err
		}
		p := path.Join(rel, e.name)
		skip, err := fn(p, in)
		if err != nil {
			return err
		}
		if !skip && (in.typ == tDir || in.typ == tExtDir) {
			if err := s.walkDir(p, in, depth+1, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// extract writes the entries selected by keep (nil: all) below dst, which must exist.
// Directories are created writable by the owner so that a cache directory can be removed.
func (s *squashfs) extract(dst string, keep func(rel string, dir bool) bool) error {
	return s.walk(func(rel string, in *inode) (bool, error) {
		isDir := in.typ == tDir || in.typ == tExtDir
		if keep != nil && !keep(rel, isDir) {
			return false, nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch in.typ {
		case tDir, tExtDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return false, err
			}
		case tSymlink, tExtLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return false, err
			}
			if err := os.Symlink(in.target, target); err != nil {
				return false, err
			}
		case tFile, tExtFile:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return false, err
			}
			perm := os.FileMode(in.mode&0o777) | 0o200
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				return false, err
			}
			err = s.copyFile(in, f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return false, fmt.Errorf("%s: %w", rel, err)
			}
		}
		return false, nil
	})
}
