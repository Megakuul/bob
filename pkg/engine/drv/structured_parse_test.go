package drv

import (
	"encoding/json"
	"testing"
)

// Parse decodes `__json` into Structured (I-M7a NEEDS 2), and Parse then ATerm stays the
// identity; a `__json` that is not in canonical form stays in Env.
func TestParseStructured(t *testing.T) {
	out := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cc-mathx"
	d := &Derivation{
		Name: "cc-mathx", System: "aarch64-linux", Builder: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bob-builder/bin/bob-builder",
		Args:    []string{"cc-compile-link"},
		Outputs: []Output{{Name: "out", Path: out}},
		Env:     map[string]string{"out": out},
		Structured: map[string]any{
			"name": "cc-mathx", "bobSpec": map[string]any{"argv": []any{"gcc", "-c"}, "jobs": json.Number("4")},
		},
	}
	a, err := d.ATerm()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Env["__json"]; ok {
		t.Errorf("__json stays in Env: %v", p.Env)
	}
	spec, ok := p.Structured["bobSpec"]
	if !ok {
		t.Fatalf("Structured has no bobSpec: %v", p.Structured)
	}
	if b, _ := json.Marshal(spec); string(b) != `{"argv":["gcc","-c"],"jobs":4}` {
		t.Errorf("bobSpec = %s", b)
	}
	if p.Name != "cc-mathx" {
		t.Errorf("Name = %q", p.Name)
	}
	rt, err := p.ATerm()
	if err != nil {
		t.Fatal(err)
	}
	if string(rt) != string(a) {
		t.Errorf("Parse then ATerm is not the identity:\n got %s\nwant %s", rt, a)
	}

	// Not canonical (a float, whitespace): kept as the variable, still the identity.
	for _, js := range []string{`{"name":"x","v":1.5}`, `{"name": "x"}`, `[1]`} {
		e := &Derivation{Name: "x", System: "aarch64-linux", Builder: "/bin/sh",
			Outputs: []Output{{Name: "out", Path: "/nix/store/cccccccccccccccccccccccccccccccc-x"}},
			Env:     map[string]string{"__json": js, "out": "/nix/store/cccccccccccccccccccccccccccccccc-x"}}
		a, err := e.ATerm()
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(a)
		if err != nil {
			t.Fatal(err)
		}
		if p.Structured != nil || p.Env["__json"] != js {
			t.Errorf("%s: Structured %v, Env %v", js, p.Structured, p.Env)
		}
		if rt, _ := p.ATerm(); string(rt) != string(a) {
			t.Errorf("%s: not the identity", js)
		}
	}
}
