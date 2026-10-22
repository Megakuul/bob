package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
)

// dec reads Nix wire primitives (test-side decoder of the export format).
type dec struct {
	b []byte
	t *testing.T
}

func (d *dec) num() uint64 {
	if len(d.b) < 8 {
		d.t.Fatalf("short stream")
	}
	n := binary.LittleEndian.Uint64(d.b)
	d.b = d.b[8:]
	return n
}

func (d *dec) bytes() []byte {
	n := int(d.num())
	p := (n + 7) / 8 * 8
	out := d.b[:n]
	d.b = d.b[p:]
	return out
}

// narEnd returns the length of the NAR at the start of b (single regular file only).
func narContents(t *testing.T, d *dec) []byte {
	for _, want := range []string{"nix-archive-1", "(", "type", "regular", "contents"} {
		if got := string(d.bytes()); got != want {
			t.Fatalf("NAR token %q, want %q", got, want)
		}
	}
	c := d.bytes()
	if got := string(d.bytes()); got != ")" {
		t.Fatalf("NAR end %q", got)
	}
	return append([]byte(nil), c...)
}

// TestWriteExportMatchesNix re-encodes the export streams captured from `nix-store --export`
// (tests/nix/testdata/export) from their decoded items and expects the same bytes.
func TestWriteExportMatchesNix(t *testing.T) {
	for _, f := range []string{"dep.export", "dep-st.export"} {
		want, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "nix", "testdata", "export", f))
		if err != nil {
			t.Fatal(err)
		}
		d := &dec{b: want, t: t}
		var items []ExportItem
		for d.num() == 1 {
			var it ExportItem
			it.NAR = flatNAR(narContents(t, d))
			if d.num() != exportMagic {
				t.Fatalf("%s: no export magic", f)
			}
			it.Path = string(d.bytes())
			for n := d.num(); n > 0; n-- {
				it.References = append(it.References, string(d.bytes()))
			}
			it.Deriver = string(d.bytes())
			if d.num() != 0 {
				t.Fatalf("%s: signature present", f)
			}
			items = append(items, it)
		}
		var got bytes.Buffer
		if err := WriteExport(&got, items); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: WriteExport differs from nix-store --export", f)
		}
	}
}

func TestWriteExportOrder(t *testing.T) {
	a := ExportItem{Path: "/nix/store/00000000000000000000000000000000-a", NAR: flatNAR(nil)}
	b := ExportItem{Path: "/nix/store/11111111111111111111111111111111-b", NAR: flatNAR(nil), References: []string{a.Path}}
	if err := WriteExport(&bytes.Buffer{}, []ExportItem{b, a}); err == nil {
		t.Errorf("a reference after its referrer was accepted")
	}
	if err := WriteExport(&bytes.Buffer{}, []ExportItem{{Path: "/tmp/x"}}); err == nil {
		t.Errorf("a non-store path was accepted")
	}
}

func TestNixConfAndNetrc(t *testing.T) {
	subs := []Substituter{{URL: "https://bob.example.com/cache", PublicKey: "bob-1:abc", NetrcPassword: "bob_dt_x"}}
	conf := string(nixConf("/h", subs))
	for _, want := range []string{
		"substituters = https://cache.nixos.org https://bob.example.com/cache\n",
		"trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= bob-1:abc\n",
		"netrc-file = /h/etc/nix/netrc\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nix.conf lacks %q:\n%s", want, conf)
		}
	}
	rc, err := netrc(subs)
	if err != nil || string(rc) != "machine bob.example.com login bob password bob_dt_x\n" {
		t.Errorf("netrc = %q, %v", rc, err)
	}
	if _, err := netrc([]Substituter{{URL: "https://x", NetrcPassword: "a b"}}); err == nil {
		t.Errorf("a password with a space was written to netrc")
	}
}

func TestHomeDir(t *testing.T) {
	t.Setenv("BOB_HOME", "")
	t.Setenv("XDG_DATA_HOME", "/data")
	if h, _ := HomeDir(""); h != "/data/bob" {
		t.Errorf("HomeDir = %s", h)
	}
	t.Setenv("BOB_HOME", "/b")
	if h, _ := HomeDir(""); h != "/b" {
		t.Errorf("HomeDir = %s", h)
	}
	if h, _ := HomeDir("/x/"); h != "/x" {
		t.Errorf("HomeDir = %s", h)
	}
}

func refusalOf(t *testing.T, err error) *diag.Error {
	t.Helper()
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 7005 {
		t.Fatalf("got %v, want BOB-E-7005", err)
	}
	return d
}

func TestRefusalClassification(t *testing.T) {
	if _, err := os.Stat("/etc/NIXOS"); err == nil {
		t.Skip("NixOS host")
	}
	d := refusalOf(t, cloneRefusal(&os.SyscallError{Syscall: "clone", Err: syscall.ENOSPC}))
	if d.Args["fix"] != fixMaxUserNS || !strings.Contains(d.Cause, "max_user_namespaces") {
		t.Errorf("ENOSPC: cause %q fix %q", d.Cause, d.Args["fix"])
	}
	if sysctl("kernel.unprivileged_userns_clone") == "" && sysctl("kernel.apparmor_restrict_unprivileged_userns") == "" {
		d = refusalOf(t, cloneRefusal(&os.SyscallError{Syscall: "clone", Err: syscall.EPERM}))
		if d.Args["fix"] != fixContainer || !strings.Contains(d.Cause, "seccomp") {
			t.Errorf("EPERM: cause %q fix %q", d.Cause, d.Args["fix"])
		}
	}
	d = refusalOf(t, appArmorRefusal("EPERM"))
	exe, _ := os.Readlink("/proc/self/exe")
	for _, want := range []string{"profile bob " + exe + " flags=(unconfined) {", "sudo apparmor_parser -r /etc/apparmor.d/bob", "60-bob-userns.conf", "abi <abi/4.0>,"} {
		if !strings.Contains(d.Args["fix"], want) {
			t.Errorf("AppArmor fix lacks %q:\n%s", want, d.Args["fix"])
		}
	}
	if !strings.Contains(d.Cause, "AppArmor") {
		t.Errorf("AppArmor cause %q", d.Cause)
	}
}

// openTest opens a store in a fresh home (the namespace must work on the test host).
func openTest(t *testing.T) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := Open(ctx, Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenLayoutAndOnlyStat(t *testing.T) {
	s := openTest(t)
	bin := s.BinDir()
	if !regexp.MustCompile(`/bin/nix-[0-9a-f]{12}/bin$`).MatchString(bin) {
		t.Errorf("bin dir %s", bin)
	}
	for _, l := range nixLinks {
		if to, err := os.Readlink(filepath.Join(bin, l)); err != nil || to != "nix" {
			t.Errorf("%s -> %q %v", l, to, err)
		}
	}
	conf, _ := os.ReadFile(filepath.Join(s.Home(), "etc", "nix", "nix.conf"))
	if !bytes.Equal(conf, nixConf(s.Home(), nil)) {
		t.Errorf("nix.conf differs")
	}
	before, _ := os.Stat(filepath.Join(bin, "nix"))
	confBefore, _ := os.Stat(filepath.Join(s.Home(), "etc", "nix", "nix.conf"))
	time.Sleep(10 * time.Millisecond)
	s2, err := Open(context.Background(), Config{Home: s.Home()})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(s2.BinDir(), "nix"))
	confAfter, _ := os.Stat(filepath.Join(s.Home(), "etc", "nix", "nix.conf"))
	if !before.ModTime().Equal(after.ModTime()) || !confBefore.ModTime().Equal(confAfter.ModTime()) {
		t.Errorf("second Open rewrote the extracted Nix or nix.conf")
	}
}

func TestInvalidAndImport(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	content := []byte("hello from bob\n")
	nar := flatNAR(content)
	// makeStorePath("source", sha256(NAR), name) computed independently of pkg/engine/drv.
	sum := sha256.Sum256(nar)
	path := sourcePathForTest(t, sum, "bob-import-test")
	inv, err := s.Invalid(ctx, []string{path})
	if err != nil || len(inv) != 1 || inv[0] != path {
		t.Fatalf("Invalid before import = %v, %v", inv, err)
	}
	if err := s.Import(ctx, []ExportItem{{Path: path, NAR: nar}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	inv, err = s.Invalid(ctx, []string{path})
	if err != nil || len(inv) != 0 {
		t.Fatalf("Invalid after import = %v, %v", inv, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.Home(), path)); err != nil || !bytes.Equal(got, content) {
		t.Errorf("imported file = %q, %v", got, err)
	}
	// The host's /nix is untouched (R-NIX-025).
	if _, err := os.Stat(path); err == nil && !inNamespace(s.Home()) {
		t.Errorf("%s exists in the host store", path)
	}
}

// sourcePathForTest is Nix's makeStorePath for a "source" path without references.
func sourcePathForTest(t *testing.T, narSum [32]byte, name string) string {
	t.Helper()
	s := "source:sha256:" + hexString(narSum[:]) + ":/nix/store:" + name
	h := sha256.Sum256([]byte(s))
	var c [20]byte
	for i := range h {
		c[i%20] ^= h[i]
	}
	return "/nix/store/" + base32(c[:]) + "-" + name
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&15])
	}
	return string(out)
}

func base32(b []byte) string {
	n := (len(b)*8-1)/5 + 1
	out := make([]byte, 0, n)
	for i := n - 1; i >= 0; i-- {
		bit := i * 5
		j, k := bit/8, uint(bit%8)
		c := b[j] >> k
		if j+1 < len(b) {
			c |= b[j+1] << (8 - k)
		}
		out = append(out, nixBase32Alphabet[c&0x1f])
	}
	return string(out)
}

func TestCommandEnvironment(t *testing.T) {
	s := openTest(t)
	var out bytes.Buffer
	c := s.Command(context.Background(), "nix", "--version")
	c.Stdout = &out
	if err := c.Run(); err != nil {
		t.Fatalf("nix --version: %v", err)
	}
	if !strings.Contains(out.String(), "2.34.9") {
		t.Errorf("nix --version = %q", out.String())
	}
	t.Setenv("NIX_PATH", "nixpkgs=/x")
	t.Setenv("NIX_REMOTE", "daemon")
	for _, kv := range s.NixEnv() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "NIX_PATH" || k == "NIX_REMOTE" {
			t.Errorf("NixEnv passes %s", kv)
		}
	}
}

// TestOpenRefusal runs only where user namespaces are blocked, e.g. under
// `unshare -Ur sh -c 'echo 0 > /proc/sys/user/max_user_namespaces; <test binary>'`: Open
// refuses with BOB-E-7005 and the max_user_namespaces fix, and creates no store.
func TestOpenRefusal(t *testing.T) {
	if sysctl("user.max_user_namespaces") != "0" {
		t.Skip("user namespaces are not blocked here")
	}
	home := filepath.Join(t.TempDir(), "bobhome")
	_, err := Open(context.Background(), Config{Home: home})
	d := refusalOf(t, err)
	if d.Args["fix"] != fixMaxUserNS {
		t.Errorf("fix %q", d.Args["fix"])
	}
	if _, err := os.Stat(filepath.Join(home, "nix", "store")); err == nil {
		t.Errorf("a store exists after the refusal")
	}
	if _, _, err := Enter(context.Background(), Config{Home: home}); err == nil {
		t.Errorf("Enter did not refuse")
	}
}

// TestEnter re-executes this test binary through Enter; the child (TestEnterChild) checks that
// it runs inside bob's namespace.
func TestEnter(t *testing.T) {
	if os.Getenv("BOB_STORE_ENTER_CHILD") != "" {
		t.Skip("child")
	}
	home := filepath.Join(t.TempDir(), "bobhome")
	if _, err := Open(context.Background(), Config{Home: home}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "child")
	t.Setenv("BOB_STORE_ENTER_CHILD", out)
	t.Setenv("BOB_HOME", home)
	saved := os.Args
	os.Args = []string{saved[0], "-test.run=^TestEnterChild$", "-test.count=1"}
	inside, exit, err := Enter(context.Background(), Config{Home: home})
	os.Args = saved
	if err != nil || inside || exit != 0 {
		t.Fatalf("Enter = %v, %d, %v", inside, exit, err)
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.HasPrefix(string(b), "inside ") {
		t.Fatalf("child reported %q, %v", b, err)
	}
	t.Logf("child: %s", b)
}

func TestEnterChild(t *testing.T) {
	out := os.Getenv("BOB_STORE_ENTER_CHILD")
	if out == "" {
		t.Skip("run by TestEnter")
	}
	d, ok := Entered()
	inside, _, err := Enter(context.Background(), Config{})
	s, oerr := Open(context.Background(), Config{})
	msg := "outside"
	if ok && inside && err == nil && oerr == nil && s.inside {
		msg = "inside " + d.String()
	}
	if err := os.WriteFile(out, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentOpenImport: four stores opened concurrently on one fresh home (state creation and
// extraction race) all import into the shared store without errors (R-NIX-026).
func TestConcurrentOpenImport(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bobhome")
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			s, err := Open(context.Background(), Config{Home: home})
			if err != nil {
				errs <- err
				return
			}
			nar := flatNAR([]byte{byte(i), 'x'})
			p := sourcePathForTest(t, sha256.Sum256(nar), "concurrent")
			shared := flatNAR([]byte("shared"))
			q := sourcePathForTest(t, sha256.Sum256(shared), "shared")
			errs <- s.Import(context.Background(), []ExportItem{{Path: p, NAR: nar}, {Path: q, NAR: shared}})
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent store %d: %v", i, err)
		}
	}
	m, _ := filepath.Glob(filepath.Join(home, "bin", "*"))
	if len(m) != 1 {
		t.Errorf("<home>/bin holds %v, want one nix-<h>", m)
	}
}

type modResolver map[string]*drv.Derivation

func (m modResolver) Modulo(p string) ([32]byte, error) { return m[p].Modulo(m) }

// TestImportDrvs: two dependent derivations are imported in one stream (dependency first, even
// when given in the other order); a second import imports nothing; the imported .drv is valid
// and Nix reads it back.
func TestImportDrvs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dep := &drv.Derivation{Name: "bob-dep", System: "aarch64-linux", Builder: "/bin/sh", Args: []string{"-c", "echo"}, Outputs: []drv.Output{{Name: "out"}}}
	if err := dep.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	depPath, _ := dep.Path()
	top := &drv.Derivation{Name: "bob-top", System: "aarch64-linux", Builder: "/bin/sh", Args: []string{"-c", "echo"},
		Outputs: []drv.Output{{Name: "out"}}, InputDrvs: []drv.InputDrv{{Path: depPath, Outputs: []string{"out"}}}}
	r := modResolver{depPath: dep}
	if err := top.Finalize(r); err != nil {
		t.Fatal(err)
	}
	topPath, _ := top.Path()
	n, err := s.ImportDrvs(ctx, []*drv.Derivation{top, dep})
	if err != nil || n != 2 {
		t.Fatalf("ImportDrvs = %d, %v", n, err)
	}
	if n, err := s.ImportDrvs(ctx, []*drv.Derivation{top, dep}); err != nil || n != 0 {
		t.Errorf("second ImportDrvs = %d, %v; want 0", n, err)
	}
	var out bytes.Buffer
	c := s.Command(ctx, "nix-store", "-q", "--references", topPath)
	c.Stdout = &out
	if err := c.Run(); err != nil || strings.TrimSpace(out.String()) != depPath {
		t.Errorf("references of %s = %q, %v", topPath, out.String(), err)
	}
	c = s.Command(ctx, "nix-store", "-q", "--outputs", topPath)
	out.Reset()
	c.Stdout = &out
	if err := c.Run(); err != nil || strings.TrimSpace(out.String()) != top.Outputs[0].Path {
		t.Errorf("outputs of %s = %q, %v; want %s", topPath, out.String(), err, top.Outputs[0].Path)
	}
}

func TestDrvCache(t *testing.T) {
	s := openTest(t)
	c := s.DrvCache()
	k := DrvKey([]byte(`{"id":"//:a"}`), []string{"/nix/store/b", "/nix/store/a"}, "cc/1", "/nix/store/x-bob-builder-1")
	if k != DrvKey([]byte(`{"id":"//:a"}`), []string{"/nix/store/a", "/nix/store/b"}, "cc/1", "/nix/store/x-bob-builder-1") {
		t.Errorf("DrvKey depends on the order of input drvs")
	}
	if k == DrvKey([]byte(`{"id":"//:a"}`), []string{"/nix/store/a", "/nix/store/b"}, "cc/2", "/nix/store/x-bob-builder-1") {
		t.Errorf("DrvKey ignores the generator version")
	}
	if _, ok := c.Get(k); ok {
		t.Errorf("hit on an empty cache")
	}
	p := "/nix/store/00000000000000000000000000000000-a.drv"
	if err := c.Put(k, p); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get(k); !ok || got != p {
		t.Errorf("Get = %q, %v", got, ok)
	}
	if err := c.Put(k, "/etc/passwd"); err == nil {
		t.Errorf("Put accepted a non-drv path")
	}
	if ents, _ := os.ReadDir(filepath.Join(s.Home(), "cache", "drv")); len(ents) == 0 {
		t.Errorf("<home>/cache/drv is empty")
	}
}
