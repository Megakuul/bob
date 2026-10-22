package cache

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// StoreDir is the logical store directory of every served path.
const StoreDir = "/nix/store"

// MaxNarInfo bounds a narinfo (served or a worker's claim, SPEC §6.6.4 step 2).
const MaxNarInfo = 4 << 20

const nix32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

var (
	hashRE      = regexp.MustCompile(`^[0-9a-df-np-sv-z]{32}$`)
	storePathRE = regexp.MustCompile(`^/nix/store/([0-9a-df-np-sv-z]{32})-([A-Za-z0-9+\-._?=]{1,211})$`)
	sha256RE    = regexp.MustCompile(`^sha256:[0-9a-df-np-sv-z]{52}$`)
)

// ValidHash reports whether s is a store hash (32 chars of Nix base32), SPEC §6.6.1.
func ValidHash(s string) bool { return hashRE.MatchString(s) }

// ValidStorePath reports whether p is /nix/store/<hash>-<name> with a name Nix accepts.
func ValidStorePath(p string) bool {
	m := storePathRE.FindStringSubmatch(p)
	return m != nil && !strings.HasPrefix(m[2], ".")
}

// HashOf returns the store hash of a store path or base name ("" if it is neither).
func HashOf(p string) string {
	b := strings.TrimPrefix(p, StoreDir+"/")
	if len(b) < 33 || b[32] != '-' || !ValidHash(b[:32]) {
		return ""
	}
	return b[:32]
}

// Nix32 encodes h in Nix's base32 (least significant 5 bits last, no padding).
func Nix32(h []byte) string {
	n := (len(h)*8-1)/5 + 1
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b := uint(i * 5)
		j, k := b/8, b%8
		c := uint(h[j]) >> k
		if int(j)+1 < len(h) {
			c |= uint(h[j+1]) << (8 - k)
		}
		out[n-1-i] = nix32Alphabet[c&0x1f]
	}
	return string(out)
}

// NarInfo is one .narinfo. References are full store paths.
type NarInfo struct {
	StorePath   string
	URL         string
	Compression string
	FileHash    string // sha256:<nix32>
	FileSize    uint64
	NarHash     string // sha256:<nix32>
	NarSize     uint64
	References  []string
	CA          string
	Sigs        []string
}

// Info is the part a signature covers.
func (n *NarInfo) Info() PathInfo {
	return PathInfo{StorePath: n.StorePath, NarHash: n.NarHash, NarSize: n.NarSize, References: n.References}
}

// Format renders the narinfo in the field order of SPEC §6.6.1. Deriver is never written.
func (n *NarInfo) Format() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "StorePath: %s\nURL: %s\nCompression: %s\n", n.StorePath, n.URL, n.Compression)
	if n.FileHash != "" {
		fmt.Fprintf(&b, "FileHash: %s\nFileSize: %d\n", n.FileHash, n.FileSize)
	}
	fmt.Fprintf(&b, "NarHash: %s\nNarSize: %d\n", n.NarHash, n.NarSize)
	refs := make([]string, 0, len(n.References))
	for _, r := range n.References {
		refs = append(refs, strings.TrimPrefix(r, StoreDir+"/"))
	}
	slices.Sort(refs)
	fmt.Fprintf(&b, "References: %s\n", strings.Join(refs, " "))
	for _, s := range n.Sigs {
		fmt.Fprintf(&b, "Sig: %s\n", s)
	}
	if n.CA != "" {
		fmt.Fprintf(&b, "CA: %s\n", n.CA)
	}
	return b.Bytes()
}

// ParseNarInfo parses a narinfo strictly (hostile input: bounded, validated). Deriver and
// unknown keys are ignored.
func ParseNarInfo(body []byte) (*NarInfo, error) {
	if len(body) > MaxNarInfo {
		return nil, errors.New("narinfo: too large")
	}
	n := &NarInfo{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), MaxNarInfo)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("narinfo: bad line %.80q", line)
		}
		v = strings.TrimPrefix(v, " ")
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
		case "CA":
			n.CA = v
		case "Sig":
			if len(n.Sigs) >= 16 {
				return nil, errors.New("narinfo: too many signatures")
			}
			n.Sigs = append(n.Sigs, v)
		}
		if err != nil {
			return nil, fmt.Errorf("narinfo: %s: %w", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("narinfo: %w", err)
	}
	if !ValidStorePath(n.StorePath) {
		return nil, fmt.Errorf("narinfo: bad StorePath %.300q", n.StorePath)
	}
	for _, r := range n.References {
		if !ValidStorePath(r) {
			return nil, fmt.Errorf("narinfo: bad reference %.300q", r)
		}
	}
	if !sha256RE.MatchString(n.NarHash) {
		return nil, errors.New("narinfo: missing or bad NarHash")
	}
	if n.FileHash != "" && !sha256RE.MatchString(n.FileHash) {
		return nil, errors.New("narinfo: bad FileHash")
	}
	return n, nil
}
