package resolve

import (
	"sort"
	"testing"
)

func TestReqMatches(t *testing.T) {
	tests := []struct {
		req  string
		yes  []string
		no   []string
		bad  bool
		desc string
	}{
		{req: "1", yes: []string{"1.0.0", "1.9.3"}, no: []string{"0.9.0", "2.0.0", "1.2.0-alpha"}},
		{req: "1.2", yes: []string{"1.2.0", "1.7.0"}, no: []string{"1.1.9", "2.0.0"}},
		{req: "^1.2.3", yes: []string{"1.2.3", "1.3.0"}, no: []string{"1.2.2", "2.0.0"}},
		{req: "0.13", yes: []string{"0.13.0", "0.13.9"}, no: []string{"0.14.0", "0.12.9"}},
		{req: "^0.2.3", yes: []string{"0.2.3", "0.2.9"}, no: []string{"0.3.0", "0.2.2"}},
		{req: "^0.0.3", yes: []string{"0.0.3"}, no: []string{"0.0.4", "0.0.2"}},
		{req: "^0.0", yes: []string{"0.0.0", "0.0.9"}, no: []string{"0.1.0"}},
		{req: "^0", yes: []string{"0.0.1", "0.9.0"}, no: []string{"1.0.0"}},
		{req: "~1.2.3", yes: []string{"1.2.3", "1.2.9"}, no: []string{"1.3.0", "1.2.2"}},
		{req: "~1.2", yes: []string{"1.2.0", "1.2.9"}, no: []string{"1.3.0"}},
		{req: "~1", yes: []string{"1.0.0", "1.9.0"}, no: []string{"2.0.0"}},
		{req: "=1.38.0", yes: []string{"1.38.0"}, no: []string{"1.38.1", "1.38.0-rc.1"}},
		{req: "=1.2", yes: []string{"1.2.0", "1.2.7"}, no: []string{"1.3.0"}},
		{req: ">=1.2, <1.5", yes: []string{"1.2.0", "1.4.9"}, no: []string{"1.5.0", "1.1.0", "1.4.0-beta"}},
		{req: ">1.2", yes: []string{"1.3.0"}, no: []string{"1.2.9"}},
		{req: ">1.2.3", yes: []string{"1.2.4"}, no: []string{"1.2.3"}},
		{req: "<=1.2", yes: []string{"1.2.9", "0.1.0"}, no: []string{"1.3.0"}},
		{req: "<1", yes: []string{"0.9.9"}, no: []string{"1.0.0"}},
		{req: "1.*", yes: []string{"1.0.0", "1.5.0"}, no: []string{"2.0.0", "1.0.0-a"}},
		{req: "1.2.*", yes: []string{"1.2.0", "1.2.5"}, no: []string{"1.3.0"}},
		{req: "*", yes: []string{"0.0.1", "9.0.0"}, no: []string{"1.0.0-alpha"}},
		{req: "^1.0.0-beta.1", yes: []string{"1.0.0-beta.1", "1.0.0-beta.2", "1.0.0", "1.2.0"}, no: []string{"1.0.0-alpha.9", "1.1.0-beta.1"}},
		{req: ">=1.0.0-rc.1", yes: []string{"1.0.0-rc.1", "1.0.0-rc.2", "2.0.0"}, no: []string{"2.0.0-rc.1"}},
		{req: "~1.2.3-beta", yes: []string{"1.2.3-beta", "1.2.3", "1.2.4"}, no: []string{"1.2.3-alpha"}},
		{req: "1.2.3+build", yes: []string{"1.2.3", "1.2.3+other"}},
		{req: "", bad: true}, {req: "x.1", bad: true}, {req: "1.2.3.4", bad: true}, {req: ">=1.*", bad: true},
		{req: "1.*.3", bad: true}, {req: "01.2", bad: true}, {req: "1.2-pre", bad: true}, {req: ">=", bad: true},
	}
	for _, tt := range tests {
		r, err := ParseReq(tt.req)
		if tt.bad {
			if err == nil {
				t.Errorf("ParseReq(%q) accepted", tt.req)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseReq(%q): %v", tt.req, err)
			continue
		}
		for _, s := range tt.yes {
			if !r.Matches(mustVersion(t, s)) {
				t.Errorf("%q should match %s", tt.req, s)
			}
		}
		for _, s := range tt.no {
			if r.Matches(mustVersion(t, s)) {
				t.Errorf("%q should not match %s", tt.req, s)
			}
		}
	}
}

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestVersionOrderAndCompat(t *testing.T) {
	// semver.org §11 example order, plus build metadata ordered last
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.0+a", "1.0.0+b", "1.0.1", "1.10.0", "2.0.0"}
	vs := make([]Version, len(order))
	for i, s := range order {
		vs[i] = mustVersion(t, s)
	}
	shuffled := append([]Version(nil), vs...)
	sort.Slice(shuffled, func(i, j int) bool { return shuffled[i].String() > shuffled[j].String() })
	sort.Slice(shuffled, func(i, j int) bool { return shuffled[i].Compare(shuffled[j]) < 0 })
	for i := range vs {
		if shuffled[i].String() != order[i] {
			t.Fatalf("order[%d] = %s, want %s", i, shuffled[i], order[i])
		}
	}
	for s, want := range map[string]string{"1.2.3": "1", "0.3.1": "0.3", "0.0.4": "0.0.4", "2.0.0-rc.1": "2", "2.1.2+zstd.1.5.7": "2"} {
		if got := mustVersion(t, s).Compat(); got != want {
			t.Errorf("Compat(%s) = %s, want %s", s, got, want)
		}
	}
	for _, bad := range []string{"1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "v1.2.3", "1.2.3+"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	if s := mustVersion(t, "2.1.2+zstd.1.5.7").String(); s != "2.1.2+zstd.1.5.7" {
		t.Errorf("String() = %s", s)
	}
}
