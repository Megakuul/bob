package cache

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// PathInfo is what a narinfo signature covers (SPEC §6.6.4 step 3e).
type PathInfo struct {
	StorePath  string   // /nix/store/<hash>-<name>
	NarHash    string   // sha256:<nix32>
	NarSize    uint64   // bytes
	References []string // full store paths
}

// Fingerprint is the string Nix signs:
// `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>`.
func (p PathInfo) Fingerprint() string {
	refs := slices.Clone(p.References)
	slices.Sort(refs)
	return "1;" + p.StorePath + ";" + p.NarHash + ";" + strconv.FormatUint(p.NarSize, 10) + ";" + strings.Join(refs, ",")
}

// SecretKey is a Nix signing key: "<name>:<base64 of the 64-byte ed25519 private key>", the
// format of `nix key generate-secret`.
type SecretKey struct {
	Name string
	Key  ed25519.PrivateKey
}

// validKeyName keeps key names to what Nix and the narinfo format can carry.
func validKeyName(n string) bool {
	return n != "" && len(n) <= 128 && !strings.ContainsAny(n, ": \t\r\n")
}

// GenerateKey returns a new key called name.
func GenerateKey(name string) (*SecretKey, error) {
	if !validKeyName(name) {
		return nil, fmt.Errorf("cache: invalid key name %q", name)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cache: generate key: %w", err)
	}
	return &SecretKey{Name: name, Key: priv}, nil
}

// KeyFromSeed rebuilds the key called name from its 32-byte ed25519 seed.
func KeyFromSeed(name string, seed []byte) (*SecretKey, error) {
	if !validKeyName(name) || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("cache: invalid key %q", name)
	}
	return &SecretKey{Name: name, Key: ed25519.NewKeyFromSeed(seed)}, nil
}

// ParseSecretKey reads the `nix key generate-secret` format.
func ParseSecretKey(s string) (*SecretKey, error) {
	name, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || !validKeyName(name) {
		return nil, errors.New("cache: secret key: want <name>:<base64>")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("cache: secret key: bad key material")
	}
	return &SecretKey{Name: name, Key: ed25519.PrivateKey(raw)}, nil
}

// Seed is the 32-byte seed of the key (what bob stores).
func (k *SecretKey) Seed() []byte { return k.Key.Seed() }

// Public is the public key in Nix's trusted-public-keys format "<name>:<base64>".
func (k *SecretKey) Public() string {
	return k.Name + ":" + base64.StdEncoding.EncodeToString(k.Key.Public().(ed25519.PublicKey))
}

// Sign returns the narinfo Sig value "<name>:<base64>" for info.
func (k *SecretKey) Sign(info PathInfo) string {
	return k.Name + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k.Key, []byte(info.Fingerprint())))
}

// Verify reports whether sig ("<name>:<base64>") is a valid signature of info by the public
// key pub ("<name>:<base64>", names must match).
func Verify(pub, sig string, info PathInfo) bool {
	pn, pb, ok1 := strings.Cut(pub, ":")
	sn, sb, ok2 := strings.Cut(sig, ":")
	if !ok1 || !ok2 || pn != sn {
		return false
	}
	pk, err1 := base64.StdEncoding.DecodeString(pb)
	sg, err2 := base64.StdEncoding.DecodeString(sb)
	if err1 != nil || err2 != nil || len(pk) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pk), []byte(info.Fingerprint()), sg)
}

// Sign returns "<keyname>:<base64>" over the fingerprint
// `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>` with the Nix secret key
// "<keyname>:<base64 ed25519 key>" (the output of `nix key generate-secret`).
func Sign(secretKey string, info PathInfo) (string, error) {
	k, err := ParseSecretKey(secretKey)
	if err != nil {
		return "", err
	}
	return k.Sign(info), nil
}
