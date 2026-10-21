package strutil

import "testing"

func TestTitle(t *testing.T) {
	if got := Title("hello bob world"); got != "Hello Bob World" {
		t.Fatalf("Title = %q", got)
	}
}
