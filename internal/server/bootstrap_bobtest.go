//go:build bobtest

package server

// bootstrapToken: -tags bobtest builds (tools/devstack, tests/serverenv/stack) honour
// BOB_ADMIN_TOKEN as the bootstrap admin credential of the test stacks (HARNESS §13.2).
const bootstrapToken = true
