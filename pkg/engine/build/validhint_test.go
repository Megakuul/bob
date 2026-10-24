package build

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The validity hint is one file per repository and command; reading keeps only store paths and
// gives up on oversized files (A314).
func TestValidityHintFile(t *testing.T) {
	home := t.TempDir()
	a := hintPath(home, Options{Root: "/r"})
	if b := hintPath(home, Options{Root: "/r", Command: CmdBuild, Patterns: []string{"//x"}}); b != a {
		t.Errorf("patterns or the default command changed the hint file: %s vs %s", a, b)
	}
	if hintPath(home, Options{Root: "/r", Command: CmdGen}) == a || hintPath(home, Options{Root: "/s"}) == a {
		t.Errorf("another command or repository shares the hint file")
	}
	if !strings.HasPrefix(a, filepath.Join(home, "cache", "drv", "validity")+"/") {
		t.Errorf("hint file %s is not in the drv cache", a)
	}

	f := filepath.Join(home, "h")
	p1 := "/nix/store/0182jyxcdxs894k9qvxsqcnqa8saxlby-a"
	if err := os.WriteFile(f, []byte(p1+"\njunk\n  \n/etc/passwd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHint(f); !slices.Equal(got, []string{p1}) {
		t.Errorf("readHint = %v", got)
	}
	if got := readHint(filepath.Join(home, "missing")); got != nil {
		t.Errorf("missing hint = %v", got)
	}
	if err := os.WriteFile(f, []byte(strings.Repeat(p1+"\n", maxHintPaths+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHint(f); got != nil {
		t.Errorf("oversized hint gave %d paths", len(got))
	}
}
