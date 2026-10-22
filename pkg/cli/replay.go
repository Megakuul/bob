//go:build !bobtest

package cli

// replayDir is the BOB_TEST_NIX_REPLAY test hook (A27). Release builds ignore it.
func replayDir(func(string) string) string { return "" }
