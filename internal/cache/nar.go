package cache

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// NAR limits for hostile input (SPEC §6.6.4 step 3b).
const (
	MaxNARSize    = 64 << 30
	MaxNAREntries = 5_000_000
	MaxNameLen    = 255
	MaxTargetLen  = 4095
	MaxNARDepth   = 256
)

// ErrBadNAR marks a structurally invalid NAR.
var ErrBadNAR = errors.New("invalid NAR")

// NarEntry is one node of a NAR, in archive order (depth first, names sorted).
type NarEntry struct {
	Path       string // "" for the root, else "/a/b"
	Type       string // regular | directory | symlink
	Size       uint64
	Executable bool
	Target     string
	Offset     uint64 // NAR offset of a regular file's contents
}

type narReader struct {
	r       *bufio.Reader
	off     uint64
	buf     [8]byte
	entries int
	visit   func(NarEntry) error
}

// WalkNAR parses one NAR from r, calls visit for each node and then checks that nothing
// follows the root. File contents are read and discarded (callers hash r themselves).
func WalkNAR(r io.Reader, visit func(NarEntry) error) error {
	rd := &narReader{r: bufio.NewReaderSize(r, 256<<10), visit: visit}
	if err := rd.expect("nix-archive-1"); err != nil {
		return err
	}
	if err := rd.node("", 0); err != nil {
		return err
	}
	if n, err := io.Copy(io.Discard, rd.r); err != nil {
		return err
	} else if n > 0 {
		return fmt.Errorf("%w: %d bytes after the root", ErrBadNAR, n)
	}
	return nil
}

func (rd *narReader) bad(format string, args ...any) error {
	return fmt.Errorf("%w at byte %d: %s", ErrBadNAR, rd.off, fmt.Sprintf(format, args...))
}

func (rd *narReader) u64() (uint64, error) {
	if _, err := io.ReadFull(rd.r, rd.buf[:]); err != nil {
		return 0, rd.truncated(err)
	}
	rd.off += 8
	return binary.LittleEndian.Uint64(rd.buf[:]), nil
}

func (rd *narReader) truncated(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return rd.bad("truncated")
	}
	return err
}

func (rd *narReader) padding(n uint64) error {
	if p := (8 - n%8) % 8; p > 0 {
		if _, err := io.ReadFull(rd.r, rd.buf[:p]); err != nil {
			return rd.truncated(err)
		}
		rd.off += p
		for _, b := range rd.buf[:p] {
			if b != 0 {
				return rd.bad("non-zero padding")
			}
		}
	}
	return nil
}

func (rd *narReader) str(max int) (string, error) {
	n, err := rd.u64()
	if err != nil {
		return "", err
	}
	if n > uint64(max) {
		return "", rd.bad("string of %d bytes (max %d)", n, max)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(rd.r, b); err != nil {
		return "", rd.truncated(err)
	}
	rd.off += n
	return string(b), rd.padding(n)
}

func (rd *narReader) expect(s string) error {
	got, err := rd.str(64)
	if err != nil {
		return err
	}
	if got != s {
		return rd.bad("expected %q, got %q", s, got)
	}
	return nil
}

func (rd *narReader) node(path string, depth int) error {
	if depth > MaxNARDepth {
		return rd.bad("deeper than %d", MaxNARDepth)
	}
	if rd.entries++; rd.entries > MaxNAREntries {
		return rd.bad("more than %d entries", MaxNAREntries)
	}
	if err := rd.expect("("); err != nil {
		return err
	}
	if err := rd.expect("type"); err != nil {
		return err
	}
	t, err := rd.str(16)
	if err != nil {
		return err
	}
	switch t {
	case "regular":
		e := NarEntry{Path: path, Type: t}
		tag, err := rd.str(16)
		if err != nil {
			return err
		}
		if tag == "executable" {
			e.Executable = true
			if err := rd.expect(""); err != nil {
				return err
			}
			if tag, err = rd.str(16); err != nil {
				return err
			}
		}
		if tag != "contents" {
			return rd.bad("expected contents, got %q", tag)
		}
		if e.Size, err = rd.u64(); err != nil {
			return err
		}
		if e.Size > MaxNARSize {
			return rd.bad("file larger than %d bytes", uint64(MaxNARSize))
		}
		e.Offset = rd.off
		if _, err := io.CopyN(io.Discard, rd.r, int64(e.Size)); err != nil {
			return rd.truncated(err)
		}
		rd.off += e.Size
		if err := rd.padding(e.Size); err != nil {
			return err
		}
		if err := rd.visit(e); err != nil {
			return err
		}
		return rd.expect(")")
	case "symlink":
		if err := rd.expect("target"); err != nil {
			return err
		}
		tg, err := rd.str(MaxTargetLen)
		if err != nil {
			return err
		}
		if tg == "" || strings.ContainsRune(tg, 0) {
			return rd.bad("bad symlink target")
		}
		if err := rd.visit(NarEntry{Path: path, Type: t, Target: tg}); err != nil {
			return err
		}
		return rd.expect(")")
	case "directory":
		if err := rd.visit(NarEntry{Path: path, Type: t}); err != nil {
			return err
		}
		prev := ""
		for {
			tag, err := rd.str(16)
			if err != nil {
				return err
			}
			if tag == ")" {
				return nil
			}
			if tag != "entry" {
				return rd.bad("expected entry, got %q", tag)
			}
			if err := rd.expect("("); err != nil {
				return err
			}
			if err := rd.expect("name"); err != nil {
				return err
			}
			name, err := rd.str(MaxNameLen)
			if err != nil {
				return err
			}
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
				return rd.bad("bad name %q", name)
			}
			if prev != "" && name <= prev {
				return rd.bad("names not sorted or not unique: %q after %q", name, prev)
			}
			prev = name
			if err := rd.expect("node"); err != nil {
				return err
			}
			if err := rd.node(path+"/"+name, depth+1); err != nil {
				return err
			}
			if err := rd.expect(")"); err != nil {
				return err
			}
		}
	default:
		return rd.bad("bad type %q", t)
	}
}

// Listing writes the `.ls` JSON (listing version 1, as Nix's binary cache writes it) while a
// NAR is walked: feed it every NarEntry in archive order, then call Close.
type Listing struct {
	w     *bufio.Writer
	open  []string // paths of the open directories
	first []bool   // per open directory: no entry written yet
	err   error
}

// NewListing starts a listing on w.
func NewListing(w io.Writer) *Listing {
	l := &Listing{w: bufio.NewWriterSize(w, 64<<10)}
	l.write(`{"version":1,"root":`)
	return l
}

func (l *Listing) write(s string) {
	if l.err == nil {
		_, l.err = l.w.WriteString(s)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}

func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// Add records e.
func (l *Listing) Add(e NarEntry) error {
	if e.Path != "" {
		parent := parentOf(e.Path)
		for len(l.open) > 0 && l.open[len(l.open)-1] != parent {
			l.closeDir()
		}
		if len(l.open) == 0 {
			return fmt.Errorf("%w: entry %q outside the root", ErrBadNAR, e.Path)
		}
		top := len(l.first) - 1
		if !l.first[top] {
			l.write(",")
		}
		l.first[top] = false
		l.write(jsonString(e.Path[len(parent)+1:]) + ":")
	}
	switch e.Type {
	case "directory":
		l.write(`{"type":"directory","entries":{`)
		l.open = append(l.open, e.Path)
		l.first = append(l.first, true)
	case "regular":
		l.write(`{"type":"regular","size":` + strconv.FormatUint(e.Size, 10))
		if e.Executable {
			l.write(`,"executable":true`)
		}
		l.write(`,"narOffset":` + strconv.FormatUint(e.Offset, 10) + "}")
	case "symlink":
		l.write(`{"type":"symlink","target":` + jsonString(e.Target) + "}")
	}
	return l.err
}

func (l *Listing) closeDir() {
	l.write("}}")
	l.open = l.open[:len(l.open)-1]
	l.first = l.first[:len(l.first)-1]
}

// Close finishes the document and flushes it.
func (l *Listing) Close() error {
	for len(l.open) > 0 {
		l.closeDir()
	}
	l.write("}")
	if l.err != nil {
		return l.err
	}
	return l.w.Flush()
}
