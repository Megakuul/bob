package resolve

import (
	"context"
	"strings"
	"testing"
)

// TestLockedPreferred: with Options.Locked a re-resolution keeps every locked version that
// still satisfies its requirement (even a yanked one) and moves only what it must; without it
// the newest versions win (bob lock --update).
func TestLockedPreferred(t *testing.T) {
	reg := MemRegistry{}
	add := func(name, vers string, yanked bool, deps ...IndexDep) {
		e, err := NewEntry(&IndexEntry{Name: name, Vers: vers, Cksum: strings.Repeat("a", 64), Yanked: yanked, Deps: deps})
		if err != nil {
			t.Fatal(err)
		}
		reg[name] = append(reg[name], e)
	}
	def := true
	add("a", "1.0.0", false)
	add("a", "1.2.0", false)
	add("b", "2.0.1", true) // yanked after it was locked
	add("b", "2.0.5", false)
	add("c", "0.3.0", false, IndexDep{Name: "a", Req: "^1.1", DefaultFeatures: &def, Kind: "normal"})
	add("c", "0.3.4", false, IndexDep{Name: "a", Req: "^1.1", DefaultFeatures: &def, Kind: "normal"})
	dep := func(name, req string) Dep {
		r, err := ParseReq(req)
		if err != nil {
			t.Fatal(err)
		}
		return Dep{Name: name, Package: name, Req: r, Default: true}
	}
	member := func(deps ...Dep) []Member { return []Member{{Name: "app", Version: "0.0.0", Deps: deps}} }
	got := func(ms []Member, o Options) string {
		t.Helper()
		res, err := Resolve(context.Background(), reg, ms, o)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range res.Crates {
			out = append(out, e.Name+"@"+e.Vers)
		}
		return strings.Join(out, " ")
	}
	locked := []string{"a@1.0.0", "b@2.0.1"}
	// unchanged pins stay; the new crate c needs a ^1.1, so a moves (the only change it forces)
	if g := got(member(dep("a", "1"), dep("b", "2")), Options{Locked: locked}); g != "a@1.0.0 b@2.0.1" {
		t.Errorf("locked: %s", g)
	}
	if g := got(member(dep("a", "1"), dep("b", "2"), dep("c", "0.3")), Options{Locked: locked}); g != "a@1.2.0 b@2.0.1 c@0.3.4" {
		t.Errorf("locked + new crate: %s", g)
	}
	if g := got(member(dep("a", "1"), dep("b", "2")), Options{}); g != "a@1.2.0 b@2.0.5" {
		t.Errorf("update all: %s", g)
	}
	// a locked version outside the requirement is not used
	if g := got(member(dep("a", "1.1"), dep("b", "2")), Options{Locked: locked}); g != "a@1.2.0 b@2.0.1" {
		t.Errorf("requirement raised: %s", g)
	}
}
