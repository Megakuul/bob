package isolation

import (
	"fmt"
	"strings"
)

// NixConf is the configuration of bob's embedded Nix inside a job container (SPEC R-FLEET-008
// "Nix as root inside the container"): Nix runs as root, every build as an auto-allocated
// non-root uid in Nix's sandbox (own network namespace), signatures required. substituters are
// bob's cache URLs, tried before cache.nixos.org; trustedKeys are their public keys.
func NixConf(substituters, trustedKeys []string) string {
	subs := append(append([]string(nil), substituters...), "https://cache.nixos.org")
	keys := append(append([]string(nil), trustedKeys...), "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=")
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, v) }
	line("experimental-features", "nix-command auto-allocate-uids cgroups")
	line("sandbox", "true")
	line("sandbox-fallback", "false")
	line("build-users-group", "")
	line("auto-allocate-uids", "true")
	line("use-cgroups", "true")
	line("allowed-users", "root")
	line("trusted-users", "root")
	line("substituters", strings.Join(subs, " "))
	line("trusted-public-keys", strings.Join(keys, " "))
	line("require-sigs", "true")
	return b.String()
}
