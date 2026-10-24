package bop

import "testing"

func TestGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/nix/store/*-glibc-*/share/i18n/**", "/nix/store/abc-glibc-2.42/share/i18n/locales/en_US", true},
		{"/nix/store/*-glibc-*/share/i18n/**", "/nix/store/abc-glibc-2.42/share/i18n", false},
		{"/nix/store/*-gcc-*-lib/lib/lib?san.so*", "/nix/store/abc-gcc-15.3.0-lib/lib/libasan.so.8", true},
		{"/nix/store/*-gcc-*-lib/lib/lib?san.so*", "/nix/store/abc-gcc-15.3.0-lib/lib/libhwasan.so", false},
		{"/nix/store/*-openssl-*/lib/libssl.so*", "/nix/store/abc-openssl-3.6.5/lib/libssl.so.3", true},
		{"/nix/store/*-openssl-*/lib/libssl.so*", "/nix/store/abc-openssl-3.6.5/lib/x/libssl.so.3", false},
		{"/nix/store/*/lib/lib[cd].so", "/nix/store/abc-x/lib/libd.so", true},
	}
	for _, c := range cases {
		re, err := globRE(c.glob)
		if err != nil {
			t.Fatalf("%s: %v", c.glob, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("%s matches %s = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
	for _, bad := range []string{"lib/*.so", "/nix/store/[x"} {
		if _, err := globRE(bad); err == nil {
			t.Errorf("glob %q accepted", bad)
		}
	}
}
