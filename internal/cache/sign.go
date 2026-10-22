package cache

import "github.com/megakuul/bob/pkg/bob/diag"

// PathInfo is what a narinfo signature covers (SPEC §6.6.4 step 3e).
type PathInfo struct {
	StorePath  string   // /nix/store/<hash>-<name>
	NarHash    string   // sha256:<nix32>
	NarSize    uint64   // bytes
	References []string // full store paths
}

// Sign returns "<keyname>:<base64>" over the fingerprint
// `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>` with the Nix secret key
// "<keyname>:<base64 ed25519 key>" (the output of `nix key generate-secret`).
func Sign(secretKey string, info PathInfo) (string, error) {
	return "", diag.NotImplemented("cache.Sign")
}
