package translate

import (
	"strings"
	"testing"
)

// TestTestFailureExcerpt (A458): bob-builder's `bob:: {"type":"test-result",…}` line never
// reaches the excerpt; the output of the failed cases it carries (the t.Fatalf message) does,
// right after the "--- FAIL:" line.
func TestTestFailureExcerpt(t *testing.T) {
	tr := New(&Context{})
	lines := []string{
		"--- FAIL: TestGreeting (0.00s)",
		"FAIL",
		`bob:: {"type":"test-result","result":{"version":1,"label":"//cmd/hello:go_test","passed":false,"cases":[{"name":"TestGreeting","result":"fail","output":"    main_test.go:7: got \"hello, bob\"\n"},{"name":"TestOther","result":"pass","output":"noise\n"}]}}`,
	}
	e := tr.testFailure("/nix/store/x-go-test.drv", lines)
	if e == nil {
		t.Fatal("no test failure found")
	}
	got := strings.Join(e.Excerpt, "\n")
	want := "--- FAIL: TestGreeting (0.00s)\n    main_test.go:7: got \"hello, bob\"\nFAIL"
	if got != want {
		t.Errorf("excerpt:\n%s\nwant:\n%s", got, want)
	}
}
