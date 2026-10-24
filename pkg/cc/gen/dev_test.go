package gen

import (
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/nixidx"
)

// TestDevShellHasCompiler (A459, SPEC §02.7.0): the C/C++ ecosystem adds GCC and the clang
// tools of the nixpkgs pin to every dev shell of a repository with C/C++ packages. Before, it
// did not implement engine.DevPreparer and a C/C++ dev shell had no compiler.
func TestDevShellHasCompiler(t *testing.T) {
	if _, ok := any(Ecosystem{}).(engine.DevPreparer); !ok {
		t.Fatal("the C/C++ ecosystem contributes nothing to dev shells (no PrepareDev)")
	}
	got := devPackages(map[string]nixidx.Entry{
		"gcc":         {Version: "15.3.0", DrvPath: "/nix/store/52dk32w642kazfdmrydn6fm99q5x44zs-gcc-wrapper-15.3.0.drv", Outputs: map[string]string{"out": "/nix/store/23jyj7s1b10awhkkilbfxsaq3g9i1gdi-gcc-wrapper-15.3.0", "man": "/nix/store/x-man"}},
		"clang-tools": {Version: "21.1.8", DrvPath: "/nix/store/0000000000000000000000000000000a-clang-tools-21.1.8.drv", Outputs: map[string]string{"out": "/nix/store/m4i9i259dl766rv65hrkxckvwcgxz44q-clang-tools-21.1.8"}},
	})
	if len(got) != 2 || got[0].Name != "gcc" || got[0].Path != "/nix/store/23jyj7s1b10awhkkilbfxsaq3g9i1gdi-gcc-wrapper-15.3.0" ||
		got[1].Name != "clang-tools" || got[1].Version != "21.1.8" {
		t.Errorf("dev packages %+v", got)
	}
	if got := devPackages(map[string]nixidx.Entry{"gcc": {Missing: true}}); len(got) != 0 {
		t.Errorf("a missing attribute gives a package: %+v", got)
	}
}
