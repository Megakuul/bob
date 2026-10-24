package greet_test

import (
	"testing"

	"example.com/acme/internal/greet"
)

func TestHello(t *testing.T) {
	if got := greet.Hello("x"); got != "hello x" {
		t.Fatalf("Hello(x) = %q", got)
	}
}
