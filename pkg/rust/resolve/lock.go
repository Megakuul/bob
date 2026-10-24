package resolve

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// NotLockedError reports a registry dependency of a local crate that bob.lock does not hold:
// the rust section is missing or stale. Callers report it as a stale lock (BOB-E-1012).
type NotLockedError struct {
	Crate string // the local crate
	Dep   string // the registry crate it depends on
	Req   string // the version requirement
}

func (e *NotLockedError) Error() string {
	return fmt.Sprintf("crate %s depends on %s %s, which bob.lock does not hold (run bob lock)", e.Crate, e.Dep, e.Req)
}

// IndexURL is the "index" value of bob.lock's rust section.
const IndexURL = "sparse+https://index.crates.io/"

// Crate is one crates.io crate version as bob.lock records it (SPEC §03.3.2, lock.Crate; the
// JSON shape is identical). NarHash and Meta are filled after the crate is fetched.
type Crate struct {
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Checksum string              `json:"checksum"` // SRI sha256 of the .crate (= index cksum)
	NarHash  string              `json:"narHash"`  // SRI sha256 NAR hash of the unpacked source
	Links    string              `json:"links,omitempty"`
	Deps     []CrateDep          `json:"deps,omitempty"`
	Features map[string][]string `json:"features,omitempty"`
	Meta     CrateMeta           `json:"meta"`
}

// CrateDep is one resolved dependency edge (lock.CrateDep).
type CrateDep struct {
	Name      string   `json:"name"`  // name used in code (rename key)
	Crate     string   `json:"crate"` // resolved "name@version"
	Kind      string   `json:"kind,omitempty"`
	Target    string   `json:"target,omitempty"`
	Optional  bool     `json:"optional,omitempty"`
	NoDefault bool     `json:"noDefault,omitempty"`
	Features  []string `json:"features,omitempty"`
}

// CrateMeta holds the Cargo.toml facts bob needs to plan a crate (lock.CrateMeta).
type CrateMeta struct {
	Edition   string `json:"edition"`
	LibName   string `json:"libName,omitempty"`
	LibPath   string `json:"libPath,omitempty"`
	ProcMacro bool   `json:"procMacro,omitempty"`
	Build     string `json:"build,omitempty"`
}

// SRI converts a hex sha256 (index cksum) to SRI form ("sha256-<base64>").
func SRI(hexSum string) (string, error) {
	b, err := hex.DecodeString(hexSum)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("checksum %q is not a hex sha256", hexSum)
	}
	return "sha256-" + base64.StdEncoding.EncodeToString(b), nil
}

// HexOfSRI converts an SRI sha256 back to hex (for Cargo.lock).
func HexOfSRI(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "sha256-"))
	if err != nil || len(b) != 32 || !strings.HasPrefix(s, "sha256-") {
		return "", fmt.Errorf("checksum %q is not an SRI sha256", s)
	}
	return hex.EncodeToString(b), nil
}

// Records returns bob.lock's crate entries: every registry crate with its resolved edges
// (dev-dependencies of registry crates are never built and left out; so are optional
// dependencies no feature in the workspace can enable), its feature table and links. NarHash
// and Meta stay empty. The result is sorted by name, then version.
func (r *Resolution) Records() ([]Crate, error) {
	out := make([]Crate, 0, len(r.Crates))
	for _, e := range r.Crates {
		sum, err := SRI(e.Checksum)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Key(), err)
		}
		c := Crate{Name: e.Name, Version: e.Vers, Checksum: sum, Links: e.Links}
		if len(e.Table) > 0 {
			c.Features = map[string][]string{}
			for k, vs := range e.Table {
				c.Features[k] = append([]string{}, vs...)
			}
		}
		for _, ed := range r.edges[e] {
			d := e.Deps[ed.Dep]
			if d.Kind == "dev" {
				continue
			}
			c.Deps = append(c.Deps, CrateDep{Name: d.Name, Crate: ed.To.Key(), Kind: d.Kind, Target: d.Target,
				Optional: d.Optional, NoDefault: !d.Default, Features: append([]string(nil), d.Features...)})
		}
		out = append(out, c)
	}
	return out, nil
}

// cargoPkg is one [[package]] of a Cargo.lock.
type cargoPkg struct {
	name, version string
	ver           Version
	local         bool
	checksum      string // hex
	deps          []string
}

// CargoLock renders the resolution as Cargo.lock format version 4, byte-identical to what
// `cargo generate-lockfile` writes for the same graph.
func (r *Resolution) CargoLock() (string, error) {
	var pkgs []cargoPkg
	names := map[string]int{}
	for _, e := range append(append([]*Entry{}, r.Members...), r.Crates...) {
		names[e.Name]++
	}
	for _, e := range append(append([]*Entry{}, r.Members...), r.Crates...) {
		p := cargoPkg{name: e.Name, version: e.Vers, ver: e.Version, local: e.Local, checksum: e.Checksum}
		seen := map[string]bool{}
		for _, ed := range r.edges[e] {
			ref := ed.To.Name
			if names[ref] > 1 {
				ref += " " + ed.To.Vers
			}
			if !seen[ref] {
				seen[ref] = true
				p.deps = append(p.deps, ref)
			}
		}
		pkgs = append(pkgs, p)
	}
	return renderCargoLock(pkgs)
}

// CargoLockFromRecords renders Cargo.lock from bob.lock's crate records and the members (no
// network): a member's dependency resolves to the highest locked version matching it.
func CargoLockFromRecords(members []Member, crates []Crate) (string, error) {
	byName := map[string][]*Crate{}
	names := map[string]int{}
	for i := range crates {
		c := &crates[i]
		byName[c.Name] = append(byName[c.Name], c)
		names[c.Name]++
	}
	local := map[string]Member{}
	for _, m := range members {
		local[m.Name] = m
		names[m.Name]++
	}
	ref := func(name, version string) string {
		if names[name] > 1 {
			return name + " " + version
		}
		return name
	}
	var pkgs []cargoPkg
	for _, m := range members {
		vs := m.Version
		if vs == "" {
			vs = "0.0.0"
		}
		v, err := ParseVersion(vs)
		if err != nil {
			return "", fmt.Errorf("crate %s: %w", m.Name, err)
		}
		p := cargoPkg{name: m.Name, version: vs, ver: v, local: true}
		seen := map[string]bool{}
		for _, d := range m.Deps {
			var s string
			if d.Local {
				l, ok := local[d.Package]
				if !ok {
					return "", fmt.Errorf("crate %s depends on unknown workspace crate %s", m.Name, d.Package)
				}
				lv := l.Version
				if lv == "" {
					lv = "0.0.0"
				}
				s = ref(l.Name, lv)
			} else {
				c := MatchLocked(byName[d.Package], d.Req)
				if c == nil {
					return "", &NotLockedError{Crate: m.Name, Dep: d.Package, Req: d.Req.String()}
				}
				s = ref(c.Name, c.Version)
			}
			if !seen[s] {
				seen[s] = true
				p.deps = append(p.deps, s)
			}
		}
		pkgs = append(pkgs, p)
	}
	for _, c := range crates {
		v, err := ParseVersion(c.Version)
		if err != nil {
			return "", fmt.Errorf("bob.lock crate %s: %w", c.Name, err)
		}
		sum, err := HexOfSRI(c.Checksum)
		if err != nil {
			return "", fmt.Errorf("bob.lock crate %s@%s: %w", c.Name, c.Version, err)
		}
		p := cargoPkg{name: c.Name, version: c.Version, ver: v, checksum: sum}
		seen := map[string]bool{}
		for _, d := range c.Deps {
			n, ver, ok := strings.Cut(d.Crate, "@")
			if !ok {
				return "", fmt.Errorf("bob.lock crate %s@%s: bad dependency %q", c.Name, c.Version, d.Crate)
			}
			s := ref(n, ver)
			if !seen[s] {
				seen[s] = true
				p.deps = append(p.deps, s)
			}
		}
		pkgs = append(pkgs, p)
	}
	return renderCargoLock(pkgs)
}

// MatchLocked returns the highest locked version of a crate that satisfies req, or nil.
func MatchLocked(locked []*Crate, req Req) *Crate {
	var best *Crate
	var bv Version
	for _, c := range locked {
		v, err := ParseVersion(c.Version)
		if err != nil || !req.Matches(v) {
			continue
		}
		if best == nil || v.Compare(bv) > 0 {
			best, bv = c, v
		}
	}
	return best
}

func renderCargoLock(pkgs []cargoPkg) (string, error) {
	sort.Slice(pkgs, func(i, j int) bool {
		a, b := pkgs[i], pkgs[j]
		if a.name != b.name {
			return a.name < b.name
		}
		if c := a.ver.Compare(b.ver); c != 0 {
			return c < 0
		}
		return a.local && !b.local
	})
	var b strings.Builder
	b.WriteString("# This file is automatically @generated by Cargo.\n# It is not intended for manual editing.\nversion = 4\n")
	for _, p := range pkgs {
		fmt.Fprintf(&b, "\n[[package]]\nname = %s\nversion = %s\n", tomlString(p.name), tomlString(p.version))
		if !p.local {
			if !isHex64(p.checksum) {
				return "", fmt.Errorf("crate %s@%s has no valid checksum", p.name, p.version)
			}
			fmt.Fprintf(&b, "source = \"registry+https://github.com/rust-lang/crates.io-index\"\nchecksum = %s\n", tomlString(p.checksum))
		}
		if len(p.deps) > 0 {
			sort.Slice(p.deps, func(i, j int) bool { return depRefLess(p.deps[i], p.deps[j]) })
			b.WriteString("dependencies = [\n")
			for _, d := range p.deps {
				fmt.Fprintf(&b, " %s,\n", tomlString(d))
			}
			b.WriteString("]\n")
		}
	}
	return b.String(), nil
}

// depRefLess orders "name[ version]" references as Cargo does: by name, then by the version
// text (a string comparison, not semver).
func depRefLess(a, b string) bool {
	an, av, _ := strings.Cut(a, " ")
	bn, bv, _ := strings.Cut(b, " ")
	if an != bn {
		return an < bn
	}
	return av < bv
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
