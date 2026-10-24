package greet_test

import (
	"testing"

	"example.com/acme/internal/greet"
)

func TestHello(t *testing.T) {
	if greet.Hello("x") != "hello x" {
		t.Fatal("bad")
	}
}
