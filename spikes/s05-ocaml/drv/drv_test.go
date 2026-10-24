package drv

import "testing"

type noRes struct{}

func (noRes) Modulo(string) ([32]byte, bool) { return [32]byte{}, false }

// Expected path reported by `nix derivation add` (Nix 2.34.8) for the same derivation.
func TestOutputPathMatchesNix(t *testing.T) {
	d := &Derivation{Name: "t1", System: "aarch64-linux",
		Builder:   "/nix/store/k56s4lq6d6rljdxg26a34iij3xdvwzz7-bash-5.3p9/bin/bash",
		Args:      []string{"-c", "echo hi > $out"},
		InputSrcs: []string{"/nix/store/k56s4lq6d6rljdxg26a34iij3xdvwzz7-bash-5.3p9"}}
	if err := d.Finalize(noRes{}); err != nil {
		t.Fatal(err)
	}
	if want := "/nix/store/7y48n6rfvka50jcx9iych1fh1hz5yp08-t1"; d.OutPath != want {
		t.Fatalf("out = %s, want %s", d.OutPath, want)
	}
}

// A real nixpkgs derivation must round-trip: our output path from parsing == path recorded in the .drv.
func TestModuloAgainstNixpkgsHello(t *testing.T) {
	const p = "/nix/store/8ba48lcw85q6cfa3bcfmn1ra724jilc2-hello-2.12.3.drv"
	c := NewModuloCache()
	raw, err := readFile(p)
	if err != nil {
		t.Skip("hello drv not in store")
	}
	d, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute output path: blank outputs and env out, hash modulo, makeStorePath.
	blank := *d
	blank.Outputs = [][4]string{{"out", "", "", ""}}
	blank.Env = nil
	for _, e := range d.Env {
		if e[0] == "out" {
			e[1] = ""
		}
		blank.Env = append(blank.Env, e)
	}
	repl := map[string]string{}
	for _, in := range d.InputDrvs {
		h, ok := c.Modulo(in.Path)
		if !ok {
			t.Fatalf("modulo %s", in.Path)
		}
		repl[in.Path] = hexs(h)
	}
	got := MakeStorePath("output:out", sha(blank.aterm(repl)), "hello-2.12.3")
	if got != d.Path("out") {
		t.Fatalf("got %s want %s (parsed %d drvs)", got, d.Path("out"), c.Parsed)
	}
	t.Logf("verified via %d parsed input drvs", c.Parsed)
}
