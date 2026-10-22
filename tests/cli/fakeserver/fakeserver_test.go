package fakeserver

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNix32 checks the base-32 encoding against the well-known hash of the empty input.
func TestNix32(t *testing.T) {
	sum := sha256.Sum256(nil)
	if got, want := nix32(sum[:]), "0mdqa9w1p6cmli6976v4wi0sw9r4p5prkj7lzfd1877wk11c9c73"; got != want {
		t.Errorf("nix32(sha256(\"\")) = %s, want %s", got, want)
	}
}

// TestNARRestores checks the NAR writer with the host's nix-store, when there is one.
func TestNARRestores(t *testing.T) {
	path, err := exec.LookPath("nix-store")
	if err != nil {
		t.Logf("nix-store not found: NAR layout checked by TestNix32 and the install suite only")
		return
	}
	var b bytes.Buffer
	writeNAR(&b, []string{"acme", "acme-migrate"}, []byte("#!x\n"))
	dst := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command(path, "--restore", dst)
	cmd.Stdin = &b
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nix-store --restore: %v: %s", err, out)
	}
	st, err := os.Stat(filepath.Join(dst, "bin", "acme-migrate"))
	if err != nil || st.Mode()&0o100 == 0 {
		t.Errorf("restored bin/acme-migrate: %v %v", st, err)
	}
}
