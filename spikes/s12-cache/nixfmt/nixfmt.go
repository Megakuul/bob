// Package nixfmt implements the small pieces of the Nix binary cache format bob needs:
// Nix base32, narinfo parse/format, the signature fingerprint and ed25519 keys in Nix's
// "name:base64" format. Written from the format as observed (nix path-info, nix key
// generate-secret, narinfo files on cache.nixos.org); verified against `nix store verify`.
package nixfmt

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const StoreDir = "/nix/store"

const b32chars = "0123456789abcdfghijklmnpqrsvwxyz"

// Base32 encodes bytes the way Nix does (reverse order, 5 bits per char, no padding).
func Base32(h []byte) string {
	n := (len(h)*8-1)/5 + 1
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b := uint(i * 5)
		j, k := b/8, b%8
		c := uint(h[j]) >> k
		if int(j)+1 < len(h) {
			c |= uint(h[j+1]) << (8 - k)
		}
		out[n-1-i] = b32chars[c&0x1f]
	}
	return string(out)
}

// DecodeBase32 is the inverse of Base32.
func DecodeBase32(s string, size int) ([]byte, error) {
	if len(s) != (size*8-1)/5+1 {
		return nil, fmt.Errorf("nix base32: bad length %d for %d bytes", len(s), size)
	}
	out := make([]byte, size)
	for n := 0; n < len(s); n++ {
		c := s[len(s)-n-1]
		d := strings.IndexByte(b32chars, c)
		if d < 0 {
			return nil, fmt.Errorf("nix base32: bad char %q", c)
		}
		b := uint(n * 5)
		i, j := b/8, b%8
		out[i] |= byte(d << j)
		if int(i)+1 < size {
			out[i+1] |= byte(d >> (8 - j))
		} else if d>>(8-j) != 0 {
			return nil, errors.New("nix base32: non-zero high bits")
		}
	}
	return out, nil
}

var (
	reHash      = regexp.MustCompile(`^[0-9a-df-np-sv-z]{32}$`)
	reStoreBase = regexp.MustCompile(`^[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+\-._?=]{1,211}$`)
)

// ValidHash reports whether s is a 32-char store hash.
func ValidHash(s string) bool { return reHash.MatchString(s) }

// ValidStorePath checks /nix/store/<hash>-<name>.
func ValidStorePath(p string) bool {
	b, ok := strings.CutPrefix(p, StoreDir+"/")
	return ok && reStoreBase.MatchString(b) && b != "" && !strings.HasPrefix(b[33:], ".")
}

// HashPart returns the 32-char hash of a store path or base name.
func HashPart(p string) string {
	b := strings.TrimPrefix(p, StoreDir+"/")
	if len(b) < 32 {
		return ""
	}
	return b[:32]
}

// NarInfo is one .narinfo file.
type NarInfo struct {
	StorePath   string
	URL         string
	Compression string
	FileHash    string // "sha256:<nix32>"
	FileSize    uint64
	NarHash     string // "sha256:<nix32>"
	NarSize     uint64
	References  []string // full store paths, sorted
	Deriver     string   // base name or ""
	CA          string
	Sigs        []string
}

// Fingerprint is the string Nix signs: 1;<path>;<narHash>;<narSize>;<refs,comma-separated>.
func (n *NarInfo) Fingerprint() string {
	refs := append([]string(nil), n.References...)
	sort.Strings(refs)
	return fmt.Sprintf("1;%s;%s;%d;%s", n.StorePath, n.NarHash, n.NarSize, strings.Join(refs, ","))
}

// Format renders the narinfo text. Field order follows Nix's own writer.
func (n *NarInfo) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "StorePath: %s\n", n.StorePath)
	fmt.Fprintf(&b, "URL: %s\n", n.URL)
	fmt.Fprintf(&b, "Compression: %s\n", n.Compression)
	if n.FileHash != "" {
		fmt.Fprintf(&b, "FileHash: %s\n", n.FileHash)
		fmt.Fprintf(&b, "FileSize: %d\n", n.FileSize)
	}
	fmt.Fprintf(&b, "NarHash: %s\n", n.NarHash)
	fmt.Fprintf(&b, "NarSize: %d\n", n.NarSize)
	refs := make([]string, 0, len(n.References))
	for _, r := range n.References {
		refs = append(refs, strings.TrimPrefix(r, StoreDir+"/"))
	}
	sort.Strings(refs)
	fmt.Fprintf(&b, "References: %s\n", strings.Join(refs, " "))
	if n.Deriver != "" {
		fmt.Fprintf(&b, "Deriver: %s\n", n.Deriver)
	}
	for _, s := range n.Sigs {
		fmt.Fprintf(&b, "Sig: %s\n", s)
	}
	if n.CA != "" {
		fmt.Fprintf(&b, "CA: %s\n", n.CA)
	}
	return b.String()
}

// ParseNarInfo parses and validates a narinfo (hostile input: bounded, strict).
func ParseNarInfo(r io.Reader) (*NarInfo, error) {
	n := &NarInfo{}
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	seen := map[string]bool{}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			return nil, fmt.Errorf("narinfo: bad line %q", line)
		}
		if seen[k] && k != "Sig" {
			return nil, fmt.Errorf("narinfo: duplicate %s", k)
		}
		seen[k] = true
		var err error
		switch k {
		case "StorePath":
			n.StorePath = v
		case "URL":
			n.URL = v
		case "Compression":
			n.Compression = v
		case "FileHash":
			n.FileHash = v
		case "FileSize":
			n.FileSize, err = strconv.ParseUint(v, 10, 64)
		case "NarHash":
			n.NarHash = v
		case "NarSize":
			n.NarSize, err = strconv.ParseUint(v, 10, 64)
		case "References":
			for _, f := range strings.Fields(v) {
				n.References = append(n.References, StoreDir+"/"+f)
			}
		case "Deriver":
			n.Deriver = v
		case "CA":
			n.CA = v
		case "Sig":
			n.Sigs = append(n.Sigs, v)
		default: // unknown keys are ignored, like Nix does
		}
		if err != nil {
			return nil, fmt.Errorf("narinfo: %s: %w", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !ValidStorePath(n.StorePath) {
		return nil, fmt.Errorf("narinfo: bad StorePath %q", n.StorePath)
	}
	for _, r := range n.References {
		if !ValidStorePath(r) {
			return nil, fmt.Errorf("narinfo: bad reference %q", r)
		}
	}
	if !strings.HasPrefix(n.NarHash, "sha256:") || n.NarSize == 0 {
		return nil, errors.New("narinfo: missing NarHash/NarSize")
	}
	return n, nil
}

// SecretKey is a Nix signing key ("name:base64(64-byte ed25519 private key)").
type SecretKey struct {
	Name string
	Key  ed25519.PrivateKey
}

// PublicKey is "name:base64(32-byte ed25519 public key)".
type PublicKey struct {
	Name string
	Key  ed25519.PublicKey
}

func GenerateKey(name string) (*SecretKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SecretKey{Name: name, Key: priv}, nil
}

func ParseSecretKey(s string) (*SecretKey, error) {
	name, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || name == "" {
		return nil, errors.New("secret key: want name:base64")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("secret key: bad key material")
	}
	return &SecretKey{Name: name, Key: ed25519.PrivateKey(raw)}, nil
}

func ParsePublicKey(s string) (*PublicKey, error) {
	name, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || name == "" {
		return nil, errors.New("public key: want name:base64")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("public key: bad key material")
	}
	return &PublicKey{Name: name, Key: ed25519.PublicKey(raw)}, nil
}

func (k *SecretKey) String() string { return k.Name + ":" + base64.StdEncoding.EncodeToString(k.Key) }

func (k *SecretKey) Public() *PublicKey {
	return &PublicKey{Name: k.Name, Key: k.Key.Public().(ed25519.PublicKey)}
}

func (k *PublicKey) String() string { return k.Name + ":" + base64.StdEncoding.EncodeToString(k.Key) }

// Sign returns "name:base64(sig)" over the narinfo fingerprint.
func (k *SecretKey) Sign(n *NarInfo) string {
	sig := ed25519.Sign(k.Key, []byte(n.Fingerprint()))
	return k.Name + ":" + base64.StdEncoding.EncodeToString(sig)
}

// Verify reports whether any Sig on n is a valid signature by k.
func (k *PublicKey) Verify(n *NarInfo) bool {
	fp := []byte(n.Fingerprint())
	for _, s := range n.Sigs {
		name, b64, ok := strings.Cut(s, ":")
		if !ok || name != k.Name {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && ed25519.Verify(k.Key, fp, sig) {
			return true
		}
	}
	return false
}

// SRIToNix converts "sha256-<base64>" (nix path-info --json) to "sha256:<nix32>".
func SRIToNix(sri string) (string, error) {
	b64, ok := strings.CutPrefix(sri, "sha256-")
	if !ok {
		if strings.HasPrefix(sri, "sha256:") {
			return sri, nil
		}
		return "", fmt.Errorf("unsupported hash %q", sri)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("bad sri hash %q", sri)
	}
	return "sha256:" + Base32(raw), nil
}
