//go:build bobtest

package cli

// replayDir is the BOB_TEST_NIX_REPLAY test hook (A27, HARNESS.md §5.3): a recorded capture
// directory that bob build translates instead of evaluating and running Nix. Only binaries
// built with -tags bobtest honour it.
func replayDir(getenv func(string) string) string { return getenv("BOB_TEST_NIX_REPLAY") }
