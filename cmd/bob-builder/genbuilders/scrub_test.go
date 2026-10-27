package main

import (
	"bytes"
	"testing"
)

func TestScrubStoreRefs(t *testing.T) {
	in := []byte("x/nix/store/ad9h5b41f7x4fvk22axfjvxykisyjc15-tzdata-2026e/share/zoneinfo/y" +
		"/nix/store/[0-9a-df-np-sv-z]{32}-(npm-)" + "/nix/store/short-name")
	got := scrubStoreRefs(in)
	want := []byte("x/nix/store/00000000000000000000000000000000-tzdata-2026e/share/zoneinfo/y" +
		"/nix/store/[0-9a-df-np-sv-z]{32}-(npm-)" + "/nix/store/short-name")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if len(got) != len(in) {
		t.Errorf("length changed: %d -> %d", len(in), len(got))
	}
}
