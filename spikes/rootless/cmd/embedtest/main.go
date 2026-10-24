// Command embedtest is a throwaway spike (S0b): a Go binary that embeds a gzip-compressed static
// Nix (stdlib only) and extracts it once to a content-addressed path on first use.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

//go:embed nix.gz
var nixGz []byte

func main() {
	dir := os.Args[1]
	sum := sha256.Sum256(nixGz)
	dst := filepath.Join(dir, "nix-"+hex.EncodeToString(sum[:8]))
	start := time.Now()
	if _, err := os.Stat(dst); err == nil {
		fmt.Printf("cached %s (%.2f ms)\n", dst, float64(time.Since(start).Microseconds())/1000)
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(nixGz))
	if err != nil {
		panic(err)
	}
	tmp, err := os.CreateTemp(dir, ".nix-*")
	if err != nil {
		panic(err)
	}
	n, err := io.Copy(tmp, zr)
	if err != nil {
		panic(err)
	}
	tmp.Chmod(0o555)
	tmp.Close()
	if err := os.Rename(tmp.Name(), dst); err != nil { // atomic: concurrent first runs are safe
		panic(err)
	}
	fmt.Printf("extracted %d bytes to %s (%.1f ms)\n", n, dst, float64(time.Since(start).Microseconds())/1000)
}
