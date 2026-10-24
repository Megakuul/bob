package resolve

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// integrityAlgos are the accepted integrity algorithms, strongest first, with digest sizes.
var integrityAlgos = []struct {
	name string
	size int
}{{"sha512", 64}, {"sha256", 32}, {"sha1", 20}}

// normalizeIntegrity returns the strongest supported hash of an SRI string as "alg-base64",
// falling back to the legacy hex shasum (sha1). Unknown algorithms are ignored when a known one
// is present and rejected otherwise; malformed digests are rejected.
func normalizeIntegrity(sri, shasum string) (string, error) {
	best, bestRank := "", len(integrityAlgos)
	var unknown []string
	for _, tok := range strings.Fields(sri) {
		alg, digest, ok := strings.Cut(tok, "-")
		if !ok {
			return "", fmt.Errorf("malformed integrity %q", tok)
		}
		digest, _, _ = strings.Cut(digest, "?")
		rank := -1
		for i, a := range integrityAlgos {
			if a.name == alg {
				rank = i
			}
		}
		if rank < 0 {
			unknown = append(unknown, alg)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(digest)
		if err != nil || len(raw) != integrityAlgos[rank].size {
			return "", fmt.Errorf("malformed %s integrity digest", alg)
		}
		if rank < bestRank {
			best, bestRank = alg+"-"+base64.StdEncoding.EncodeToString(raw), rank
		}
	}
	if best != "" {
		return best, nil
	}
	if len(unknown) > 0 {
		return "", fmt.Errorf("unsupported integrity algorithm %q (bob accepts sha512, sha256 and sha1)", unknown[0])
	}
	if shasum != "" {
		raw, err := hex.DecodeString(shasum)
		if err != nil || len(raw) != 20 {
			return "", fmt.Errorf("malformed shasum %q", shasum)
		}
		return "sha1-" + base64.StdEncoding.EncodeToString(raw), nil
	}
	return "", fmt.Errorf("no integrity hash")
}

// validateManifest checks the registry data of a version bob is about to use. Registry
// documents are hostile input: names become directory names, the tarball URL is fetched and
// the integrity pins its content.
func validateManifest(m *manifest) error {
	if !ValidName(m.Name) {
		return fmt.Errorf("invalid package name %q", m.Name)
	}
	if !ValidVersion(m.Version) {
		return fmt.Errorf("invalid version %q", m.Version)
	}
	for _, deps := range []*orderedMap{m.Dependencies, m.OptionalDependencies, m.PeerDependencies} {
		for _, k := range deps.Keys() {
			if !ValidName(k) {
				return fmt.Errorf("invalid dependency name %q", k)
			}
		}
	}
	for _, k := range m.bundled {
		if !ValidName(k) {
			return fmt.Errorf("invalid bundled dependency name %q", k)
		}
	}
	sri, err := normalizeIntegrity(m.Dist.Integrity, m.Dist.Shasum)
	if err != nil {
		return err
	}
	m.Dist.Integrity = sri
	u, err := url.Parse(m.Dist.Tarball)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid tarball URL %q", m.Dist.Tarball)
	}
	for k, v := range m.bins {
		if strings.ContainsAny(k, "/\x00") || strings.Contains(v, "\x00") || strings.HasPrefix(v, "../") {
			return fmt.Errorf("invalid bin entry %q", k)
		}
	}
	for _, l := range [][]string{m.os, m.cpu, m.libc} {
		for _, x := range l {
			if len(x) > 64 || strings.ContainsAny(x, "\x00\n") {
				return fmt.Errorf("invalid platform entry %q", x)
			}
		}
	}
	return nil
}
