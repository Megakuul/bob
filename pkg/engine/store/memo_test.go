package store

import (
	"context"
	"crypto/sha256"
	"slices"
	"testing"
)

// The validity memo (A310): answers in order and once per path, positive answers kept for the
// run, negative ones only until the store runs a command that may change it.
func TestValidityMemo(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := s.WithValidityMemo()
	if m.WithValidityMemo() != m {
		t.Errorf("WithValidityMemo of a memo store made a new memo")
	}
	nar := flatNAR([]byte("memo\n"))
	p := sourcePathForTest(t, sha256.Sum256(nar), "bob-memo-test")
	q := sourcePathForTest(t, sha256.Sum256(flatNAR([]byte("never\n"))), "bob-memo-never")

	inv, err := m.Invalid(ctx, []string{q, p, q})
	if err != nil || !slices.Equal(inv, []string{q, p}) {
		t.Fatalf("Invalid = %v, %v; want [q p] once each, in order", inv, err)
	}
	if answered, valid := m.known(q); !answered || valid {
		t.Errorf("q not remembered as invalid: %v %v", answered, valid)
	}
	if err := m.Import(ctx, []ExportItem{{Path: p, NAR: nar}}); err != nil {
		t.Fatal(err)
	}
	// The import made p valid (remembered) and ended the negative answer about q.
	if answered, valid := m.known(p); !answered || !valid {
		t.Errorf("p not remembered as valid after Import: %v %v", answered, valid)
	}
	if answered, _ := m.known(q); answered {
		t.Errorf("negative answer about q survived a store-changing command")
	}
	inv, err = m.Invalid(ctx, []string{p, q})
	if err != nil || !slices.Equal(inv, []string{q}) {
		t.Fatalf("Invalid after import = %v, %v", inv, err)
	}
	if _, err := m.Invalid(ctx, []string{"/tmp/x"}); err == nil {
		t.Errorf("a non-store path was accepted")
	}

	// Prefetch answers a later Invalid; AskedPaths lists every answered path.
	m2 := s.WithValidityMemo()
	m2.Prefetch(ctx, []string{p, q, "junk"})
	if inv, err := m2.Invalid(ctx, []string{p}); err != nil || len(inv) != 0 {
		t.Fatalf("Invalid after Prefetch = %v, %v", inv, err)
	}
	want := []string{p, q}
	slices.Sort(want)
	if got := m2.AskedPaths(); !slices.Equal(got, want) {
		t.Errorf("AskedPaths = %v, want %v", got, want)
	}
	// Without a memo nothing is remembered and nothing is prefetched.
	s.Prefetch(ctx, []string{p})
	if s.AskedPaths() != nil {
		t.Errorf("a store without memo remembers paths")
	}
}

func TestReadOnlyCommands(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"nix-store", []string{"--check-validity", "--print-invalid"}, true},
		{"nix", []string{"path-info", "--json"}, true},
		{"nix-store", []string{"--import"}, false},
		{"nix-store", []string{"--realise"}, false},
		{"nix", []string{"build"}, false},
		{"nix", nil, false},
	} {
		if got := readOnly(c.name, c.args); got != c.want {
			t.Errorf("readOnly(%s %v) = %v", c.name, c.args, got)
		}
	}
}
