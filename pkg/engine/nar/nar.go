package nar

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// filter is the set of paths to include below one directory. A nil filter (or all) includes
// everything below.
type filter struct {
	all      bool
	children map[string]*filter
}

func newFilter(files []string) (*filter, error) {
	if files == nil {
		return nil, nil
	}
	root := &filter{children: map[string]*filter{}}
	for _, f := range files {
		clean := path.Clean(f)
		if f == "" || path.IsAbs(f) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("nar: file %q is not a relative path inside the root", f)
		}
		n := root
		for part := range strings.SplitSeq(clean, "/") {
			c := n.children[part]
			if c == nil {
				c = &filter{children: map[string]*filter{}}
				n.children[part] = c
			}
			n = c
		}
		n.all = true
	}
	return root, nil
}

// Write writes the NAR serialisation of the tree at root to w. With files non-nil only those
// slash-separated paths (relative to root, directories created as needed) are included, in
// canonical order. A listed directory is included with everything below it.
func Write(w io.Writer, root string, files []string) error {
	f, err := newFilter(files)
	if err != nil {
		return err
	}
	// A bytes.Buffer needs no buffering; anything else gets a small buffer (many NARs are
	// tiny source trees, R-NIX-009).
	bw, buffered := w.(*bytes.Buffer)
	var flush func() error
	nw := &writer{w: bw}
	if !buffered {
		b := bufio.NewWriterSize(w, 8<<10)
		nw.w, flush = b, b.Flush
	}
	nw.str("nix-archive-1")
	if err := nw.node(root, f); err != nil {
		return err
	}
	if nw.err != nil {
		return fmt.Errorf("nar: write: %w", nw.err)
	}
	if flush != nil {
		if err := flush(); err != nil {
			return fmt.Errorf("nar: write: %w", err)
		}
	}
	return nil
}

// bufWriter is what the NAR writer writes to (*bytes.Buffer or *bufio.Writer).
type bufWriter interface {
	io.Writer
	io.StringWriter
	io.ReaderFrom
}

// Hash returns the SHA-256 and the size of the NAR of the tree at root (filtered as in Write).
func Hash(root string, files []string) (sum [32]byte, size int64, err error) {
	h := sha256.New()
	cw := &countWriter{w: h}
	if err := Write(cw, root, files); err != nil {
		return sum, 0, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, cw.n, nil
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

// writer emits NAR tokens; the first write error sticks and is reported by Write.
type writer struct {
	w   bufWriter
	err error
	buf [8]byte
}

func (n *writer) u64(v uint64) {
	if n.err != nil {
		return
	}
	binary.LittleEndian.PutUint64(n.buf[:], v)
	_, n.err = n.w.Write(n.buf[:])
}

func (n *writer) pad(size uint64) {
	if r := size % 8; r != 0 && n.err == nil {
		var zeros [8]byte
		_, n.err = n.w.Write(zeros[:8-r])
	}
}

func (n *writer) str(s string) {
	n.u64(uint64(len(s)))
	if n.err == nil {
		_, n.err = n.w.WriteString(s)
	}
	n.pad(uint64(len(s)))
}

func (n *writer) node(p string, f *filter) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("nar: %w", err)
	}
	n.str("(")
	switch {
	case fi.Mode().IsRegular():
		if f != nil && !f.all {
			return fmt.Errorf("nar: %s is a file, but paths below it were listed", p)
		}
		n.str("type")
		n.str("regular")
		if fi.Mode()&0o100 != 0 {
			n.str("executable")
			n.str("")
		}
		if err := n.contents(p, fi.Size()); err != nil {
			return err
		}
	case fi.Mode()&os.ModeSymlink != 0:
		if f != nil && !f.all {
			return fmt.Errorf("nar: %s is a symlink, but paths below it were listed", p)
		}
		target, err := os.Readlink(p)
		if err != nil {
			return fmt.Errorf("nar: %w", err)
		}
		n.str("type")
		n.str("symlink")
		n.str("target")
		n.str(target)
	case fi.IsDir():
		n.str("type")
		n.str("directory")
		names, err := entries(p, f)
		if err != nil {
			return err
		}
		for _, name := range names {
			var sub *filter
			if f != nil && !f.all {
				sub = f.children[name]
			}
			n.str("entry")
			n.str("(")
			n.str("name")
			n.str(name)
			n.str("node")
			if err := n.node(filepath.Join(p, name), sub); err != nil {
				return err
			}
			n.str(")")
		}
	default:
		return fmt.Errorf("nar: %s has unsupported file type %s", p, fi.Mode().Type())
	}
	n.str(")")
	return n.err
}

// entries lists the names to serialise in directory p, sorted bytewise as Nix does.
func entries(p string, f *filter) ([]string, error) {
	var names []string
	if f != nil && !f.all {
		for name := range f.children {
			names = append(names, name)
		}
	} else {
		d, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("nar: %w", err)
		}
		names, err = d.Readdirnames(-1)
		_ = d.Close()
		if err != nil {
			return nil, fmt.Errorf("nar: %s: %w", p, err)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (n *writer) contents(p string, size int64) error {
	n.str("contents")
	n.u64(uint64(size))
	if n.err != nil {
		return n.err
	}
	fh, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("nar: %w", err)
	}
	defer fh.Close()
	m, err := n.w.ReadFrom(io.LimitReader(fh, size))
	if err != nil {
		return fmt.Errorf("nar: %s: %w", p, err)
	}
	if m != size {
		return fmt.Errorf("nar: %s changed while being read", p)
	}
	n.pad(uint64(size))
	return n.err
}
