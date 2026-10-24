package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestGoName is the naming table of ADR 0020 §2.
func TestGoName(t *testing.T) {
	for attr, want := range map[string]string{
		"openssl":                         "Openssl",
		"go_1_26":                         "Go1_26",
		"nix-update":                      "NixUpdate",
		"python313":                       "Python313",
		"3proxy":                          "N3proxy",
		"postgresql_17":                   "Postgresql17",
		"rust_1_97.packages.stable.rustc": "Rust1_97PackagesStableRustc",
		"protoc-gen-go":                   "ProtocGenGo",
		"xorg.libX11":                     "XorgLibX11",
		"ghc'":                            "GhcPrime",
		"_1password-gui":                  "N1passwordGui",
		"libsigcxx12":                     "Libsigcxx12",
		"dvd+rw-tools":                    "DvdPlusrwTools",
	} {
		if got := GoName(attr); got != want {
			t.Errorf("GoName(%q) = %q, want %q", attr, got, want)
		}
	}
}

// TestAssignNames: collision groups and reserved names take the A_<attr> fallback; a fallback
// that still collides gets the fnv suffix.
func TestAssignNames(t *testing.T) {
	names, groups := assignNames([]string{"foo-bar", "foo_bar", "fooBar", "openssl", "lib", "tool", "a.b", "a_b"})
	want := map[string]string{
		"foo-bar": "A_foo_bar_", "foo_bar": "A_foo_bar_", "fooBar": "A_fooBar", "openssl": "Openssl",
		"lib": "A_lib", "tool": "A_tool", "a.b": "A_a_b_", "a_b": "A_a_b_",
	}
	for a, w := range want {
		got := names[a]
		if strings.HasSuffix(w, "_") {
			if !strings.HasPrefix(got, w) || len(got) != len(w)+8 {
				t.Errorf("%s: %q, want %s<8 hex>", a, got, w)
			}
			continue
		}
		if got != w {
			t.Errorf("%s: %q, want %q", a, got, w)
		}
	}
	if names["foo-bar"] == names["foo_bar"] || names["a.b"] == names["a_b"] {
		t.Errorf("fallback names still collide: %v", names)
	}
	wantGroups := map[string][]string{"FooBar": {"foo-bar", "fooBar", "foo_bar"}, "Lib": {"lib"}, "Tool": {"tool"}, "AB": {"a.b", "a_b"}}
	if !reflect.DeepEqual(groups, wantGroups) {
		t.Errorf("groups = %v, want %v", groups, wantGroups)
	}
}

func TestDocLines(t *testing.T) {
	h := handle{name: "Openssl", attr: "openssl", info: Info{Version: "3.6.1",
		Description: "A cryptographic library that implements the SSL and TLS protocols.\n", Licenses: []string{"Apache-2.0"}}}
	if got := docLines(h); !reflect.DeepEqual(got, []string{"Openssl is openssl 3.6.1: A cryptographic library that implements the SSL and TLS protocols. Licence: Apache-2.0."}) {
		t.Errorf("docLines = %q", got)
	}
	h = handle{name: "X", attr: "x", info: Info{Unfree: true, Platforms: []string{"aarch64-linux"}, Description: strings.Repeat("word ", 50)}}
	got := docLines(h)
	if len(got) != 3 || !strings.HasPrefix(got[0], "X is x unversioned: word word") || !strings.HasSuffix(got[0], "…. Licence: unknown.") ||
		got[1] != "Unfree: needs nixpkgs.AllowUnfree()." || got[2] != "Only on aarch64-linux." {
		t.Errorf("docLines = %q", got)
	}
	if len(got[0]) > 260 {
		t.Errorf("description not cut: %d bytes", len(got[0]))
	}
	if got := docLines(handle{name: "Y", attr: "y", info: Info{Version: "1"}}); got[0] != "Y is y 1: y 1. Licence: unknown." {
		t.Errorf("no description: %q", got[0])
	}
}
