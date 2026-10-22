package store

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// NixVersion names the embedded Nix: `nix --version` prints it.
func TestNixVersion(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, Config{Home: filepath.Join(t.TempDir(), "home")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	c := s.Command(ctx, "nix", "--version")
	c.Stdout = &out
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if f := strings.Fields(out.String()); len(f) == 0 || f[len(f)-1] != NixVersion {
		t.Errorf("nix --version = %q, NixVersion = %s", out.String(), NixVersion)
	}
}
