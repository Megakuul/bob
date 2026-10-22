package clients_test

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/serverenv/clients"
)

func TestNixVersion(t *testing.T) {
	r := clients.Nix(t).Run("--version")
	if r.Exit != 0 || !strings.Contains(r.Stdout, "nix") {
		t.Fatalf("nix --version: exit %d: %s%s", r.Exit, r.Stdout, r.Stderr)
	}
}

func TestPodmanVersion(t *testing.T) {
	r := clients.Podman(t).Run("--version")
	if r.Exit != 0 || !strings.Contains(r.Stdout, "podman") {
		t.Fatalf("podman --version: exit %d: %s%s", r.Exit, r.Stdout, r.Stderr)
	}
}
