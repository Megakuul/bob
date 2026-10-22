package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"sort"

	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/nar"
)

// ExportItem is one path of an export stream: the NAR of the path, its references and deriver.
// The NAR is either held in memory (NAR) or written on demand by WriteNAR, which keeps large
// trees out of memory (F5): WriteNAR may be called more than once (an import is retried when
// the store database is busy) and must write the same bytes every time or fail; an error it
// returns aborts the stream before the item is complete, so the path is not imported.
type ExportItem struct {
	Path       string
	NAR        []byte
	WriteNAR   func(io.Writer) error
	References []string
	Deriver    string
}

// TreeItem is the streamed export item of the tree (or single file) at root, restricted to files
// as in nar.Write. With narSHA256 set, the NAR written is checked against it before the item is
// completed, so a tree that changed after its store path was computed is refused instead of
// imported under a wrong path.
func TreeItem(path, root string, files []string, narSHA256 *[32]byte) ExportItem {
	return ExportItem{Path: path, WriteNAR: func(w io.Writer) error {
		var h hash.Hash
		if narSHA256 != nil {
			h = sha256.New()
			w = io.MultiWriter(w, h)
		}
		if err := nar.Write(w, root, files); err != nil {
			return err
		}
		if h != nil && !bytes.Equal(h.Sum(nil), narSHA256[:]) {
			return fmt.Errorf("%s changed while it was imported", root)
		}
		return nil
	}}
}

// exportMagic follows each NAR in the stream ("NIXE", little-endian).
const exportMagic = 0x4558494e

// WriteExport writes items as an export stream of format v1; items must be topologically
// ordered (references first).
//
// The format is Nix's `nix-store --export`: per item the number 1, the NAR, the magic
// 0x4558494e, the path, the references (count, then each), the deriver ("" for none) and 0 (no
// signature); after the last item the number 0. Numbers are 64-bit little-endian; strings are
// length-prefixed and zero-padded to 8 bytes.
func WriteExport(w io.Writer, items []ExportItem) error {
	pos := make(map[string]int, len(items))
	for i, it := range items {
		if !isStorePath(it.Path) {
			return fmt.Errorf("store: export: %q is not a store path", it.Path)
		}
		if _, dup := pos[it.Path]; dup {
			return fmt.Errorf("store: export: %s appears twice", it.Path)
		}
		pos[it.Path] = i
	}
	for i, it := range items {
		for _, r := range it.References {
			if j, ok := pos[r]; ok && j > i {
				return fmt.Errorf("store: export: %s comes before its reference %s", it.Path, r)
			}
		}
	}
	bw := bufio.NewWriterSize(w, 1<<16)
	e := &enc{w: bw}
	for _, it := range items {
		e.num(1)
		if it.WriteNAR != nil && it.NAR == nil {
			if e.err == nil {
				if err := it.WriteNAR(bw); err != nil {
					return fmt.Errorf("store: export %s: %w", it.Path, err)
				}
			}
		} else {
			e.raw(it.NAR)
		}
		e.num(exportMagic)
		e.str(it.Path)
		e.num(uint64(len(it.References)))
		for _, r := range it.References {
			e.str(r)
		}
		e.str(it.Deriver)
		e.num(0)
	}
	e.num(0)
	if e.err != nil {
		return fmt.Errorf("store: export: %w", e.err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("store: export: %w", err)
	}
	return nil
}

// enc writes Nix wire primitives, remembering the first error.
type enc struct {
	w   io.Writer
	err error
}

func (e *enc) raw(b []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(b)
	}
}

func (e *enc) num(n uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	e.raw(b[:])
}

var zeros [8]byte

func (e *enc) bytes(b []byte) {
	e.num(uint64(len(b)))
	e.raw(b)
	if pad := (8 - len(b)%8) % 8; pad > 0 {
		e.raw(zeros[:pad])
	}
}

func (e *enc) str(s string) { e.bytes([]byte(s)) }

// flatNAR is the NAR of a single non-executable regular file with contents b.
func flatNAR(b []byte) []byte {
	var out bytes.Buffer
	e := &enc{w: &out}
	for _, s := range []string{"nix-archive-1", "(", "type", "regular", "contents"} {
		e.str(s)
	}
	e.bytes(b)
	e.str(")")
	return out.Bytes()
}

// DrvExport is the export item of a finalised derivation: the .drv file with the input
// derivations and sources as references.
func DrvExport(d *drv.Derivation) (ExportItem, error) {
	path, err := d.Path()
	if err != nil {
		return ExportItem{}, err
	}
	aterm, err := d.ATerm()
	if err != nil {
		return ExportItem{}, err
	}
	refs := make([]string, 0, len(d.InputDrvs)+len(d.InputSrcs))
	seen := map[string]bool{}
	for _, in := range d.InputDrvs {
		if !seen[in.Path] {
			seen[in.Path] = true
			refs = append(refs, in.Path)
		}
	}
	for _, src := range d.InputSrcs {
		if !seen[src] {
			seen[src] = true
			refs = append(refs, src)
		}
	}
	sort.Strings(refs)
	return ExportItem{Path: path, NAR: flatNAR(aterm), References: refs}, nil
}
