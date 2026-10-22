package resolve

import (
	"encoding/json"
	"os"
	"testing"
)

// node-semver.json was produced by node-semver 7 as bundled with npm 11.19.0 (nodejs 24.21.0,
// the oracle pin): Range formatting, satisfies(v, r, loose), valid(v, loose), the engines check
// (includePrerelease) and compare.
func TestSemverMatchesNodeSemver(t *testing.T) {
	b, err := os.ReadFile("testdata/node-semver.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Ranges     [][2]*string `json:"ranges"`
		Satisfies  [][]int      `json:"satisfies"`
		Valid      [][2]*string `json:"valid"`
		Versions   []string     `json:"versions"`
		RawEngines [][2]any     `json:"engines"`
		Cmp        struct {
			Vs  []string `json:"vs"`
			Cmp []int    `json:"cmp"`
		} `json:"cmp"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	for i, rr := range g.Ranges {
		raw := *rr[0]
		r, err := ParseRange(raw)
		switch {
		case rr[1] == nil && err == nil:
			t.Errorf("range %q: node-semver rejects it, bob parses %q", raw, r.String())
		case rr[1] != nil && err != nil:
			t.Errorf("range %q: node-semver gives %q, bob: %v", raw, *rr[1], err)
		case rr[1] != nil && r.String() != *rr[1]:
			t.Errorf("range %q: node-semver %q, bob %q", raw, *rr[1], r.String())
		}
		for j, v := range g.Versions {
			if got, want := Satisfies(v, raw), g.Satisfies[i][j] == 1; got != want {
				t.Errorf("satisfies(%q, %q) = %v, node-semver %v", v, raw, got, want)
			}
		}
	}
	for _, vv := range g.Valid {
		v, err := ParseVersion(*vv[0], true)
		switch {
		case vv[1] == nil && err == nil:
			t.Errorf("valid(%q): node-semver null, bob %q", *vv[0], v.String())
		case vv[1] != nil && (err != nil || v.String() != *vv[1]):
			t.Errorf("valid(%q): node-semver %q, bob %v %v", *vv[0], *vv[1], v, err)
		}
	}
	for _, e := range g.RawEngines {
		r, ok := e[0].(string), e[1].(bool)
		if got := satisfiesEngine("24.21.0", r); got != ok {
			t.Errorf("engines %q with node 24.21.0: bob %v, npm-install-checks %v", r, got, ok)
		}
	}
	if len(g.Ranges) < 90 || len(g.Versions) < 50 || len(g.RawEngines) < 9 {
		t.Fatalf("golden too small: %d ranges, %d versions, %d engines", len(g.Ranges), len(g.Versions), len(g.RawEngines))
	}
	n := len(g.Cmp.Vs)
	for i, a := range g.Cmp.Vs {
		for j, c := range g.Cmp.Vs {
			va, _ := ParseVersion(a, false)
			vc, _ := ParseVersion(c, false)
			if got, want := va.Compare(vc), g.Cmp.Cmp[i*n+j]; got != want {
				t.Errorf("compare(%s, %s) = %d, node-semver %d", a, c, got, want)
			}
		}
	}
}

// icu-en.json is Intl.Collator('en').compare of nodejs 24.21.0 over package names and paths.
func TestLocaleCompareMatchesICU(t *testing.T) {
	b, err := os.ReadFile("testdata/icu-en.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Words []string `json:"words"`
		Pairs []int    `json:"pairs"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	ascii := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return false
			}
		}
		return true
	}
	n := len(g.Words)
	for i, a := range g.Words {
		for j, c := range g.Words {
			if !ascii(a) || !ascii(c) {
				continue
			}
			if got, want := localeCompare(a, c), g.Pairs[i*n+j]; got != want {
				t.Errorf("localeCompare(%q, %q) = %d, ICU %d", a, c, got, want)
			}
		}
	}
}
