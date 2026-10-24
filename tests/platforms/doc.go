// Package platforms holds the acceptance tests of SPEC §4.1 platforms (R-PLAT-001, 004, 005, 006,
// 007). R-PLAT-002 is (manual) and R-PLAT-008 is a budget row (tests/budgets B-085).
//
// Cross builds run on this arm64 host for linux-amd64; cross outputs run under qemu-user (pin
// "qemu-user", the same name tests/core uses). The amd64-host half of the 4-combination matrix
// of R-PLAT-005 is (manual) until an x86_64 runner exists (HARNESS.md §4.2).
package platforms
