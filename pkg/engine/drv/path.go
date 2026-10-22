package drv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// StoreDir is the logical store directory of every path bob computes. bob's store is rootless
// but keeps the standard store directory so paths substitute from standard caches (R-NIX-003).
const StoreDir = "/nix/store"

// maxNameLen is Nix's limit on the name part of a store path.
const maxNameLen = 211

// nixBase32Alphabet is Nix's base-32 alphabet (no e, o, t, u).
const nixBase32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

// base32 encodes b the way Nix prints hashes in store paths: least significant 5-bit group last.
func base32(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	n := (len(b)*8-1)/5 + 1
	out := make([]byte, 0, n)
	for i := n - 1; i >= 0; i-- {
		bit := i * 5
		j, k := bit/8, uint(bit%8)
		c := b[j] >> k
		if j+1 < len(b) {
			c |= b[j+1] << (8 - k)
		}
		out = append(out, nixBase32Alphabet[c&0x1f])
	}
	return string(out)
}

// validName reports why name cannot be the name part of a store path ("" when it can).
func validName(name string) string {
	switch {
	case name == "":
		return "is empty"
	case len(name) > maxNameLen:
		return fmt.Sprintf("is longer than %d bytes", maxNameLen)
	case name[0] == '.':
		return "starts with a dot"
	}
	for i := 0; i < len(name); i++ {
		if !nameByte(name[i]) {
			return fmt.Sprintf("contains the character %q", name[i])
		}
	}
	return ""
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("+-._?=", c) >= 0
}

// MakeStorePath is makeStorePath(type, h, name) of SPEC R-NIX-006.
func MakeStorePath(typ string, h [32]byte, name string) (string, error) {
	if why := validName(name); why != "" {
		return "", fmt.Errorf("store name %q %s", name, why)
	}
	s := typ + ":sha256:" + hex.EncodeToString(h[:]) + ":" + StoreDir + ":" + name
	sum := sha256.Sum256([]byte(s))
	var folded [20]byte
	for i, b := range sum {
		folded[i%20] ^= b
	}
	return StoreDir + "/" + base32(folded[:]) + "-" + name, nil
}

// hashLen is the digest size of the hash algorithms Nix accepts for fixed outputs (0: unknown).
func hashLen(algo string) int {
	switch algo {
	case "md5":
		return 16
	case "sha1":
		return 20
	case "sha256":
		return 32
	case "sha512":
		return 64
	}
	return 0
}

// FixedOutputPath is the store path of a fixed-output result: flat files (recursive false) or
// NAR trees (recursive true) with the given algorithm and lowercase hex hash.
func FixedOutputPath(algo, hexHash string, recursive bool, name string) (string, error) {
	n := hashLen(algo)
	if n == 0 {
		return "", fmt.Errorf("unknown hash algorithm %q", algo)
	}
	raw, err := hex.DecodeString(hexHash)
	if err != nil || len(raw) != n || strings.ToLower(hexHash) != hexHash {
		return "", fmt.Errorf("%q is not a lowercase hex %s hash", hexHash, algo)
	}
	if recursive && algo == "sha256" {
		var h [32]byte
		copy(h[:], raw)
		return MakeStorePath("source", h, name)
	}
	method := ""
	if recursive {
		method = "r:"
	}
	return MakeStorePath("output:out", sha256.Sum256([]byte("fixed:out:"+method+algo+":"+hexHash+":")), name)
}

// fixedOutput splits a derivation output's algo field ("r:sha256", "sha256") into the
// recursive flag and the hash algorithm. Text and git hashing are not used by bob or nixpkgs.
func fixedOutput(algo string) (hashAlgo string, recursive bool, err error) {
	if a, ok := strings.CutPrefix(algo, "r:"); ok {
		return a, true, nil
	}
	if strings.Contains(algo, ":") {
		return "", false, fmt.Errorf("unsupported fixed-output method %q", algo)
	}
	return algo, false, nil
}

// Name joins parts into a store name (SPEC R-NIX-006 "Store names").
func Name(parts ...string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('-')
		}
		for _, r := range p {
			if r < 0x80 && nameByte(byte(r)) {
				b.WriteRune(r)
			} else {
				b.WriteByte('-')
			}
		}
	}
	s := b.String()
	if strings.HasPrefix(s, ".") {
		s = "-" + s[1:]
	}
	if len(s) > 200 {
		sum := sha256.Sum256([]byte(s))
		s = s[:191] + "-" + base32(sum[:])[:8]
	}
	return s
}
