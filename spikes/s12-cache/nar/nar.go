// Package nar is a streaming, validating NAR reader. It never buffers file contents; it is
// used by the orchestrator to verify uploaded NARs (hostile input) and to build .ls listings.
package nar

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Limits for hostile input [proposal].
const (
	MaxNameLen   = 255
	MaxTargetLen = 4095
	MaxDepth     = 256
	MaxEntries   = 5_000_000
)

type Kind string

const (
	Regular   Kind = "regular"
	Directory Kind = "directory"
	Symlink   Kind = "symlink"
)

// Entry is one node seen while walking the NAR.
type Entry struct {
	Path       string // "" for the root, else "/a/b"
	Kind       Kind
	Size       uint64
	Executable bool
	Target     string
	NarOffset  uint64 // offset of file contents in the NAR (for .ls)
}

type reader struct {
	r       io.Reader
	off     uint64
	buf     [8]byte
	entries int
	fn      func(Entry) error
}

// Walk parses a NAR from r, calling fn for each node. It returns an error on any
// structural problem (bad magic, unsorted or duplicate names, bad names, truncated data,
// trailing garbage is not read). File contents are discarded in 64 KiB chunks.
func Walk(r io.Reader, fn func(Entry) error) error {
	rd := &reader{r: r, fn: fn}
	if err := rd.expect("nix-archive-1"); err != nil {
		return err
	}
	if err := rd.node("", 0); err != nil {
		return err
	}
	return nil
}

func (rd *reader) u64() (uint64, error) {
	if _, err := io.ReadFull(rd.r, rd.buf[:]); err != nil {
		return 0, fmt.Errorf("nar: truncated at %d: %w", rd.off, err)
	}
	rd.off += 8
	return binary.LittleEndian.Uint64(rd.buf[:]), nil
}

func (rd *reader) str(max int) (string, error) {
	n, err := rd.u64()
	if err != nil {
		return "", err
	}
	if n > uint64(max) {
		return "", fmt.Errorf("nar: string too long (%d) at %d", n, rd.off)
	}
	b := make([]byte, (n+7)&^7)
	if _, err := io.ReadFull(rd.r, b); err != nil {
		return "", fmt.Errorf("nar: truncated string at %d: %w", rd.off, err)
	}
	rd.off += uint64(len(b))
	for _, p := range b[n:] {
		if p != 0 {
			return "", errors.New("nar: non-zero padding")
		}
	}
	return string(b[:n]), nil
}

func (rd *reader) expect(s string) error {
	got, err := rd.str(64)
	if err != nil {
		return err
	}
	if got != s {
		return fmt.Errorf("nar: expected %q, got %q at %d", s, got, rd.off)
	}
	return nil
}

func (rd *reader) node(path string, depth int) error {
	if depth > MaxDepth {
		return errors.New("nar: too deep")
	}
	rd.entries++
	if rd.entries > MaxEntries {
		return errors.New("nar: too many entries")
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
	switch Kind(t) {
	case Regular:
		e := Entry{Path: path, Kind: Regular}
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
			return fmt.Errorf("nar: expected contents, got %q", tag)
		}
		if e.Size, err = rd.u64(); err != nil {
			return err
		}
		e.NarOffset = rd.off
		padded := (e.Size + 7) &^ 7
		if _, err := io.CopyN(io.Discard, rd.r, int64(e.Size)); err != nil {
			return fmt.Errorf("nar: truncated contents of %q: %w", path, err)
		}
		if pad := padded - e.Size; pad > 0 {
			if _, err := io.ReadFull(rd.r, rd.buf[:pad]); err != nil {
				return err
			}
			for _, p := range rd.buf[:pad] {
				if p != 0 {
					return errors.New("nar: non-zero padding")
				}
			}
		}
		rd.off += padded
		if err := rd.fn(e); err != nil {
			return err
		}
		return rd.expect(")")
	case Symlink:
		if err := rd.expect("target"); err != nil {
			return err
		}
		tg, err := rd.str(MaxTargetLen)
		if err != nil {
			return err
		}
		if tg == "" || strings.ContainsRune(tg, 0) {
			return errors.New("nar: bad symlink target")
		}
		if err := rd.fn(Entry{Path: path, Kind: Symlink, Target: tg}); err != nil {
			return err
		}
		return rd.expect(")")
	case Directory:
		if err := rd.fn(Entry{Path: path, Kind: Directory}); err != nil {
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
				return fmt.Errorf("nar: expected entry, got %q", tag)
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
				return fmt.Errorf("nar: bad name %q", name)
			}
			if prev != "" && name <= prev {
				return fmt.Errorf("nar: names not sorted/unique: %q after %q", name, prev)
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
		return fmt.Errorf("nar: bad type %q", t)
	}
}
