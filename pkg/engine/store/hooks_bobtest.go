//go:build bobtest

package store

import (
	"os"
	"syscall"
)

// Fault hooks for the simulated refusal cases of R-NIX-022 (HARNESS.md §7.1), compiled in only
// with -tags bobtest:
//
//	BOB_TEST_CLONE_ERRNO=ENOSPC|EPERM|EACCES   the namespace clone fails with that errno
//	BOB_TEST_MOUNT_ERRNO=EPERM                 the first mount inside the namespace fails (exit 125)

func errnoFromEnv(name string) error {
	switch os.Getenv(name) {
	case "ENOSPC":
		return syscall.ENOSPC
	case "EPERM":
		return syscall.EPERM
	case "EACCES":
		return syscall.EACCES
	}
	return nil
}

func testCloneErrno() error {
	if err := errnoFromEnv("BOB_TEST_CLONE_ERRNO"); err != nil {
		return &os.SyscallError{Syscall: "clone", Err: err}
	}
	return nil
}

func testMountErrno() error { return errnoFromEnv("BOB_TEST_MOUNT_ERRNO") }
