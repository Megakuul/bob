package store

// NixVersion is the version of the Nix that scripts/fetch-nix.sh embeds (nixpkgs nixStatic at
// the pin; run.start "nix", bob version). TestNixVersion checks it against the embedded binary.
// A bob built with -tags nixsystem (the flake build, ADR 0002a) runs the Nix of its closure
// instead, which the flake pins to the same version; Open refuses one outside the 2.34 series
// (nixsystem.go).
const NixVersion = "2.34.9"

// nixLinks are the legacy command names; Nix dispatches on argv[0], so they are symlinks to nix
// (rootless-results "Embedding").
var nixLinks = []string{"nix-store", "nix-build", "nix-instantiate", "nix-env", "nix-hash", "nix-daemon"}
