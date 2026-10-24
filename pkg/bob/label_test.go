package bob

import (
	"errors"
	"strings"
	"testing"
)

func TestParseForms(t *testing.T) {
	cases := []struct {
		in   string
		want string // %T of the result, "" = error
	}{
		{"//...", "bob.Pattern"},
		{"//a/...", "bob.Pattern"},
		{"//a:all", "bob.Pattern"},
		{"//:all", "bob.Pattern"},
		{"//a/b", "bob.Pattern"},
		{"//a:b", "bob.Label"},
		{"nixpkgs:zlib", "bob.Label"},
		{"example.com/x//a:b", "bob.Label"},
		{"//a:b@linux-amd64+debug+asan", "bob.Configured"},
		{"//a:b@+fast", "bob.Configured"},
		{"///...", ""},
		{"//a:b@linux-x86", ""},
		{"//a:b@+", ""},
		{"//a:b@+debug+release", ""},
		{"examplecom//a:b", ""},
		{"//a:all@linux-amd64", ""},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		switch {
		case c.want == "" && err == nil:
			t.Errorf("Parse(%q) = %#v, want an error", c.in, got)
		case c.want == "" && !errors.Is(err, ErrSyntax):
			t.Errorf("Parse(%q) error %v does not wrap ErrSyntax", c.in, err)
		case c.want != "" && err != nil:
			t.Errorf("Parse(%q): %v", c.in, err)
		case c.want != "" && typeName(got) != c.want:
			t.Errorf("Parse(%q) = %T, want %s", c.in, got, c.want)
		}
	}
	if _, err := ParseLabel("//a:all"); err == nil {
		t.Error(`ParseLabel accepted the pattern "//a:all"`)
	}
}

func typeName(v any) string {
	switch v.(type) {
	case Label:
		return "bob.Label"
	case Pattern:
		return "bob.Pattern"
	case Configured:
		return "bob.Configured"
	}
	return "?"
}

func TestPatternMatch(t *testing.T) {
	foo := Label{Dir: "libs/foo", Name: "foo"}
	cases := []struct {
		p    Pattern
		l    Label
		want bool
	}{
		{"//...", foo, true},
		{"//...", Label{Nix: "zlib"}, false},
		{"//...", Label{Module: "example.com/x", Name: "a"}, false},
		{"//libs/...", foo, true},
		{"//libs/...", Label{Dir: "libsx", Name: "a"}, false},
		{"//libs/foo/...", foo, true},
		{"//libs:all", foo, false},
		{"//libs/foo:all", foo, true},
		{"//libs/foo", foo, true},
		{"//libs/foo:foo", foo, true},
		{"//libs/foo:bar", foo, false},
		{"nixpkgs:zlib", Label{Nix: "zlib"}, true},
		{"//bad pattern", foo, false},
	}
	for _, c := range cases {
		if got := c.p.Match(c.l); got != c.want {
			t.Errorf("%q.Match(%s) = %v, want %v", c.p, c.l, got, c.want)
		}
	}
}

func TestParseConfiguredKnownTokens(t *testing.T) {
	c, err := ParseConfigured("//a:b@+fast+race", LinuxAMD64, "fast")
	if err != nil || c.String() != "//a:b@linux-amd64+race+fast" {
		t.Errorf("ParseConfigured = %v, %v", c, err)
	}
	_, err = ParseConfigured("//a:b@+slow", LinuxAMD64, "fast")
	if err == nil || !strings.Contains(err.Error(), `unknown variant "slow" (known: release, debug, asan, ubsan, race, fast)`) {
		t.Errorf("unknown variant error: %v", err)
	}
}

func TestPositionString(t *testing.T) {
	if s := (Position{File: "libs/foo/bob.go", Line: 12}).String(); s != "libs/foo/bob.go:12" {
		t.Errorf("Position.String() = %q", s)
	}
	if s := (Position{}).String(); s != "" {
		t.Errorf("zero Position.String() = %q", s)
	}
}

// A named variant may shadow a built-in token (the polyglot fixture names one "asan").
func TestNamedVariantShadowsToken(t *testing.T) {
	c, err := ParseConfigured("//a:b@+asan", LinuxARM64, "asan")
	if err != nil || len(c.Variant.Sanitize) != 0 || len(c.Variant.Named) != 1 || c.String() != "//a:b@linux-arm64+asan" {
		t.Errorf("ParseConfigured = %+v, %v", c, err)
	}
}
