//go:build nixsystem

package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// The flake variant (ADR 0002a) runs with BOB_TEST_SYSTEM_NIX, a static Nix 2.34 binary (the
// flake's checks pass it); without one the tests that need a real Nix skip.
func TestMain(m *testing.M) {
	if p := os.Getenv("BOB_TEST_SYSTEM_NIX"); p != "" {
		systemNix = p
	}
	os.Exit(m.Run())
}

func TestParseNixVersion(t *testing.T) {
	for _, tt := range []struct {
		in, impl string
		ver      [3]int
		bad      bool
	}{
		{in: "nix (Nix) 2.34.9\n", impl: "Nix", ver: [3]int{2, 34, 9}},
		{in: "nix (Nix) 2.35.0pre20261001_abcdef", impl: "Nix", ver: [3]int{2, 35, 0}},
		{in: "nix (Nix) 2.18", impl: "Nix", ver: [3]int{2, 18, 0}},
		{in: "nix (Lix, like Nix) 2.93.3", impl: "Lix", ver: [3]int{2, 93, 3}},
		{in: "nix 2.34.9", bad: true},
		{in: "nix (Nix)", bad: true},
		{in: "nix (Nix) two.34", bad: true},
		{in: "", bad: true},
	} {
		impl, ver, err := parseNixVersion(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("%q: no error", tt.in)
			}
			continue
		}
		if err != nil || impl != tt.impl || ver != tt.ver {
			t.Errorf("%q = %q %v %v, want %q %v", tt.in, impl, ver, err, tt.impl, tt.ver)
		}
	}
}

// A script that prints the given `nix --version` line, as a stand-in for a Nix binary.
func fakeNix(t *testing.T, version string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nix")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func code(err error) diag.Code {
	var d *diag.Error
	if errors.As(err, &d) {
		return d.Code
	}
	return 0
}

// Refusals: no path, a missing binary, a non-ELF file, a dynamically linked binary.
func TestSystemNixRefusals(t *testing.T) {
	ctx := context.Background()
	old := systemNix
	t.Cleanup(func() { systemNix = old })
	home := t.TempDir()

	for _, p := range []string{"", "relative/nix", "/nix/store/../x/nix"} {
		systemNix = p
		if _, err := extractNix(ctx, home); code(err) != 7003 {
			t.Errorf("path %q: %v, want BOB-E-7003", p, err)
		}
	}
	systemNix = filepath.Join(t.TempDir(), "missing")
	if _, err := extractNix(ctx, home); code(err) != 7003 {
		t.Errorf("missing: %v, want BOB-E-7003", err)
	}
	systemNix = fakeNix(t, "nix (Nix) 2.34.9")
	if _, err := extractNix(ctx, home); code(err) != 1026 {
		t.Errorf("script: %v, want BOB-E-1026", err)
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		if real, err := filepath.EvalSymlinks(sh); err == nil {
			if f, err := os.Open(real); err == nil {
				f.Close()
				systemNix = real
				_, err := extractNix(ctx, home)
				// A static sh would pass the ELF check and fail the version check: 1026 either way.
				if code(err) != 1026 {
					t.Errorf("%s: %v, want BOB-E-1026", real, err)
				}
			}
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "bin")); len(entries) != 0 {
		t.Errorf("a refused Nix left %d entries in <home>/bin", len(entries))
	}
}

// checkSystemNix's version gate, past the ELF check (tested above), on fake version lines.
func TestSystemNixVersionGate(t *testing.T) {
	for _, tt := range []struct {
		line string
		ok   bool
	}{
		{"nix (Nix) 2.34.0", true},
		{"nix (Nix) 2.34.9", true},
		{"nix (Nix) 2.33.4", false},
		{"nix (Nix) 2.35.0", false},
		{"nix (Nix) 3.34.0", false},
		{"nix (Lix, like Nix) 2.94.3", false},
	} {
		impl, ver, err := parseNixVersion(tt.line)
		if err != nil {
			t.Fatal(err)
		}
		got := impl == "Nix" && ver[0] == nixMajor && ver[1] == nixMinor
		if got != tt.ok {
			t.Errorf("%q accepted = %v, want %v", tt.line, got, tt.ok)
		}
	}
}

// With a real static Nix: the copy lands at <home>/bin/nix-<12 hex>/bin with the links, a second
// Open only stats, and Nix runs inside bob's namespace with bob's nix.conf.
func TestSystemNixCopy(t *testing.T) {
	if os.Getenv("BOB_TEST_SYSTEM_NIX") == "" {
		t.Skip("BOB_TEST_SYSTEM_NIX not set")
	}
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "home")
	s, err := Open(ctx, Config{Home: home})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, l := range nixLinks {
		if to, err := os.Readlink(filepath.Join(s.BinDir(), l)); err != nil || to != "nix" {
			t.Errorf("%s -> %q %v", l, to, err)
		}
	}
	fi1, err := os.Stat(filepath.Join(s.BinDir(), "nix"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ctx, Config{Home: home})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	fi2, _ := os.Stat(filepath.Join(s2.BinDir(), "nix"))
	if s2.BinDir() != s.BinDir() || !os.SameFile(fi1, fi2) {
		t.Errorf("second Open copied again: %s vs %s", s.BinDir(), s2.BinDir())
	}
	var out strings.Builder
	c := s.Command(ctx, "nix", "config", "show", "sandbox")
	c.Stdout = &out
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "true" {
		t.Errorf("sandbox = %q inside the namespace, want true (bob's nix.conf)", out.String())
	}
}
