package main

import "testing"

func TestRedClassification(t *testing.T) {
	cases := []struct {
		name, out string
		red       bool
	}{
		{"harness red", "    x_test.go:12: red: not implemented yet: bob build -> error[BOB-E-9008]\n", true},
		{"ui red", "    ui_test.go:39: not implemented: / renders no data-page\n", true},
		{"mixed", "    x_test.go:12: red: not implemented yet: x\n    x_test.go:15: got 3, want 4\n", false},
		{"plain failure", "    x_test.go:9: wrong answer\n", false},
		{"no failure line", "--- FAIL: TestX (0.00s)\n", true}, // handled by the parent rule
	}
	for _, c := range cases {
		got := hasRedMarker(c.out) && onlyRedFailures(c.out)
		if c.name == "no failure line" {
			got = !hasOwnFailureLine(c.out)
		}
		if got != c.red {
			t.Errorf("%s: red = %v, want %v", c.name, got, c.red)
		}
	}
}

func TestAcceptanceDirs(t *testing.T) {
	for p, want := range map[string]bool{
		modulePath + "/tests/store":            true,
		modulePath + "/tests/eco/golang":       true,
		modulePath + "/tests/harness":          false,
		modulePath + "/tests/serverenv/rustfs": false,
		modulePath + "/pkg/bob":                false,
	} {
		if isAcceptance(p) != want {
			t.Errorf("isAcceptance(%s) = %v", p, !want)
		}
	}
}
