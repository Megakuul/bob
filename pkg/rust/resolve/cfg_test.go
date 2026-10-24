package resolve

import "testing"

func TestPlatformMatches(t *testing.T) {
	arm, err := NewPlatform("aarch64-unknown-linux-gnu")
	if err != nil {
		t.Fatal(err)
	}
	x86, err := NewPlatform("x86_64-unknown-linux-gnu")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		target   string
		arm, x86 bool
	}{
		{"", true, true},
		{"cfg(unix)", true, true},
		{"cfg(windows)", false, false},
		{`cfg(target_os = "linux")`, true, true},
		{`cfg(target_arch="x86_64")`, false, true},
		{`cfg(target_feature = "neon")`, true, false},
		{`cfg(target_has_atomic = "128")`, true, false},
		{`cfg(all(unix, not(target_arch = "wasm32"), target_pointer_width = "64",))`, true, true},
		{`cfg(any(target_os = "macos", target_os = "ios"))`, false, false},
		{`cfg(not(any(windows, target_os = "hermit", target_os = "wasi")))`, true, true},
		{`cfg(all())`, true, true},
		{`cfg(any())`, false, false},
		{`cfg(feature = "x")`, false, false},
		{"x86_64-unknown-linux-gnu", false, true},
		{"x86_64-pc-windows-msvc", false, false},
	}
	for _, tt := range tests {
		for _, c := range []struct {
			p    *Platform
			want bool
		}{{arm, tt.arm}, {x86, tt.x86}} {
			got, err := c.p.Matches(tt.target)
			if err != nil || got != c.want {
				t.Errorf("%s on %s = %v, %v; want %v", tt.target, c.p.Triple, got, err, c.want)
			}
		}
	}
	for _, bad := range []string{"cfg(", "cfg(unix", "cfg(not(a, b))", `cfg(a = b)`, "cfg(unix) x", `cfg(a = "x)`, "cfg(1abc)", "cfg(a-b)"} {
		if _, err := arm.Matches(bad); err == nil {
			t.Errorf("Matches(%q) accepted", bad)
		}
	}
	if _, err := NewPlatform("riscv64gc-unknown-linux-gnu"); err == nil {
		t.Error("unknown triple accepted")
	}
}
