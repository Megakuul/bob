package resolve

import (
	"strings"
	"testing"
)

const sri0 = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// unifyGraph is a locked graph exercising roles, weak features, dev-dependencies and
// platform-specific dependencies.
func unifyGraph(t *testing.T) ([]Member, []Crate) {
	t.Helper()
	req := func(s string) Req {
		r, err := ParseReq(s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	app := Member{Name: "app", Version: "0.0.0",
		Features: map[string][]string{"extra": {"l/with-opt"}},
		Deps: []Dep{
			{Name: "l", Package: "l", Req: req("1"), Default: true},
			{Name: "pm", Package: "pm", Req: req("1"), Default: true},
			{Name: "bd", Package: "bd", Req: req("1"), Kind: "build", Default: true},
			{Name: "dd", Package: "dd", Req: req("1"), Kind: "dev", Default: true},
			{Name: "win", Package: "win", Req: req("1"), Target: "cfg(windows)", Default: true},
			{Name: "x86", Package: "x86only", Req: req("1"), Target: `cfg(target_arch = "x86_64")`, Default: true},
			{Name: "core", Package: "core", Local: true, Default: true, Features: []string{"fast"}},
		}}
	core := Member{Name: "core", Version: "0.0.0", Features: map[string][]string{"fast": {}, "slow": {}}}
	leaf := func(name string, feats ...string) Crate {
		c := Crate{Name: name, Version: "1.0.0", Checksum: sri0, Meta: CrateMeta{Edition: "2021"}}
		if len(feats) > 0 {
			c.Features = map[string][]string{}
			for _, f := range feats {
				c.Features[f] = nil
			}
		}
		return c
	}
	crates := []Crate{
		{Name: "bd", Version: "1.0.0", Checksum: sri0, Deps: []CrateDep{{Name: "shared", Crate: "shared@1.0.0", Features: []string{"b"}}}},
		{Name: "dd", Version: "1.0.0", Checksum: sri0, Deps: []CrateDep{{Name: "shared", Crate: "shared@1.0.0", Features: []string{"d"}}}},
		{Name: "l", Version: "1.0.0", Checksum: sri0,
			Deps: []CrateDep{
				{Name: "shared", Crate: "shared@1.0.0", Features: []string{"a"}},
				{Name: "opt", Crate: "opt@1.0.0", Optional: true},
				{Name: "sys", Crate: "sys@1.0.0", Target: "cfg(unix)"},
			},
			Features: map[string][]string{"default": {"weak"}, "weak": {"opt?/f"}, "with-opt": {"dep:opt"}}},
		leaf("opt", "f"),
		{Name: "pm", Version: "1.0.0", Checksum: sri0, Meta: CrateMeta{ProcMacro: true},
			Deps: []CrateDep{{Name: "shared", Crate: "shared@1.0.0", Features: []string{"c"}}}},
		leaf("shared", "a", "b", "c", "d"),
		leaf("sys"),
		leaf("win"),
		leaf("x86only"),
	}
	return []Member{app, core}, crates
}

func unitMap(units []Unit) map[string]string {
	m := map[string]string{}
	for _, u := range units {
		var deps []string
		for _, d := range u.Deps {
			deps = append(deps, d.Name+"->"+d.To.String())
		}
		m[u.Key.String()] = strings.Join(u.Features, ",") + " | " + strings.Join(deps, " ")
	}
	return m
}

func TestUnify(t *testing.T) {
	members, crates := unifyGraph(t)
	arm, _ := NewPlatform("aarch64-unknown-linux-gnu")
	x86, _ := NewPlatform("x86_64-unknown-linux-gnu")

	// native build of app, no tests
	units, err := Unify(members, crates, []Root{{Member: "app", Default: true}}, arm, arm)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"app@0.0.0 (local)":   " | l->l@1.0.0 pm->pm@1.0.0 [host] bd->bd@1.0.0 [host] core->core@0.0.0 (local)",
		"core@0.0.0 (local)":  "fast | ",
		"l@1.0.0":             "default,weak | shared->shared@1.0.0 sys->sys@1.0.0",
		"shared@1.0.0":        "a | ",
		"shared@1.0.0 [host]": "b,c | ",
		"pm@1.0.0 [host]":     " | shared->shared@1.0.0 [host]",
		"bd@1.0.0 [host]":     " | shared->shared@1.0.0 [host]",
		"sys@1.0.0":           " | ",
	}
	assertUnits(t, "native", unitMap(units), want)

	// cross build for x86_64 with tests and the "extra" feature: the weak feature applies now
	units, err = Unify(members, crates, []Root{{Member: "app", Default: true, Dev: true, Features: []string{"extra"}}}, arm, x86)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]string{
		"app@0.0.0 (local)":   "extra | l->l@1.0.0 pm->pm@1.0.0 [host] bd->bd@1.0.0 [host] dd->dd@1.0.0 x86->x86only@1.0.0 core->core@0.0.0 (local)",
		"core@0.0.0 (local)":  "fast | ",
		"l@1.0.0":             "default,weak,with-opt | shared->shared@1.0.0 opt->opt@1.0.0 sys->sys@1.0.0",
		"opt@1.0.0":           "f | ",
		"shared@1.0.0":        "a,d | ",
		"shared@1.0.0 [host]": "b,c | ",
		"pm@1.0.0 [host]":     " | shared->shared@1.0.0 [host]",
		"bd@1.0.0 [host]":     " | shared->shared@1.0.0 [host]",
		"dd@1.0.0":            " | shared->shared@1.0.0",
		"sys@1.0.0":           " | ",
		"x86only@1.0.0":       " | ",
	}
	assertUnits(t, "cross+dev", unitMap(units), want)

	if _, err := Unify(members, crates, []Root{{Member: "nope"}}, arm, arm); err == nil {
		t.Error("unknown root accepted")
	}
	if _, err := Unify(members, crates, []Root{{Member: "app", Features: []string{"bogus"}}}, arm, arm); err == nil {
		t.Error("unknown root feature accepted")
	}
	broken := append([]Crate(nil), crates...)
	broken[0].Deps = []CrateDep{{Name: "gone", Crate: "gone@1.0.0"}}
	if _, err := Unify(members, broken, []Root{{Member: "app"}}, arm, arm); err == nil {
		t.Error("dangling edge in bob.lock accepted")
	}
}

func assertUnits(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: unit %s missing", what, k)
		} else if g != w {
			t.Errorf("%s: unit %s = %q, want %q", what, k, g, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected unit %s = %q", what, k, got[k])
		}
	}
}
