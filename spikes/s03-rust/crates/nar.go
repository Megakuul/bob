package crates

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// narNode is an in-memory tree of an unpacked archive.
type narNode struct {
	kind     byte // 'd' dir, 'f' file, 'l' symlink
	exec     bool
	data     []byte
	target   string
	children map[string]*narNode
}

// UnpackedNarHash computes the Nix NAR hash (SRI) of a .crate as it looks after unpacking with
// its single top-level directory stripped — the output of a recursive fixed-output fetch.
func UnpackedNarHash(crateFile string) (string, error) {
	f, err := os.Open(crateFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	root := &narNode{kind: 'd', children: map[string]*narNode{}}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return "", fmt.Errorf("unsafe path %q in crate", h.Name)
		}
		parts := strings.Split(name, "/")
		dir := root
		for _, p := range parts[:len(parts)-1] {
			c := dir.children[p]
			if c == nil {
				c = &narNode{kind: 'd', children: map[string]*narNode{}}
				dir.children[p] = c
			}
			dir = c
		}
		leaf := parts[len(parts)-1]
		switch h.Typeflag {
		case tar.TypeDir:
			if dir.children[leaf] == nil {
				dir.children[leaf] = &narNode{kind: 'd', children: map[string]*narNode{}}
			}
		case tar.TypeReg, tar.TypeRegA:
			total += h.Size
			if total > 512<<20 {
				return "", fmt.Errorf("crate unpacks to more than 512 MiB")
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return "", err
			}
			dir.children[leaf] = &narNode{kind: 'f', exec: h.Mode&0o100 != 0, data: b}
		case tar.TypeSymlink:
			dir.children[leaf] = &narNode{kind: 'l', target: h.Linkname}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return "", fmt.Errorf("unsupported tar entry %q (type %c)", h.Name, h.Typeflag)
		}
	}
	if len(root.children) != 1 {
		return "", fmt.Errorf("crate has %d top-level entries, want 1", len(root.children))
	}
	var top *narNode
	for _, c := range root.children {
		top = c
	}
	hh := sha256.New()
	str(hh, "nix-archive-1")
	writeNar(hh, top)
	return "sha256-" + base64.StdEncoding.EncodeToString(hh.Sum(nil)), nil
}

func str(w hash.Hash, s string) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(len(s)))
	w.Write(b[:])
	w.Write([]byte(s))
	if pad := (8 - len(s)%8) % 8; pad > 0 {
		w.Write(make([]byte, pad))
	}
}

func writeNar(w hash.Hash, n *narNode) {
	str(w, "(")
	switch n.kind {
	case 'f':
		str(w, "type")
		str(w, "regular")
		if n.exec {
			str(w, "executable")
			str(w, "")
		}
		str(w, "contents")
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(len(n.data)))
		w.Write(b[:])
		w.Write(n.data)
		if pad := (8 - len(n.data)%8) % 8; pad > 0 {
			w.Write(make([]byte, pad))
		}
	case 'l':
		str(w, "type")
		str(w, "symlink")
		str(w, "target")
		str(w, n.target)
	case 'd':
		str(w, "type")
		str(w, "directory")
		names := make([]string, 0, len(n.children))
		for k := range n.children {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			str(w, "entry")
			str(w, "(")
			str(w, "name")
			str(w, k)
			str(w, "node")
			writeNar(w, n.children[k])
			str(w, ")")
		}
	}
	str(w, ")")
}
