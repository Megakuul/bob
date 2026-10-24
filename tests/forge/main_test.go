package forge

import (
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// replayCert is the certificate of the in-process replay servers.
var replayCert tls.Certificate

// liveDir is the state directory root of the live forges (set before the first TLS use, see
// TestMain); liveCAs is added by the live files: it returns the PEM files the test process must
// trust.
var (
	liveDir string
	liveCAs []func(dir string) ([]string, error)
)

// TestMain makes the test process trust the harness' own certificates. The forge adapters have no
// CA option and Go reads SSL_CERT_FILE once, so the bundle must exist before the first TLS use:
// the replay server's certificate, and (with the live tags) the live forges', which are created
// in liveDir before the forges start.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bob-forge-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness error:", err)
		os.Exit(2)
	}
	liveDir = dir
	cert, certPEM, err := regenv.LoopbackCert()
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness error:", err)
		os.Exit(2)
	}
	replayCert = cert
	bundle := certPEM
	for _, mk := range liveCAs {
		files, err := mk(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "harness error:", err)
			os.Exit(2)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintln(os.Stderr, "harness error:", err)
				os.Exit(2)
			}
			bundle = append(append(bundle, '\n'), b...)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), bundle, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "harness error:", err)
		os.Exit(2)
	}
	os.Setenv("SSL_CERT_FILE", filepath.Join(dir, "ca.pem"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func pemBlock(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
