package npm

import "testing"

func TestRanges(t *testing.T) {
	cases := []struct {
		r, v string
		ok   bool
	}{
		{"^1.2.3", "1.9.0", true}, {"^1.2.3", "2.0.0", false}, {"^0.2.3", "0.2.9", true}, {"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.4", false}, {"~1.2.3", "1.2.9", true}, {"~1.2.3", "1.3.0", false}, {"1.x", "1.5.0", true},
		{">=1.2.7 <1.3.0", "1.2.8", true}, {"1.2.3 - 2.3", "2.3.9", true}, {"1.2.3 - 2.3", "2.4.0", false},
		{"*", "1.0.0-beta", false}, {">1.2.3-alpha.3", "1.2.3-alpha.7", true}, {">1.2.3-alpha.3", "3.4.5-alpha.9", false},
		{"^1.2.3-beta.2", "1.2.3-beta.4", true}, {"^1.2.3-beta.2", "1.2.4-beta.2", false}, {"<2", "1.9.9", true},
		{">= 2.1.2 < 3", "2.5.0", true}, {"^18.0.0 || ^19.0.0", "19.1.0", true}, {"", "3.0.0", true}, {"~1", "1.9.0", true},
		{"<=1.2", "1.2.9", true}, {"<=1.2", "1.3.0", false}, {">1", "2.0.0", true}, {">1", "1.9.0", false}, {"=1.0.0", "1.0.0", true},
		{"^0.x", "0.5.0", true}, {"~0.0.1", "0.0.5", true}, {"^1.0.0-rc.1", "1.0.0", true},
	}
	for _, c := range cases {
		r, err := ParseRange(c.r)
		if err != nil {
			t.Fatalf("%s: %v", c.r, err)
		}
		v, _ := ParseVersion(c.v)
		if got := r.Match(v); got != c.ok {
			t.Errorf("%q matches %s = %v, want %v", c.r, c.v, got, c.ok)
		}
	}
}
