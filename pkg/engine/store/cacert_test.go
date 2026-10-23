package store

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestCABundle(t *testing.T) {
	n := 0
	for rest := CABundle(); ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			t.Fatalf("certificate %d of the embedded CA bundle: %v", n+1, err)
		}
		n++
	}
	if n < 100 {
		t.Errorf("the embedded CA bundle holds %d certificates, want the Mozilla set (> 100)", n)
	}
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "x")
	if err := os.WriteFile(notPEM, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCAFile(notPEM); err == nil {
		t.Error("readCAFile accepted a file without a certificate")
	}
	if _, err := readCAFile(dir); err == nil {
		t.Error("readCAFile accepted a directory")
	}
}

// TestEnsureNixCA: without Nix's default CA file, Nix's second candidate below bob's /nix holds
// a bundle afterwards.
func TestEnsureNixCA(t *testing.T) {
	home := t.TempDir()
	dst := filepath.Join(home, filepath.FromSlash(nixProfileCAFile))
	if err := ensureNixCAFor(home, filepath.Join(home, "missing")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || !x509.NewCertPool().AppendCertsFromPEM(b) {
		t.Fatalf("no CA bundle for Nix: %v", err)
	}
	// With Nix's default present nothing is written.
	other := t.TempDir()
	if err := ensureNixCAFor(other, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, filepath.FromSlash(nixProfileCAFile))); err == nil {
		t.Error("a CA bundle was written although Nix's default file exists")
	}
}
