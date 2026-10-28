package main

import "testing"

func TestDropGoProgress(t *testing.T) {
	in := "go: downloading github.com/a/b v1.0.0\nreal line\ngo: finding module for package x\ngo: cannot find main module\n"
	want := "real line\ngo: cannot find main module\n"
	if got := dropGoProgress(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := dropGoProgress("plain\n"); got != "plain\n" {
		t.Errorf("plain output changed: %q", got)
	}
}
