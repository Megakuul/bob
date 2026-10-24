package bob

import "testing"

// TestToolchainFor is the C/C++ row of the SPEC R-FW-033 table.
func TestToolchainFor(t *testing.T) {
	for _, c := range []struct {
		host, target                 Platform
		cc, bintools, prefix, triple string
		emulator                     string
	}{
		{LinuxARM64, LinuxARM64, "gcc", "binutils", "", "", ""},
		{LinuxAMD64, LinuxAMD64, "gcc", "binutils", "", "", ""},
		{LinuxARM64, LinuxAMD64, "pkgsCross.gnu64.stdenv.cc", "pkgsCross.gnu64.stdenv.cc.bintools",
			"x86_64-unknown-linux-gnu-", "x86_64-unknown-linux-gnu", "qemu-x86_64"},
		{LinuxAMD64, LinuxARM64, "pkgsCross.aarch64-multiplatform.stdenv.cc", "pkgsCross.aarch64-multiplatform.stdenv.cc.bintools",
			"aarch64-unknown-linux-gnu-", "aarch64-unknown-linux-gnu", "qemu-aarch64"},
	} {
		tc := ToolchainFor(c.host, c.target)
		if tc.CC != c.cc || tc.Bintools != c.bintools || tc.Prefix != c.prefix || tc.Triple != c.triple ||
			tc.EmulatorCmd != c.emulator || tc.Cross() != (c.host != c.target) {
			t.Errorf("ToolchainFor(%s, %s) = %+v", c.host, c.target, tc)
		}
		if tc.Cross() && (tc.CCLib != c.cc+".cc" || tc.Emulator != "qemu-user") {
			t.Errorf("ToolchainFor(%s, %s): CCLib %q, Emulator %q", c.host, c.target, tc.CCLib, tc.Emulator)
		}
	}
	if tc := ToolchainFor(LinuxARM64, "linux-riscv64"); tc.Cross() || tc.CC != "gcc" {
		t.Errorf("unknown target: %+v", tc)
	}
	for p, want := range map[Platform][3]string{
		LinuxAMD64: {"x86_64-linux", "amd64", "x86_64-unknown-linux-gnu"},
		LinuxARM64: {"aarch64-linux", "arm64", "aarch64-unknown-linux-gnu"},
		"x":        {"", "", ""},
	} {
		if got := [3]string{p.NixSystem(), p.GOARCH(), p.Triple()}; got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}
