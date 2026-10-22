package nar

import (
	"io"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Write writes the NAR serialisation of the tree at root to w. With files non-nil only those
// slash-separated paths (relative to root, directories created as needed) are included, in
// canonical order.
func Write(w io.Writer, root string, files []string) error {
	return diag.NotImplemented("nar.Write")
}

// Hash returns the SHA-256 and the size of the NAR of the tree at root (filtered as in Write).
func Hash(root string, files []string) (sum [32]byte, size int64, err error) {
	return sum, 0, diag.NotImplemented("nar.Hash")
}
