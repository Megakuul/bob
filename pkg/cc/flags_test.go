package cc

import (
	"strings"
	"testing"
)

// TestFlagProblem: flags that name paths outside the build's inputs, redirect outputs or load
// plugins are refused (R-CC-002a); every include or library path flag is refused, relative or
// absolute (R-CC-006: a package gets them only from Deps); ordinary flags pass.
func TestFlagProblem(t *testing.T) {
	for _, c := range []struct{ flag, before, want string }{
		{"-I/nix/store/x", "", `flag "-I/nix/store/x" is not allowed; use Deps to add include paths`},
		{"/usr/include", "-isystem", "use Deps to add include paths"},
		{"--sysroot=/opt/x", "", "use Deps to add include paths"},
		{"-L/usr/lib", "", "use Deps to add libraries"},
		{"-o", "", "bob names the outputs"},
		{"-ofoo", "", "bob names the outputs"},
		{"-fplugin=/x.so", "", "compiler plugins are not hermetic"},
		{"@flags.txt", "", "response files"},
		{"-Wl,-rpath,/nix/store/y", "", "it names a path outside the sources"},
		{"-Wall", "", ""},
		{"-Iinclude", "", "use Deps to add include paths"},
		{"-I../libs/mathx/include", "", `flag "-I../libs/mathx/include" is not allowed; use Deps to add include paths`},
		{"include", "-isystem", "use Deps to add include paths"},
		{"-iquote", "", "use Deps to add include paths"},
		{"-include", "", "use Deps to add include paths"},
		{"-lz", "", "use Deps to add libraries"},
		{"-l:libfoo.a", "", "use Deps to add libraries"},
		{"-Llib", "", "use Deps to add libraries"},
		{"-Wl,-L,../lib", "", "use Deps to add libraries"},
		{"-Bbin", "", "it names a path outside the sources"},
		{"-Wl,--as-needed", "", ""},
		{"-std=c17", "", ""},
		{"-DPATH=a/b", "", ""},
		{"-O3", "", ""},
		{"-fsanitize=address", "", ""},
	} {
		got := flagProblem(c.flag, c.before)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("flagProblem(%q after %q) = %q, want %q", c.flag, c.before, got, c.want)
		}
	}
}
