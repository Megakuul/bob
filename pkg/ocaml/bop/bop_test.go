package bop

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestScrub: references to build-only store paths lose their hash; the others stay.
func TestScrub(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prog")
	ocaml := "/nix/store/m8b3m1p90mpywd3r7is2scsnimmahz28-ocaml-5.4.1/lib/ocaml"
	glibc := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-glibc-2.42-50/lib"
	dev := "/nix/store/1123456789abcdfghijklmnpqrsvwxyz-glibc-2.42-50-dev/include"
	if err := os.WriteFile(p, []byte("\x7fELF\x00"+ocaml+"\x00"+glibc+"\x00"+dev+"\x00"), 0o555); err != nil {
		t.Fatal(err)
	}
	var res []*regexp.Regexp
	for _, s := range []string{`^ocaml-[0-9][0-9.]*$`, `^glibc-.*-dev$`} {
		res = append(res, regexp.MustCompile(s))
	}
	if err := scrubFile(p, "/nix/store", res); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	e32 := strings.Repeat("e", 32)
	if strings.Contains(s, "m8b3m1p90mpywd3r7is2scsnimmahz28") || !strings.Contains(s, e32+"-ocaml-5.4.1") {
		t.Errorf("ocaml reference not scrubbed: %q", s)
	}
	if !strings.Contains(s, glibc) {
		t.Errorf("glibc reference was scrubbed: %q", s)
	}
	if strings.Contains(s, "1123456789abcdfghijklmnpqrsvwxyz") {
		t.Errorf("glibc-dev reference not scrubbed: %q", s)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("program lost its mode: %v", fi.Mode())
	}
}

// TestShebang: scripts whose interpreter is missing in the sandbox run through the job's PATH.
func TestShebang(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ocaml"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configure"), []byte("#!/usr/bin/env ocaml\nlet () = ()\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &runner{path: []string{bin}}
	argv, err := r.resolve(dir, []string{"./configure", "--prefix", "/x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(bin, "ocaml"), filepath.Join(dir, "configure"), "--prefix", "/x"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("got %q, want %q", argv, want)
	}
}

// TestMeta: the META of a local library names its requirements and archive.
func TestMeta(t *testing.T) {
	got := meta("geom", []string{"fmt", "lwt.unix"})
	for _, w := range []string{`requires = "fmt lwt.unix"`, `archive(native) = "geom.cmxa"`} {
		if !strings.Contains(got, w) {
			t.Errorf("META lacks %s:\n%s", w, got)
		}
	}
}
