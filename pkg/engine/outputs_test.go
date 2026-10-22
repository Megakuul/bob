package engine

import (
	"testing"
)

func TestStoreName(t *testing.T) {
	for in, want := range map[string][2]string{
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-glibc-2.42-47":     {"glibc", "2.42-47"},
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nss-cacert-3.115":  {"nss-cacert", "3.115"},
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-gcc-15.3.0-lib":    {"gcc", "15.3.0-lib"},
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-go-link-hello":     {"go-link-hello", ""},
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-tzdata-2026c":      {"tzdata", "2026c"},
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-openssl-3.6.5-bin": {"openssl", "3.6.5-bin"},
	} {
		if n, v := StoreName(in); n != want[0] || v != want[1] {
			t.Errorf("StoreName(%s) = %q %q, want %q", in, n, v, want)
		}
	}
}

func TestPruneGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		match         bool
	}{
		{"/nix/store/*-openssl-*/lib/libssl.so*", "/nix/store/aaaa-openssl-3.6/lib/libssl.so.3", true},
		{"/nix/store/*-openssl-*/lib/libssl.so*", "/nix/store/aaaa-openssl-3.6/lib/x/libssl.so.3", false},
		{"/nix/store/*-glibc-*/share/i18n/**", "/nix/store/aaaa-glibc-2.42/share/i18n/locales/de_DE", true},
		{"/nix/store/*-gcc-*-lib/lib/lib?san.so*", "/nix/store/aaaa-gcc-15-lib/lib/libasan.so.8", true},
		{"/nix/store/*-gcc-*-lib/lib/lib?san.so*", "/nix/store/aaaa-gcc-15-lib/lib/libhwasan.so", false},
	}
	for _, c := range cases {
		re, err := PruneGlob(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if re.MatchString(c.path) != c.match {
			t.Errorf("%s ~ %s = %v, want %v", c.pattern, c.path, !c.match, c.match)
		}
	}
	if _, err := PruneGlob("lib/*.so"); err == nil {
		t.Errorf("a pattern outside /nix/store/ was accepted")
	}
}

func TestSPDXExpression(t *testing.T) {
	ls := []Licence{{Licenses: []string{"MIT"}}, {Licenses: []string{"unknown"}}, {Licenses: []string{"Apache-2.0", "MIT"}}}
	if got := SPDXExpression(ls); got != "Apache-2.0 AND MIT" {
		t.Errorf("got %q", got)
	}
	if got := SPDXExpression([]Licence{{Licenses: []string{"unknown"}}}); got != "NOASSERTION" {
		t.Errorf("got %q", got)
	}
}

func TestClosureOf(t *testing.T) {
	refs := map[string][]string{"r": {"a", "b"}, "a": {"c"}, "b": {"c"}, "c": nil, "x": nil}
	got := ClosureOf(refs, "r")
	if len(got) != 4 || got[0] != "a" || got[3] != "r" {
		t.Errorf("closure %v", got)
	}
}
