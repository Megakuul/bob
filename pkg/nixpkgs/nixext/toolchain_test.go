package nixext

import (
	"testing"

	"github.com/megakuul/bob/pkg/bob"
)

// TestToolchainFor is the package half of the SPEC R-FW-033 C/C++ table (bob.ToolchainFor has
// the triples and the emulator command).
func TestToolchainFor(t *testing.T) {
	for _, c := range []struct {
		host, target bob.Platform
		cc, bintools string
	}{
		{bob.LinuxARM64, bob.LinuxARM64, "gcc", "binutils"},
		{bob.LinuxAMD64, bob.LinuxAMD64, "gcc", "binutils"},
		{bob.LinuxARM64, bob.LinuxAMD64, "pkgsCross.gnu64.stdenv.cc", "pkgsCross.gnu64.stdenv.cc.bintools"},
		{bob.LinuxAMD64, bob.LinuxARM64, "pkgsCross.aarch64-multiplatform.stdenv.cc", "pkgsCross.aarch64-multiplatform.stdenv.cc.bintools"},
	} {
		tc := ToolchainFor(c.host, c.target)
		if tc.CC.Name() != c.cc || tc.Bintools.Name() != c.bintools {
			t.Errorf("ToolchainFor(%s, %s) = %+v", c.host, c.target, tc)
		}
		cross := c.host != c.target
		if cross && (tc.CCLib.Name() != c.cc+".cc" || tc.Emulator.Name() != "qemu-user") {
			t.Errorf("ToolchainFor(%s, %s): CCLib %q, Emulator %q", c.host, c.target, tc.CCLib.Name(), tc.Emulator.Name())
		}
		if !cross && (tc.CCLib != (Attr{}) || tc.Emulator != (Attr{})) {
			t.Errorf("ToolchainFor(%s, %s): native CCLib/Emulator set: %+v", c.host, c.target, tc)
		}
	}
	if tc := ToolchainFor(bob.LinuxARM64, "linux-riscv64"); tc.CC.Name() != "gcc" || tc.Emulator != (Attr{}) {
		t.Errorf("unknown target: %+v", tc)
	}
}
