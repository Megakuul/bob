// Package nix holds the acceptance tests of SPEC §01's derivation layer (R-NIX-*): derivation
// and store-path computation, sources and fetching, the bob-builder contract, fixup, the
// embedded rootless Nix, the nixpkgs index and the build pipeline.
//
// The host's own `nix` binary is used only as an oracle on throw-away chroot stores (it computes
// the paths bob must reproduce); bob never executes it. Tests that exercise a stubbed feature end
// with harness.Red / harness.RedFeature, so every red is "not implemented yet".
package nix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
)

// notImplemented reports whether err is the BOB-E-9008 answer of a stub and, if so, fails t as red.
func notImplemented(t testing.TB, err error, feature string) bool {
	t.Helper()
	var de *diag.Error
	if errors.As(err, &de) && de.Code == diag.CodeNotImplemented {
		harness.RedFeature(t, feature)
		return true
	}
	return false
}

// oracle is the host's nix operating on a private chroot store: logical store dir /nix/store,
// real files under root/nix/store. Paths it prints are the ones bob must compute.
type oracle struct {
	t    testing.TB
	root string
	home string
}

func newOracle(t testing.TB) *oracle {
	t.Helper()
	if _, err := exec.LookPath("nix"); err != nil {
		t.Fatalf("harness: the host nix oracle is missing (HARNESS.md §14 needs nix on the test host): %v", err)
	}
	return &oracle{t: t, root: filepath.Join(t.TempDir(), "store"), home: t.TempDir()}
}

func (o *oracle) cmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	full := append([]string{"--store", o.root}, args...)
	if name == "nix" {
		full = append([]string{"--extra-experimental-features", "nix-command"}, full...)
	}
	cmd := exec.CommandContext(ctx, name, full...)
	conf := filepath.Join(o.home, "conf")
	_ = os.MkdirAll(conf, 0o755)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + o.home, "NIX_CONF_DIR=" + conf, "NIX_USER_CONF_FILES=", "TMPDIR=" + o.home}
	return cmd
}

// run runs nix with args (the --store flag is added) and returns trimmed stdout.
func (o *oracle) run(args ...string) string {
	o.t.Helper()
	return o.runIn("", nil, "nix", args...)
}

func (o *oracle) runIn(dir string, stdin []byte, name string, args ...string) string {
	o.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := o.cmd(ctx, name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		o.t.Fatalf("harness: %s %s: %v: %s", name, strings.Join(args, " "), err, se.String())
	}
	return strings.TrimSpace(so.String())
}

// addNar is `nix store add --mode nar --name <name> <dir>`.
func (o *oracle) addNar(dir, name string) string {
	o.t.Helper()
	return o.run("store", "add", "--mode", "nar", "--name", name, dir)
}

// addFlat is `nix store add --mode flat --hash-algo <algo> --name <name> <file>`.
func (o *oracle) addFlat(file, name, algo string) string {
	o.t.Helper()
	return o.run("store", "add", "--mode", "flat", "--hash-algo", algo, "--name", name, file)
}

// narHashHex is the lowercase hex SHA-256 of the NAR of dir.
func (o *oracle) narHashHex(dir string) string {
	o.t.Helper()
	return o.run("hash", "path", "--type", "sha256", "--base16", dir)
}

// nixBase32 encodes b in Nix's base-32 alphabet (needed to recompute the `-<hash8>` suffix of
// long store names independently of the code under test).
func nixBase32(b []byte) string {
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
	n := (len(b)*8-1)/5 + 1
	out := make([]byte, 0, n)
	for i := n - 1; i >= 0; i-- {
		bit := i * 5
		j, k := bit/8, uint(bit%8)
		c := b[j] >> k
		if j+1 < len(b) {
			c |= b[j+1] << (8 - k)
		}
		out = append(out, alphabet[c&0x1f])
	}
	return string(out)
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return b
}

func mustWrite(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("harness: %v", err)
	}
}

// TestNixBase32 checks the harness' own base-32 encoder against Nix.
func TestNixBase32(t *testing.T) {
	o := newOracle(t)
	empty := sha256.Sum256(nil)
	want := o.run("hash", "convert", "--to", "nix32", "--hash-algo", "sha256", hex.EncodeToString(empty[:]))
	if got := nixBase32(empty[:]); got != want {
		t.Errorf("nixBase32 = %s, nix says %s", got, want)
	}
}
