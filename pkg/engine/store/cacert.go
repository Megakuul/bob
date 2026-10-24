package store

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// caBundle is the Mozilla CA bundle of nixpkgs nss-cacert 3.126 (MPL-2.0, ATTRIBUTION.md).
// bob uses it only where the host has no CA bundle (minimal container images, A242).
//
//go:embed cacert/ca-bundle.crt
var caBundle []byte

// CABundle returns the embedded CA bundle (PEM). cmd/bob installs it as Go's fallback roots
// (crypto/x509.SetFallbackRoots: used only when the system has no root certificates).
func CABundle() []byte { return bytes.Clone(caBundle) }

// nixDefaultCAFile is the first CA file Nix looks for when NIX_SSL_CERT_FILE and SSL_CERT_FILE
// are unset (bob passes neither, R-NIX-024); nixProfileCAFile is the second, below bob's /nix.
const (
	nixDefaultCAFile = "/etc/ssl/certs/ca-certificates.crt"
	nixProfileCAFile = "nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt" // relative to <home>
)

// hostCAFiles are the system bundles Go's crypto/x509 reads on Linux (root_linux.go), after
// Nix's own default.
var hostCAFiles = []string{
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL 6
	"/etc/ssl/ca-bundle.pem",                            // openSUSE
	"/etc/pki/tls/cacert.pem",                           // OpenELEC
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS, RHEL 7
	"/etc/ssl/cert.pem",                                 // Alpine
}

// maxCAFile caps a host CA bundle bob copies (real ones are well below 1 MiB).
const maxCAFile = 16 << 20

// ensureNixCA gives Nix a CA bundle on hosts where its default file is missing: Nix then reads
// <home>/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt (/nix/var/... inside the
// namespace). Its content is $SSL_CERT_FILE, else the host's bundle at a known place, else the
// embedded one. Without it substitution from https caches fails on such hosts. The file is
// rewritten only when its content changes (atomic rename); concurrent bob processes write the
// same bytes.
func ensureNixCA(home string) error { return ensureNixCAFor(home, nixDefaultCAFile) }

// ensureNixCAFor is ensureNixCA with Nix's default file given (tests).
func ensureNixCAFor(home, nixDefault string) error {
	if _, err := os.Stat(nixDefault); err == nil {
		return nil
	}
	want := caBundle
	cands := hostCAFiles
	if f := os.Getenv("SSL_CERT_FILE"); f != "" {
		cands = append([]string{f}, cands...)
	}
	for _, f := range cands {
		if b, err := readCAFile(f); err == nil {
			want = b
			break
		}
	}
	dst := filepath.Join(home, filepath.FromSlash(nixProfileCAFile))
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return accessError(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".ca-bundle-*")
	if err != nil {
		return accessError(err)
	}
	_, werr := tmp.Write(want)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		_ = os.Remove(tmp.Name())
		return accessError(err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return accessError(err)
	}
	return nil
}

// readCAFile reads a host CA bundle: a regular file (symlinks followed) of bounded size that
// holds at least one PEM certificate.
func readCAFile(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxCAFile {
		return nil, fmt.Errorf("%s: %w", p, fs.ErrInvalid)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(b, []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, fmt.Errorf("%s: no certificate", p)
	}
	return b, nil
}
