package cli

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestRCLI005NoServer: with a fresh HOME (no credentials.json) the local commands exit 0 on the
// fixture; `bob build --remote` exits 4 with BOB-E-4009 ("no bob server configured").
func TestRCLI005NoServer(t *testing.T) {
	parity.Covers(t, "R-CLI-005")
	dir := harness.Fixture(t, "acme")
	env := home(t) // one fresh HOME for the whole test: no credentials.json, no config.json
	for _, args := range [][]string{
		{"build", "//..."},
		{"test", "//..."},
		{"dev", "--", "true"},
		{"check"},
		{"graph"},
	} {
		r := bob(t, dir, env, args...)
		if harness.Red(t, r) {
			continue
		}
		if r.Exit != 0 {
			t.Errorf("bob %s without a server: exit %d, want 0\n%s", strings.Join(args, " "), r.Exit, r.Combined())
		}
		noLeak(t, r)
	}
	r := bob(t, dir, env, "build", "--remote")
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 4 {
		t.Errorf("bob build --remote without a server: exit %d, want 4\n%s", r.Exit, r.Combined())
	}
	if !strings.Contains(r.Combined(), "BOB-E-4009") || !strings.Contains(r.Combined(), "no bob server configured") {
		t.Errorf("bob build --remote: want BOB-E-4009 `no bob server configured`:\n%s", r.Combined())
	}
	noLeak(t, r)
}
