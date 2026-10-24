package main

import "testing"

func TestNumHex(t *testing.T) {
	if got := numHex(42); got != "3432" {
		t.Fatalf("numHex(42) = %q", got)
	}
}
