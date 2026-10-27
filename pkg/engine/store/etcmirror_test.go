package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestMirrorEtc reproduces the NixOS layout of Q4 (A470): /etc entries are symlinks through
// /etc/static into the host store, which bob's namespace then covers. The fake host lives in a
// temporary directory: etc/ links into hidden/, and after mirrorEtc hidden/ is covered by an
// empty tmpfs exactly like /nix by bob's store. Every file must still be readable through etc/,
// links that do not touch the store stay as they were, and an /etc without such links is not
// mounted over at all. The mounts happen in a child of this test binary in a new user and
// mount namespace.
func TestMirrorEtc(t *testing.T) {
	if dir := os.Getenv("BOB_TEST_MIRROR_ETC"); dir != "" {
		mirrorEtcChild(t, dir)
		return
	}
	dir := t.TempDir()
	hidden := filepath.Join(dir, "hidden")
	etc := filepath.Join(dir, "etc")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	// hidden/aaa-etc/etc is NixOS' /etc/static; its entries link on into other store paths.
	write(filepath.Join(hidden, "bbb-hosts"), "127.0.0.1 localhost\n")
	write(filepath.Join(hidden, "ccc-nss-cacert", "ca-bundle.crt"), "CA\n")
	link(filepath.Join(hidden, "bbb-hosts"), filepath.Join(hidden, "aaa-etc", "etc", "hosts"))
	link(filepath.Join(hidden, "ccc-nss-cacert", "ca-bundle.crt"), filepath.Join(hidden, "aaa-etc", "etc", "ssl", "certs", "ca-certificates.crt"))
	link(filepath.Join(dir, "run", "resolv.conf"), filepath.Join(hidden, "aaa-etc", "etc", "resolv.conf"))
	write(filepath.Join(dir, "run", "resolv.conf"), "nameserver 192.0.2.1\n")
	link(filepath.Join(hidden, "aaa-etc", "etc"), filepath.Join(etc, "static"))
	link(filepath.Join(etc, "static", "hosts"), filepath.Join(etc, "hosts"))
	link("static/resolv.conf", filepath.Join(etc, "resolv.conf"))
	link(filepath.Join(etc, "static", "ssl", "certs", "ca-certificates.crt"), filepath.Join(etc, "ssl", "certs", "ca-certificates.crt"))
	write(filepath.Join(etc, "ssl", "certs", "local.pem"), "LOCAL\n")
	write(filepath.Join(etc, "passwd"), "root:x:0:0::/root:/bin/sh\n")
	write(filepath.Join(etc, "sub", "file"), "SUB\n")
	link("../proc/self/mounts", filepath.Join(etc, "mtab"))
	// A host without store links: nothing is mounted over it.
	plain := filepath.Join(dir, "plainetc")
	write(filepath.Join(plain, "hosts"), "127.0.0.1 localhost\n")

	cmd := exec.Command(os.Args[0], "-test.run=^TestMirrorEtc$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "BOB_TEST_MIRROR_ETC="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if testMountErrno() != nil || testCloneErrno() != nil {
			t.Skipf("no user namespaces with mounts here: %v\n%s", err, out)
		}
		t.Fatalf("child: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "MIRROR-OK") {
		t.Fatalf("child did not finish:\n%s", out)
	}
}

func mirrorEtcChild(t *testing.T, dir string) {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatalf("make / private: %v", err)
	}
	hidden := filepath.Join(dir, "hidden")
	etc := filepath.Join(dir, "etc")
	plain := filepath.Join(dir, "plainetc")
	if err := mirrorEtc(plain, filepath.Join(dir, "staging-plain"), hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging-plain")); err == nil {
		t.Error("an /etc without store links was mirrored")
	}
	if err := mirrorEtc(etc, filepath.Join(dir, "staging"), hidden); err != nil {
		t.Fatal(err)
	}
	// Cover the store like bob's namespace covers the host's /nix.
	if err := syscall.Mount("tmpfs", hidden, "tmpfs", 0, "mode=755"); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"hosts":                         "127.0.0.1 localhost\n",
		"resolv.conf":                   "nameserver 192.0.2.1\n",
		"ssl/certs/ca-certificates.crt": "CA\n",
		"ssl/certs/local.pem":           "LOCAL\n",
		"passwd":                        "root:x:0:0::/root:/bin/sh\n",
		"sub/file":                      "SUB\n",
	} {
		b, err := os.ReadFile(filepath.Join(etc, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("etc/%s after the store is covered: %q, %v; want %q", rel, b, err, want)
		}
	}
	// Nix's sandbox stats these paths: they must not be dangling links.
	for _, rel := range []string{"hosts", "resolv.conf", "ssl/certs/ca-certificates.crt"} {
		if _, err := os.Stat(filepath.Join(etc, filepath.FromSlash(rel))); err != nil {
			t.Errorf("stat etc/%s: %v", rel, err)
		}
	}
	if l, err := os.Readlink(filepath.Join(etc, "mtab")); err != nil || l != "../proc/self/mounts" {
		t.Errorf("etc/mtab = %q, %v; want the original link ../proc/self/mounts", l, err)
	}
	if !t.Failed() {
		t.Log("MIRROR-OK")
	}
}
