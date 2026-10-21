package crates

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LockedCrate is one crates.io crate in bob.lock (spike format, JSON).
//
// Everything bob needs to plan a build is here, so evaluation never touches the network
// and never needs the crate sources (a warm cache can serve every output).
type LockedCrate struct {
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Checksum string              `json:"checksum"`          // SRI sha256 of the .crate file (= index cksum)
	NarHash  string              `json:"narHash,omitempty"` // SRI NAR hash of the unpacked crate (fetch output)
	Deps     []LockedDep         `json:"deps,omitempty"`
	Features map[string][]string `json:"features,omitempty"`
	Links    string              `json:"links,omitempty"`
	Meta     *CrateMeta          `json:"meta,omitempty"` // from the crate's Cargo.toml, filled by `lock`
}

// LockedDep is one dependency edge, already resolved to a version.
type LockedDep struct {
	Name      string   `json:"name"`             // name used in code (rename key)
	Crate     string   `json:"crate,omitempty"`  // "name@version" it resolved to; "" = not in lock (inactive)
	Kind      string   `json:"kind,omitempty"`   // "" normal | build | dev
	Target    string   `json:"target,omitempty"` // cfg(...) / triple
	Optional  bool     `json:"optional,omitempty"`
	NoDefault bool     `json:"noDefault,omitempty"`
	Features  []string `json:"features,omitempty"`
}

// CrateMeta is what the index does not carry but rustc needs.
type CrateMeta struct {
	Edition   string `json:"edition"`
	LibName   string `json:"libName,omitempty"` // when different from name with '-' -> '_'
	LibPath   string `json:"libPath,omitempty"` // when not src/lib.rs
	ProcMacro bool   `json:"procMacro,omitempty"`
	Build     string `json:"build,omitempty"` // build script path, "" = none
}

type Lock struct {
	Version int            `json:"version"`
	Crates  []*LockedCrate `json:"crates"`
}

func cksumSRI(hexsum string) string {
	b, err := hex.DecodeString(hexsum)
	if err != nil || len(b) != 32 {
		return ""
	}
	return "sha256-" + base64.StdEncoding.EncodeToString(b)
}

// ToLock converts a resolution (registry crates only) into bob.lock entries.
func (r *Resolution) ToLock() *Lock {
	l := &Lock{Version: 1}
	for _, p := range r.Pkgs {
		if p.Local {
			continue
		}
		e := p.Entry
		c := &LockedCrate{Name: e.Name, Version: e.Vers, Checksum: cksumSRI(e.Cksum), Links: e.Links, Features: e.AllFeatures()}
		if len(c.Features) == 0 {
			c.Features = nil
		}
		for i, d := range e.Deps {
			if d.Kind == "dev" {
				continue // dev-deps of registry crates are never built
			}
			ld := LockedDep{Name: d.Name, Crate: p.Edges[i], Kind: d.Kind, Target: d.Target, Optional: d.Optional,
				NoDefault: !d.DefaultFeatures, Features: d.Features}
			if ld.Kind == "normal" {
				ld.Kind = ""
			}
			if ld.Crate == "" && !d.Optional {
				continue
			}
			if ld.Crate == "" {
				continue // optional and never activated by any feature in this workspace
			}
			c.Deps = append(c.Deps, ld)
		}
		l.Crates = append(l.Crates, c)
	}
	sort.Slice(l.Crates, func(i, j int) bool {
		return keyLess(l.Crates[i].Name, l.Crates[i].Version, l.Crates[j].Name, l.Crates[j].Version)
	})
	return l
}

func keyLess(an, av, bn, bv string) bool {
	if an != bn {
		return an < bn
	}
	va, _ := ParseVersion(av)
	vb, _ := ParseVersion(bv)
	return va.Compare(vb) < 0
}

func (l *Lock) Write(path string) error {
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func ReadLock(path string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

func (l *Lock) Get(key string) *LockedCrate {
	for _, c := range l.Crates {
		if c.Name+"@"+c.Version == key {
			return c
		}
	}
	return nil
}

// CargoLock renders a Cargo.lock (format version 4) for rust-analyzer / cargo tooling.
// Local crates come from r (their edges), registry crates from the resolution as well.
func (r *Resolution) CargoLock() string {
	type pk struct {
		name, vers string
		local      bool
		cksum      string
		deps       []string
	}
	byName := map[string]int{}
	for _, p := range r.Pkgs {
		byName[p.Entry.Name]++
	}
	var ps []pk
	for _, p := range r.Pkgs {
		x := pk{name: p.Entry.Name, vers: p.Entry.Vers, local: p.Local, cksum: p.Entry.Cksum}
		seen := map[string]bool{}
		for _, k := range p.Edges {
			q := r.Pkgs[k]
			s := q.Entry.Name
			if byName[s] > 1 {
				s += " " + q.Entry.Vers
			}
			if !seen[s] {
				seen[s] = true
				x.deps = append(x.deps, s)
			}
		}
		sort.Strings(x.deps)
		ps = append(ps, x)
	}
	sort.Slice(ps, func(i, j int) bool { return keyLess(ps[i].name, ps[i].vers, ps[j].name, ps[j].vers) })
	var b strings.Builder
	b.WriteString("# This file is automatically @generated by Cargo.\n# It is not intended for manual editing.\nversion = 4\n")
	for _, p := range ps {
		fmt.Fprintf(&b, "\n[[package]]\nname = %q\nversion = %q\n", p.name, p.vers)
		if !p.local {
			fmt.Fprintf(&b, "source = \"registry+https://github.com/rust-lang/crates.io-index\"\nchecksum = %q\n", p.cksum)
		}
		if len(p.deps) > 0 {
			b.WriteString("dependencies = [\n")
			for _, d := range p.deps {
				fmt.Fprintf(&b, " %q,\n", d)
			}
			b.WriteString("]\n")
		}
	}
	return b.String()
}

// CargoLockFromLock renders Cargo.lock from bob.lock + the workspace (no network).
func CargoLockFromLock(w *Workspace, l *Lock) string {
	r := &Resolution{Pkgs: map[string]*Pkg{}}
	for _, c := range l.Crates {
		e := &IndexEntry{Name: c.Name, Vers: c.Version, Cksum: hexOfSRI(c.Checksum)}
		p := &Pkg{Entry: e, Edges: map[int]string{}}
		for i, d := range c.Deps {
			if d.Crate != "" {
				p.Edges[i] = d.Crate
			}
		}
		r.Pkgs[c.Name+"@"+c.Version] = p
	}
	for _, wc := range w.Crates {
		p := &Pkg{Entry: &IndexEntry{Name: wc.Name, Vers: wc.Version}, Local: true, Edges: map[int]string{}}
		for i, d := range wc.Deps {
			if d.Local {
				p.Edges[i] = d.Name + "@" + w.Get(d.Name).Version
				continue
			}
			name := d.Name
			if d.Crate != "" {
				name = d.Crate
			}
			req, _ := ParseReq(d.Req)
			best := ""
			var bv Version
			for _, c := range l.Crates {
				if c.Name != name {
					continue
				}
				v, _ := ParseVersion(c.Version)
				if req.Matches(v) && (best == "" || v.Compare(bv) > 0) {
					best, bv = c.Name+"@"+c.Version, v
				}
			}
			if best != "" {
				p.Edges[i] = best
			}
		}
		r.Pkgs[wc.Name+"@"+wc.Version] = p
	}
	return r.CargoLock()
}

func hexOfSRI(s string) string {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "sha256-"))
	if err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
