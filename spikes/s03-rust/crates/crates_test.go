package crates

import (
	"testing"
)

func TestReq(t *testing.T) {
	cases := []struct {
		req, v string
		ok     bool
	}{
		{"1.2.3", "1.9.0", true}, {"1.2.3", "2.0.0", false}, {"0.3", "0.3.9", true}, {"0.3", "0.4.0", false},
		{"0.0.4", "0.0.5", false}, {"~1.2", "1.2.9", true}, {"~1.2", "1.3.0", false}, {">=1.0, <1.5", "1.4.9", true},
		{"=1.0.5", "1.0.6", false}, {"1.*", "1.7.0", true}, {"*", "3.0.0", true}, {"1.0.0", "1.1.0-alpha", false},
		{"1.1.0-alpha", "1.1.0-beta", true}, {">1.2", "1.2.9", false}, {"<=1.2", "1.2.9", true},
	}
	for _, c := range cases {
		r, err := ParseReq(c.req)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := ParseVersion(c.v)
		if r.Matches(v) != c.ok {
			t.Errorf("%s matches %s = %v, want %v", c.req, c.v, !c.ok, c.ok)
		}
	}
}

func TestCfg(t *testing.T) {
	p := Platforms["aarch64-unknown-linux-gnu"]
	for expr, want := range map[string]bool{
		`cfg(unix)`: true, `cfg(windows)`: false, `cfg(target_os = "linux")`: true,
		`cfg(all(unix, not(target_arch = "wasm32")))`: true, `cfg(any(target_os = "macos", target_env = "msvc"))`: false,
		`x86_64-pc-windows-msvc`: false, `aarch64-unknown-linux-gnu`: true, `cfg(target_abi = "")`: true,
		`cfg(tracing_unstable)`: false,
	} {
		got, err := p.MatchTarget(expr)
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", expr, got, err, want)
		}
	}
}

func TestFeatures(t *testing.T) {
	e := &IndexEntry{Features: map[string][]string{"default": {"std"}, "std": {"serde?/std"}, "json": {"dep:serde_json", "serde/derive"}},
		Deps: []IndexDep{{Name: "serde", Optional: true}, {Name: "serde_json", Optional: true}, {Name: "log", Optional: true}}}
	x := Expand(e, []string{"default"}, nil, false)
	if x.Deps["serde"] || len(x.DepFeats["serde"]) != 0 {
		t.Errorf("weak feature enabled serde: %+v", x)
	}
	x = Expand(e, []string{"default", "json"}, nil, false)
	if !x.Deps["serde"] || !x.Deps["serde_json"] || !x.Feats["serde"] {
		t.Errorf("json: %+v", x)
	}
	if !x.Feats["log"] == false || x.Deps["log"] {
		t.Errorf("log should be off: %+v", x)
	}
	x = Expand(e, []string{"default"}, nil, true) // lock mode: weak acts strong
	if !x.Deps["serde"] {
		t.Errorf("lock mode should lock serde")
	}
}

// TestCrossPlan: x86_64 target from an aarch64 host splits host and target units.
func TestCrossPlan(t *testing.T) {
	w, err := ReadWorkspace("../ws/demo/ws.json")
	if err != nil {
		t.Skip(err)
	}
	l, err := ReadLock("../ws/demo/bob.lock.json")
	if err != nil {
		t.Skip(err)
	}
	host, tgt := Platforms["aarch64-unknown-linux-gnu"], Platforms["x86_64-unknown-linux-gnu"]
	p, err := MakePlan(w, l, "demo", tgt, host, "rustc")
	if err != nil {
		t.Fatal(err)
	}
	nh, nt := 0, 0
	names := map[string]int{}
	for _, u := range p.Units {
		if u.Host {
			nh++
		} else {
			nt++
		}
		names[u.Name]++
		if u.ProcMacro() && !u.Host {
			t.Errorf("proc-macro %s planned for target", u.Name)
		}
	}
	both := 0
	for _, n := range names {
		if n > 1 {
			both++
		}
	}
	t.Logf("cross plan: %d units: %d host, %d target, %d crates built for both", len(p.Units), nh, nt, both)
}
