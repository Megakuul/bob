//go:build !bobtest

package server

import "time"

// Clock returns the process clock. Release builds use the wall clock and ignore the test
// variables BOB_TEST_CLOCK_OFFSET and BOB_TEST_CLOCK_FILE.
func Clock(func(string) string) func() time.Time { return time.Now }
