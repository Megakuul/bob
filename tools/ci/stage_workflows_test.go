package main

import (
	"slices"
	"testing"
)

func TestUnpinnedActions(t *testing.T) {
	const wf = `jobs:
  a:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/cache@v4
      - uses: actions/cache@caa296126883cff596d87d8935842f9db880ef25
      - uses: ./local
      - name: x
        uses: cachix/install-nix-action@13d8dd58da0234aa297dedd986986ccb8e7f3e24 # v31.11.1
`
	got := unpinnedActions(wf)
	want := []string{
		"line 5: - uses: actions/cache@v4",
		"line 6: - uses: actions/cache@caa296126883cff596d87d8935842f9db880ef25",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
