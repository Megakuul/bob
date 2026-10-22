//go:build !bobtest

package store

// Fault hooks of the rootless matrix (HARNESS.md §7.1) exist only with -tags bobtest.

func testCloneErrno() error { return nil }

func testMountErrno() error { return nil }
