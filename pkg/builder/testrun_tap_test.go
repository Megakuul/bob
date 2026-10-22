package builder

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTapCases: TAP output gives one case per test line (SKIP and TODO directives, failing
// diagnostics); any other output is one case named after the unit.
func TestTapCases(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "test.log")
	tap := "TAP version 14\n1..4\nok 1 - gcd(12,18)\nnot ok 2 - gcd(-7,21)\n# got 0\nok 3 # SKIP no x\nnot ok 4 - later # TODO soon\n"
	if err := os.WriteFile(log, []byte(tap), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := tapCases(log, "//libs/mathx:mathx_test", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, result, output string }{
		{"gcd(12,18)", "pass", ""}, {"gcd(-7,21)", "fail", "# got 0\n"}, {"3", "skip", ""}, {"later", "pass", ""},
	}
	if len(cases) != len(want) {
		t.Fatalf("cases %+v", cases)
	}
	for i, w := range want {
		if c := cases[i]; c.Name != w.name || c.Result != w.result || c.Output != w.output {
			t.Errorf("case %d = %+v, want %+v", i, c, w)
		}
	}
	if err := os.WriteFile(log, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err = tapCases(log, "//libs/mathx:mathx_test", false, 1)
	if err != nil || len(cases) != 1 || cases[0].Name != "mathx_test" || cases[0].Result != "fail" || cases[0].Output != "hello\n" {
		t.Errorf("plain output: %+v %v", cases, err)
	}
}
