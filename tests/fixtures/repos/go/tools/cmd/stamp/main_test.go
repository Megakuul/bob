package main

import "testing"

func TestRender(t *testing.T) {
	if got := render(); got != "Built By Bob (42)" {
		t.Fatalf("render() = %q", got)
	}
}
