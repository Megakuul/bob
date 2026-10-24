package bob

import "testing"

// R-FW-016 / R-CLI-011: the column of a constructor call (bob graph shows file:line:col).
func TestCallColumn(t *testing.T) {
	for _, c := range []struct {
		line, fn string
		want     int
	}{
		{"\treturn cc.SharedLibrary(p, \"foo\", cc.LibraryOpts{", "github.com/megakuul/bob/pkg/cc.SharedLibrary", 9},
		{"\t\tDeps: []cc.Linkable{nixpkgs.Lib(p, \"openssl\")},", "github.com/megakuul/bob/pkg/nixpkgs.Lib", 23},
		{"var X = bob.Define(func(p *bob.P) *T { return step(p).File(\"v.txt\") })", "github.com/megakuul/bob/pkg/bob.(*Step).File", 55},
		{"\treturn golang.Binary[T](p, opts)", "example.com/x/golang.Binary[...]", 9},
		{"\tx := NotLib(p); y := Lib (p)", "a/b.Lib", 23},
		{"\tx := helper(p)", "a/b.Lib", 0},
		{"\treturn mk(p)", "a/b.mk.func1", 9},
		{"\treturn mk(p)", "", 0},
	} {
		if got := callColumn(c.line, funcName(c.fn), ""); got != c.want {
			t.Errorf("callColumn(%q, %s) = %d, want %d", c.line, c.fn, got, c.want)
		}
	}
	// Two calls on one line: the one naming the package.
	line := `		Tools:   []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")},`
	for name, want := range map[string]int{"bash": 23, "coreutils": 48, "": 23, "zsh": 23} {
		if got := callColumn(line, "Tool", name); got != want {
			t.Errorf("callColumn(%q) = %d, want %d", name, got, want)
		}
	}
}
