package nixext

import "testing"

func TestMainProgram(t *testing.T) {
	for attr, want := range map[string]string{"curl": "curl", "": "", "no-such-attribute-xyz": ""} {
		if got := mainProgram(attr); got != want {
			t.Errorf("mainProgram(%q) = %q, want %q", attr, got, want)
		}
	}
}
