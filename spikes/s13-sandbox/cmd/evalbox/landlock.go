package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock: a second, independent filesystem allow-list (the mount namespace is the first), plus
// "no TCP bind/connect at all" and scoping of abstract unix sockets and signals to the sandbox.

const (
	fsRead  = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	fsExec  = unix.LANDLOCK_ACCESS_FS_EXECUTE
	fsWrite = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK
)

func landlockABI() int {
	v, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if e != 0 {
		return 0
	}
	return int(v)
}

func applyLandlock(cfg Config) error {
	abi := landlockABI()
	if abi < 1 {
		return fmt.Errorf("landlock not available")
	}
	handled := uint64(0x1fff) // ABI 1
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	attr := unix.LandlockRulesetAttr{Handled_access_fs: handled}
	size := unsafe.Sizeof(uint64(0)) // ABI 1-3: only fs
	if abi >= 4 {
		attr.Handled_access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		size = 2 * unsafe.Sizeof(uint64(0))
	}
	if abi >= 6 {
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
		size = 3 * unsafe.Sizeof(uint64(0))
	}
	fd, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if e != 0 {
		return fmt.Errorf("create ruleset (abi %d): %w", abi, e)
	}
	defer unix.Close(int(fd))
	all := handled &^ unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	rules := []struct {
		path   string
		access uint64
	}{
		{"/nix/store", fsRead | fsExec},
		{"/src", fsRead},
		{"/gomod", fsRead},
		{"/etc", fsRead},
		{"/proc", fsRead},
		{"/work", all},
		{"/dev/null", unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
		{"/dev/zero", unix.LANDLOCK_ACCESS_FS_READ_FILE},
		{"/dev/full", unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
		{"/dev/random", unix.LANDLOCK_ACCESS_FS_READ_FILE},
		{"/dev/urandom", unix.LANDLOCK_ACCESS_FS_READ_FILE},
	}
	for _, r := range rules {
		pfd, err := unix.Open(r.path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			continue // path not present in this sandbox
		}
		acc := r.access & handled
		if fi, err := os.Stat(r.path); err == nil && !fi.IsDir() {
			acc &= unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_TRUNCATE
		}
		pb := unix.LandlockPathBeneathAttr{Allowed_access: acc, Parent_fd: int32(pfd)}
		_, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
		unix.Close(pfd)
		if e != 0 {
			return fmt.Errorf("add rule %s: %w", r.path, e)
		}
	}
	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); e != 0 {
		return fmt.Errorf("restrict self: %w", e)
	}
	if cfg.Timing {
		fmt.Fprintf(os.Stderr, "evalbox: landlock abi %d applied\n", abi)
	}
	return nil
}
