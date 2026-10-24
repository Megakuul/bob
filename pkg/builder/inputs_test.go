package builder

import (
	"slices"
	"strings"
	"syscall"
	"testing"
)

// The mounts below the store directory are found with their read-only state; the store
// directory itself (where outputs are created) and everything else are not listed.
func TestParseMountinfo(t *testing.T) {
	info := strings.Join([]string{
		`22 1 0:21 / / rw,relatime - tmpfs none rw`,
		`23 22 0:22 /store /nix/store rw,relatime shared:1 - ext4 /dev/sda1 rw`,
		`24 23 0:22 /store/aaa-bash /nix/store/aaa-bash rw,nosuid,relatime - ext4 /dev/sda1 rw`,
		`25 23 0:22 /store/bbb-glibc /nix/store/bbb-glibc ro,relatime - ext4 /dev/sda1 rw`,
		`26 23 0:22 /store/ccc-a\040b /nix/store/ccc-a\040b rw - ext4 /dev/sda1 rw`,
		`27 22 0:23 / /nix/storex rw - tmpfs none rw`,
		`28 22 0:24 / /build rw - tmpfs none rw`,
	}, "\n")
	got, err := parseMountinfo(strings.NewReader(info), "/nix/store/")
	if err != nil {
		t.Fatal(err)
	}
	want := []mount{{"/nix/store/aaa-bash", false}, {"/nix/store/bbb-glibc", true}, {"/nix/store/ccc-a b", false}}
	if !slices.Equal(got, want) {
		t.Errorf("parseMountinfo = %v, want %v", got, want)
	}
	if _, err := parseMountinfo(strings.NewReader("1 2 3"), "/nix/store"); err == nil {
		t.Errorf("a malformed line was accepted")
	}
	if _, err := parseMountinfo(strings.NewReader(`1 2 0:1 / /nix/store/x\04 rw - ext4 x rw`), "/nix/store"); err == nil {
		t.Errorf("a truncated escape was accepted")
	}
}

// The remount adds MS_RDONLY and keeps the flags a less privileged namespace may not clear.
func TestRemountFlags(t *testing.T) {
	base := uintptr(syscall.MS_REMOUNT | syscall.MS_BIND | syscall.MS_RDONLY)
	for _, c := range []struct {
		statfs int64
		want   uintptr
	}{
		{0, base},
		{syscall.MS_NOSUID | syscall.MS_NODEV, base | syscall.MS_NOSUID | syscall.MS_NODEV},
		{syscall.MS_NOEXEC | stRelatime, base | syscall.MS_NOEXEC | msRelatime},
		{syscall.MS_NOATIME | syscall.MS_NODIRATIME | syscall.MS_RDONLY, base | syscall.MS_NOATIME | syscall.MS_NODIRATIME},
	} {
		if got := remountFlags(c.statfs); got != c.want {
			t.Errorf("remountFlags(%#x) = %#x, want %#x", c.statfs, got, c.want)
		}
	}
}

// Outside a sandbox (no mount below the store directory) the job runs in this process.
func TestLockInputsWithoutStoreMounts(t *testing.T) {
	t.Setenv("NIX_STORE", "/nonexistent-bob-store")
	var log strings.Builder
	if code, done := LockInputs([]string{"bob-builder", "run"}, &log); done {
		t.Fatalf("LockInputs took over (exit %d): %s", code, log.String())
	}
}
