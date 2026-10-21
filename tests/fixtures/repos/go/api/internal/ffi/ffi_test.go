package ffi

import "testing"

func TestNumHex(t *testing.T) {
	if got := NumHex(42); got != "3432" {
		t.Fatalf("NumHex(42) = %q", got)
	}
}

func TestChecksum(t *testing.T) {
	if Checksum("a") == Checksum("b") {
		t.Fatal("checksum does not depend on input")
	}
}
