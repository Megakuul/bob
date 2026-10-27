package bob

import "testing"

// TestToolchainFor is the C/C++ row of the SPEC R-FW-033 table (the packages: nixext).
func TestToolchainFor(t *testing.T) {
	for _, c := range []struct {
		host, target   Platform
		prefix, triple string
		emulator       string
	}{
		{LinuxARM64, LinuxARM64, "", "", ""},
		{LinuxAMD64, LinuxAMD64, "", "", ""},
		{LinuxARM64, LinuxAMD64, "x86_64-unknown-linux-gnu-", "x86_64-unknown-linux-gnu", "qemu-x86_64"},
		{LinuxAMD64, LinuxARM64, "aarch64-unknown-linux-gnu-", "aarch64-unknown-linux-gnu", "qemu-aarch64"},
	} {
		tc := ToolchainFor(c.host, c.target)
		if tc.Prefix != c.prefix || tc.Triple != c.triple ||
			tc.EmulatorCmd != c.emulator || tc.Cross() != (c.host != c.target) {
			t.Errorf("ToolchainFor(%s, %s) = %+v", c.host, c.target, tc)
		}
	}
	if tc := ToolchainFor(LinuxARM64, "linux-riscv64"); tc.Cross() || tc.Prefix != "" {
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
