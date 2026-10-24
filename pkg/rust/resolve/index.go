package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Limits on registry metadata (hostile input, AGENTS.md §5.4).
const (
	MaxIndexFile    = 16 << 20 // one sparse-index file
	maxIndexLine    = 4 << 20  // one version line
	maxVersions     = 50000    // versions of one crate
	maxDepsPerEntry = 2000     // dependency lines of one version
	maxFeatures     = 5000     // feature table entries of one version
	maxIndexSchema  = 2        // highest index schema ("v") Cargo understands
)

// ErrNoCrate is returned by a Registry for a crate the registry does not have.
var ErrNoCrate = errors.New("no such crate")

// Registry yields the published versions of a crate. Implementations must be safe for
// concurrent use; the sparse index client (Index) is the production one.
type Registry interface {
	Versions(ctx context.Context, name string) ([]*Entry, error)
}

// Prefetcher is an optional Registry extension: load several crates at once (in parallel).
type Prefetcher interface {
	Prefetch(ctx context.Context, names []string)
}

// IndexDep is one dependency of a published version (sparse index format).
type IndexDep struct {
	Name            string   `json:"name"` // name in Cargo.toml (rename key, used in code)
	Req             string   `json:"req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures *bool    `json:"default_features"`
	Target          string   `json:"target"` // cfg(...) or a triple; "" = every platform
	Kind            string   `json:"kind"`   // "normal" | "build" | "dev" ("" = normal)
	Registry        string   `json:"registry"`
	Package         string   `json:"package"` // real crate name when renamed
}

// IndexEntry is one line of a sparse-index file: one published version.
type IndexEntry struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []IndexDep          `json:"deps"`
	Cksum       string              `json:"cksum"`
	Features    map[string][]string `json:"features"`
	Features2   map[string][]string `json:"features2"`
	Yanked      bool                `json:"yanked"`
	Links       string              `json:"links"`
	V           int                 `json:"v"`
	RustVersion string              `json:"rust_version"`
}

// Dep is a dependency as the resolver sees it.
type Dep struct {
	Name     string   // name in Cargo.toml (rename key)
	Package  string   // crate name to resolve
	Req      Req      // version requirement (ignored for Local)
	Local    bool     // a workspace member (path dependency)
	Kind     string   // "" (normal) | "build" | "dev"
	Target   string   // cfg(...) or triple; "" = every platform
	Optional bool     //
	Default  bool     // default-features
	Features []string // features enabled on the dependency
}

// Entry is a parsed, validated crate version (Cargo's "summary").
type Entry struct {
	Name        string
	Version     Version
	Vers        string // version as published
	Checksum    string // hex sha256 of the .crate ("" for workspace members)
	Yanked      bool
	Links       string
	RustVersion string
	Deps        []Dep
	// Table is the published feature table (features ∪ features2); Features adds the
	// implicit feature of every optional dependency no "dep:" value names.
	Table    map[string][]string
	Features map[string][]string
	Local    bool // workspace member
}

// Key is "name@version", the identity of a crate version in the lock.
func (e *Entry) Key() string { return e.Name + "@" + e.Vers }

// ParseIndexFile parses one sparse-index file. Lines Cargo would not use (unparsable
// versions, a newer index schema) are skipped; malformed JSON fails the crate.
func ParseIndexFile(name string, body []byte) ([]*Entry, error) {
	if len(body) > MaxIndexFile {
		return nil, fmt.Errorf("index file of %s is larger than %d bytes", name, MaxIndexFile)
	}
	var out []*Entry
	for len(body) > 0 {
		line := body
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			line, body = body[:i], body[i+1:]
		} else {
			body = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if len(line) > maxIndexLine {
			return nil, fmt.Errorf("index of %s: a line is larger than %d bytes", name, maxIndexLine)
		}
		var ie IndexEntry
		if err := json.Unmarshal(line, &ie); err != nil {
			return nil, fmt.Errorf("index of %s: malformed line: %w", name, err)
		}
		if ie.V > maxIndexSchema || !strings.EqualFold(ie.Name, name) {
			continue
		}
		e, err := NewEntry(&ie)
		if err != nil {
			continue // Cargo ignores versions it cannot read; so does bob
		}
		if len(out) == maxVersions {
			return nil, fmt.Errorf("index of %s has more than %d versions", name, maxVersions)
		}
		out = append(out, e)
	}
	return out, nil
}

// NewEntry validates an index line and builds its feature map.
func NewEntry(ie *IndexEntry) (*Entry, error) {
	if !ValidName(ie.Name) {
		return nil, fmt.Errorf("invalid crate name %q", ie.Name)
	}
	v, err := ParseVersion(ie.Vers)
	if err != nil {
		return nil, err
	}
	if len(ie.Deps) > maxDepsPerEntry || len(ie.Features)+len(ie.Features2) > maxFeatures {
		return nil, fmt.Errorf("%s %s: too many dependencies or features", ie.Name, ie.Vers)
	}
	if ie.Cksum != "" && !isHex64(ie.Cksum) {
		return nil, fmt.Errorf("%s %s: bad checksum", ie.Name, ie.Vers)
	}
	e := &Entry{Name: ie.Name, Version: v, Vers: ie.Vers, Checksum: ie.Cksum, Yanked: ie.Yanked,
		Links: ie.Links, RustVersion: ie.RustVersion}
	for _, d := range ie.Deps {
		dep, err := indexDep(d)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", ie.Name, ie.Vers, err)
		}
		e.Deps = append(e.Deps, dep)
	}
	e.Table = map[string][]string{}
	for k, vs := range ie.Features {
		e.Table[k] = vs
	}
	for k, vs := range ie.Features2 {
		e.Table[k] = vs
	}
	e.Features = FeatureMap(e.Table, e.Deps)
	return e, nil
}

func indexDep(d IndexDep) (Dep, error) {
	pkg := d.Package
	if pkg == "" {
		pkg = d.Name
	}
	if !ValidName(d.Name) || !ValidName(pkg) {
		return Dep{}, fmt.Errorf("invalid dependency name %q", d.Name)
	}
	if d.Registry != "" && d.Registry != "https://github.com/rust-lang/crates.io-index" {
		return Dep{}, fmt.Errorf("dependency %s is from another registry (%s)", d.Name, d.Registry)
	}
	req, err := ParseReq(d.Req)
	if err != nil {
		return Dep{}, err
	}
	kind := d.Kind
	switch kind {
	case "normal", "":
		kind = ""
	case "build", "dev":
	default:
		return Dep{}, fmt.Errorf("dependency %s has unknown kind %q", d.Name, d.Kind)
	}
	def := d.DefaultFeatures == nil || *d.DefaultFeatures
	return Dep{Name: d.Name, Package: pkg, Req: req, Kind: kind, Target: d.Target, Optional: d.Optional,
		Default: def, Features: d.Features}, nil
}

// ValidName reports whether s is a crate name crates.io can hold.
func ValidName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IndexPath is the sparse-index path of a crate (Cargo reference, "Index format").
func IndexPath(name string) string {
	n := strings.ToLower(name)
	switch len(n) {
	case 1:
		return "1/" + n
	case 2:
		return "2/" + n
	case 3:
		return "3/" + n[:1] + "/" + n
	}
	return n[:2] + "/" + n[2:4] + "/" + n
}

// MemRegistry is an in-memory Registry (tests, recorded snapshots).
type MemRegistry map[string][]*Entry

// Versions implements Registry.
func (m MemRegistry) Versions(_ context.Context, name string) ([]*Entry, error) {
	if es, ok := m[strings.ToLower(name)]; ok {
		return es, nil
	}
	return nil, fmt.Errorf("%s: %w", name, ErrNoCrate)
}
