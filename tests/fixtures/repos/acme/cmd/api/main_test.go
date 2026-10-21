package main

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	l := line()
	if !strings.Contains(l, "numhex=3432") {
		t.Fatalf("line() = %q", l)
	}
}
