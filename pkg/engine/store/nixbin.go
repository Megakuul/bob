package store

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// nixLinks are the legacy command names; Nix dispatches on argv[0], so they are symlinks to nix
// (rootless-results "Embedding").
var nixLinks = []string{"nix-store", "nix-build", "nix-instantiate", "nix-env", "nix-hash", "nix-daemon"}

// embeddedNix returns the hex sha256 of the embedded Nix binary (from nix.sha256, written with
// it by scripts/fetch-nix.sh), or a 7003 error for a bob built without it.
func embeddedNix() (string, error) {
	b, err := nixFS.ReadFile(nixDir + "/nix.sha256")
	sum := strings.TrimSpace(string(b))
	if err != nil || len(sum) != 64 {
		return "", diag.New(7003, "what", "this bob was built without its embedded Nix (run scripts/fetch-nix.sh before go build)")
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return "", diag.New(7003, "what", "the embedded Nix has an invalid checksum")
	}
	return sum, nil
}

// nixBinDir is <home>/bin/nix-<first 12 hex of sha256>/bin.
func nixBinDir(home, sum string) string {
	return filepath.Join(home, "bin", "nix-"+sum[:12], "bin")
}

// extractNix makes <home>/bin/nix-<h>/bin/nix (with the legacy symlinks) exist and returns its
// bin directory. An existing directory is only stat'ed. Otherwise the binary is decompressed into
// a temporary directory <home>/bin/.nix-<h>-*, checked against its sha256, synced, and renamed
// into place atomically; a concurrent extraction that wins the rename is accepted.
func extractNix(home string) (string, error) {
	sum, err := embeddedNix()
	if err != nil {
		return "", err
	}
	bin := nixBinDir(home, sum)
	final := filepath.Dir(bin)
	if fi, err := os.Stat(filepath.Join(bin, "nix")); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", accessError(err)
	}
	tmp, err := os.MkdirTemp(parent, ".nix-"+sum[:12]+"-")
	if err != nil {
		return "", accessError(err)
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", accessError(err)
	}
	if err := writeNix(filepath.Join(tmp, "bin"), sum); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		// Another bob process extracted the same Nix first.
		if fi, serr := os.Stat(filepath.Join(bin, "nix")); serr == nil && fi.Mode().IsRegular() {
			return bin, nil
		}
		return "", accessError(err)
	}
	if d, err := os.Open(parent); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return bin, nil
}

// writeNix decompresses the embedded Nix to dir/nix (mode 0555), verifies it and adds the links.
func writeNix(dir, sum string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return accessError(err)
	}
	src, err := nixFS.Open(nixDir + "/nix.gz")
	if err != nil {
		return diag.New(7003, "what", "this bob was built without its embedded Nix (run scripts/fetch-nix.sh before go build)")
	}
	defer src.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(src, 1<<20))
	if err != nil {
		return diag.New(7003, "what", "the embedded Nix is corrupt")
	}
	path := filepath.Join(dir, "nix")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return accessError(err)
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(f, h), zr)
	if cerr == nil {
		cerr = f.Sync()
	}
	if err := f.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		var pe *os.PathError
		if errors.As(cerr, &pe) {
			return accessError(cerr)
		}
		return diag.New(7003, "what", fmt.Sprintf("the embedded Nix cannot be extracted (%v)", cerr))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return diag.New(7003, "what", "the embedded Nix does not match its checksum")
	}
	if err := os.Chmod(path, 0o555); err != nil {
		return accessError(err)
	}
	for _, l := range nixLinks {
		if err := os.Symlink("nix", filepath.Join(dir, l)); err != nil {
			return accessError(err)
		}
	}
	return nil
}
