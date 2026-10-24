package translate

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// TestRealiseFailure: the stderr of the evaluator's `nix-store --realise` of go, recorded by Q4
// on an emulated NixOS layout (the owner's first `bob lock`), is explained by its cause class
// instead of BOB-E-9004 "bob bug", without Nix text in the message or cause, with the raw text
// (including the cache warning from the start) for -v (A471).
func TestRealiseFailure(t *testing.T) {
	var drvs []string
	for range 267 {
		drvs = append(drvs, "  /nix/store/i422bd2d8b8n6hvjlw6c6k93l2i44g8j-go-1.26.8.drv")
	}
	owner := "disabling binary cache 'https://cache.nixos.org' for 60 seconds\n" +
		"warning: unable to download 'https://cache.nixos.org/nix-cache-info': Problem with the SSL CA cert (path? access rights?) (77) error adding trust anchors from file: /etc/ssl/certs/ca-certificates.crt\n" +
		"these 267 derivations will be built:\n" + strings.Join(drvs, "\n") + "\n" +
		"error:\n       … while setting up the build environment\n\n" +
		"       error: getting attributes of path \"/nix/store/q4q4q4q4q4q4q4q4q4q4q4q4q4q4q4q4-etc/etc/hosts\": No such file or directory\n"
	cases := []struct {
		name, stderr string
		code         diag.Code
		cause        string // substring of Cause
	}{
		{"owner: CA bundle dangling", owner, 4003, "/etc/ssl/certs/ca-certificates.crt cannot be read"},
		{"dns", "warning: unable to download 'https://cache.nixos.org/abc.narinfo': Could not resolve host: cache.nixos.org (6); retrying in 250 ms (attempt 1/5)\nerror: some error\n", 4003, "cannot be resolved"},
		{"sandbox hosts", "error:\n       … while setting up the build environment\n\n       error: getting attributes of path \"/nix/store/q4q4q4q4q4q4q4q4q4q4q4q4q4q4q4q4-etc/etc/hosts\": No such file or directory\n", 7002, "links into its own Nix store"},
		{"disk", "error: writing to file: No space left on device\n", 6001, ""},
		{"permission", "error: opening lock file '/nix/var/nix/db/big-lock': Permission denied\n", 7002, "no access to /nix/var/nix/db/big-lock"},
		{"upstream", "error: builder for '/nix/store/i422bd2d8b8n6hvjlw6c6k93l2i44g8j-go-1.26.8.drv' failed with exit code 2\n", 5001, ""},
		{"unknown", "error: something new\n", 9005, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := RealiseFailure("nixpkgs:go", c.stderr)
			if e.Code != c.code {
				t.Fatalf("code %d (%s), want %d", e.Code, e.Error(), c.code)
			}
			if c.cause != "" && !strings.Contains(e.Cause, c.cause) {
				t.Errorf("cause %q, want it to contain %q", e.Cause, c.cause)
			}
			for _, s := range []string{e.Error(), e.Cause, e.Hint()} {
				if leakRe().MatchString(s) || strings.Contains(s, "BOB-E-9004") {
					t.Errorf("Nix text in default output: %q", s)
				}
			}
			if len(e.Raw) == 0 || len(e.Raw) > 40 {
				t.Errorf("raw has %d lines, want 1..40", len(e.Raw))
			}
		})
	}
	e := RealiseFailure("nixpkgs:go", owner)
	if !strings.Contains(strings.Join(e.Raw, "\n"), "unable to download") || !strings.Contains(strings.Join(e.Raw, "\n"), "getting attributes") {
		t.Errorf("-v text lost the cache warning or the final error:\n%s", strings.Join(e.Raw, "\n"))
	}
}
