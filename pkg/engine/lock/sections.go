package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
)

// Section keys of bob.lock owned by the ecosystems (SPEC §03.7), plus nixpkgsFacts (§01).
const (
	SectionGo           = "go"
	SectionRust         = "rust"
	SectionNpm          = "npm"
	SectionOpam         = "opam"
	SectionCC           = "cc"
	SectionNixpkgsFacts = "nixpkgsFacts"
)

// sectionKeys lists the keys File.Sections may hold, in the order of SPEC §03.7.
var sectionKeys = []string{SectionGo, SectionRust, SectionNpm, SectionOpam, SectionCC, SectionNixpkgsFacts}

// GoSection is bob.lock's "go" section (SPEC §03.2.4).
type GoSection struct {
	Inputs     string                 `json:"inputs"`
	Proxy      string                 `json:"proxy"`      // "https://proxy.golang.org"
	Workspaces map[string]GoBuildList `json:"workspaces"` // key: workspace module path
	Modules    []GoModule             `json:"modules"`    // sorted by path, then semver
}

// GoBuildList is the build list of one workspace module.
type GoBuildList struct {
	Go      string   `json:"go"`      // "1.26.0"
	Modules []string `json:"modules"` // "path@version", sorted; every module of the pruned graph
}

// GoModule is one third-party module version.
type GoModule struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	ModH1    string `json:"modH1"`          // h1: of the .mod file
	H1       string `json:"h1,omitempty"`   // h1: of the zip (only if Packages)
	Tree     string `json:"tree,omitempty"` // SRI sha256 NAR hash of the unpacked tree (only if Packages)
	Go       string `json:"go,omitempty"`   // the module's own go directive
	Packages bool   `json:"packages"`       // provides packages to some build
}

// RustSection is bob.lock's "rust" section (SPEC §03.3.2).
type RustSection struct {
	Inputs string  `json:"inputs"`
	Index  string  `json:"index"`  // "sparse+https://index.crates.io/"
	Crates []Crate `json:"crates"` // sorted by name, then semver
}

// Crate is one crates.io crate version.
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

// CrateDep is one dependency edge of a crate.
type CrateDep struct {
	Name      string   `json:"name"`                // name used in code (after rename)
	Crate     string   `json:"crate"`               // resolved "name@version"
	Kind      string   `json:"kind,omitempty"`      // "" (normal) | "build" | "dev"
	Target    string   `json:"target,omitempty"`    // cfg expression or triple
	Optional  bool     `json:"optional,omitempty"`  //
	NoDefault bool     `json:"noDefault,omitempty"` // default-features = false
	Features  []string `json:"features,omitempty"`
}

// CrateMeta holds the Cargo.toml facts bob needs to plan a crate (D58).
type CrateMeta struct {
	Edition   string `json:"edition"`
	LibName   string `json:"libName,omitempty"` // if ≠ name with '-'→'_'
	LibPath   string `json:"libPath,omitempty"` // if ≠ "src/lib.rs"
	ProcMacro bool   `json:"procMacro,omitempty"`
	Build     string `json:"build,omitempty"` // build script path
}

// NpmSection is bob.lock's "npm" section (SPEC §03.4.2).
type NpmSection struct {
	Inputs    string                 `json:"inputs"`
	Registry  string                 `json:"registry"`  // "https://registry.npmjs.org/"
	Importers map[string]NpmImporter `json:"importers"` // key: repo-relative importer dir
	Packages  map[string]NpmPackage  `json:"packages"`  // key: instance key
}

// NpmImporter is one importer (a directory with npm dependencies).
type NpmImporter struct {
	Deps map[string]string `json:"deps"`          // alias → instance key or "link:<dir>"
	Dev  []string          `json:"dev,omitempty"` // aliases that are Dev()
}

// NpmPackage is one package instance.
type NpmPackage struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`         // sha512-… (sha256/sha1 legacy accepted)
	Tarball          string            `json:"tarball,omitempty"` // only if not the default registry URL
	Deps             map[string]string `json:"deps,omitempty"`    // alias → instance key
	Peers            []string          `json:"peers,omitempty"`
	Optional         bool              `json:"optional,omitempty"`
	OS               []string          `json:"os,omitempty"`
	CPU              []string          `json:"cpu,omitempty"`
	Libc             []string          `json:"libc,omitempty"`
	Bundled          []string          `json:"bundled,omitempty"`
	HasInstallScript bool              `json:"hasInstallScript,omitempty"`
	Bin              map[string]string `json:"bin,omitempty"` // command → relative path
}

// ToolLock is a lock of an npm-based generator embedded in bob (SPEC §03.6.5): an NpmSection
// without inputs and with one importer "tool".
type ToolLock struct {
	Registry  string                 `json:"registry"`
	Importers map[string]NpmImporter `json:"importers"`
	Packages  map[string]NpmPackage  `json:"packages"`
}

// OpamSection is bob.lock's "opam" section (SPEC §03.5.2).
type OpamSection struct {
	Inputs     string                 `json:"inputs"`
	Repository OpamRepository         `json:"repository"`
	Packages   map[string]OpamPackage `json:"packages"` // key: opam name
}

// OpamRepository is the pinned opam-repository snapshot.
type OpamRepository struct {
	URL     string `json:"url"`     // "github:ocaml/opam-repository"
	Commit  string `json:"commit"`  // 40 hex
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked snapshot
}

// OpamPackage is one opam package version.
type OpamPackage struct {
	Version string   `json:"version"`
	URL     string   `json:"url"`
	Hash    string   `json:"hash"`           // SRI sha512 or sha256 from the opam file's checksum (flat)
	Opam    string   `json:"opam"`           // sha256 hex of the opam file in the snapshot
	Deps    []string `json:"deps,omitempty"` // depends + present depopts, minus provided packages
}

// CCSection is bob.lock's "cc" section (SPEC §03.7.6).
type CCSection struct {
	Inputs    string               `json:"inputs"`
	Externals map[string]CExternal `json:"externals"` // key: label ("//third_party/zstd:zstd")
}

// CExternal is one cc.External source.
type CExternal struct {
	URL     string `json:"url"`
	Strip   int    `json:"strip"`
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked, stripped, mode-normalised tree
}

// NixpkgsFacts is the informational "nixpkgsFacts" member: system → attribute → facts.
type NixpkgsFacts map[string]map[string]NixpkgsFact

// NixpkgsFact is what bob.lock records about one nixpkgs attribute (for review diffs).
type NixpkgsFact struct {
	Version string `json:"version"`
}

// DecodeSection decodes and validates one ecosystem section ("go", "rust", "npm", "opam",
// "cc"; also "nixpkgsFacts") into its §03.7 type (a pointer, e.g. *GoSection). Names, versions
// and hashes are checked against the ecosystem's grammar; invalid input is an error, never a
// panic.
func DecodeSection(key string, raw json.RawMessage) (any, error) {
	var v interface{ validate() error }
	switch key {
	case SectionGo:
		v = &GoSection{}
	case SectionRust:
		v = &RustSection{}
	case SectionNpm:
		v = &NpmSection{}
	case SectionOpam:
		v = &OpamSection{}
	case SectionCC:
		v = &CCSection{}
	case SectionNixpkgsFacts:
		v = &NixpkgsFacts{}
	default:
		return nil, fmt.Errorf("unknown section %q (known: %s)", clip(key), strings.Join(sectionKeys, ", "))
	}
	if len(raw) > maxLockBytes {
		return nil, fmt.Errorf("section %s is larger than %d bytes", key, maxLockBytes)
	}
	if err := strictDecode(raw, v); err != nil {
		return nil, fmt.Errorf("section %s: %w", key, err)
	}
	if err := v.validate(); err != nil {
		return nil, fmt.Errorf("section %s: %w", key, err)
	}
	return v, nil
}

// EncodeSection encodes a section decoded by DecodeSection (or a value of a section type)
// canonically: compact JSON, keys sorted, arrays in schema order. Content that fails
// validation is a *diag.Error BOB-E-1023 naming the item.
func EncodeSection(key string, v any) (json.RawMessage, error) {
	ok := false
	switch key {
	case SectionGo:
		ok = isType[GoSection](v)
	case SectionRust:
		ok = isType[RustSection](v)
	case SectionNpm:
		ok = isType[NpmSection](v)
	case SectionOpam:
		ok = isType[OpamSection](v)
	case SectionCC:
		ok = isType[CCSection](v)
	case SectionNixpkgsFacts:
		ok = isType[NixpkgsFacts](v)
	default:
		return nil, fmt.Errorf("unknown section %q", clip(key))
	}
	if !ok {
		return nil, fmt.Errorf("section %s: cannot encode a %T", key, v)
	}
	b, err := marshalCanonical(v)
	if err != nil {
		return nil, fmt.Errorf("section %s: %w", key, err)
	}
	// bob never writes what it cannot read back: an invalid item is BOB-E-1023.
	if _, err := DecodeSection(key, b); err != nil {
		return nil, unpinnable(key, err)
	}
	return b, nil
}

func isType[T any](v any) bool {
	switch x := v.(type) {
	case T:
		return true
	case *T:
		return x != nil
	}
	return false
}

// strictDecode decodes one JSON value into v, rejecting unknown fields and trailing data.
func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return atEOF(dec)
}

func (s *GoSection) validate() error {
	if err := checkInputs(s.Inputs); err != nil {
		return err
	}
	if s.Proxy != "" { // "" = the default proxy
		if err := checkHTTPURL("proxy", s.Proxy, ""); err != nil {
			return err
		}
	}
	for ws, bl := range s.Workspaces {
		if err := checkGoPath(ws); err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		if !goVerRe().MatchString(bl.Go) {
			return fmt.Errorf("workspace %s: go version %q is invalid", ws, clip(bl.Go))
		}
		for _, m := range bl.Modules {
			p, v, ok := strings.Cut(m, "@")
			if !ok || !goSemverRe().MatchString(v) {
				return fmt.Errorf("workspace %s: module %q is not path@version", ws, clip(m))
			}
			if err := checkGoPath(p); err != nil {
				return fmt.Errorf("workspace %s: %w", ws, err)
			}
		}
	}
	for _, m := range s.Modules {
		if err := checkGoPath(m.Path); err != nil {
			return err
		}
		if !goSemverRe().MatchString(m.Version) {
			return fmt.Errorf("module %s: version %q is not a semantic version", m.Path, clip(m.Version))
		}
		if err := checkH1("module "+m.Path+" modH1", m.ModH1); err != nil {
			return err
		}
		if m.H1 != "" {
			if err := checkH1("module "+m.Path+" h1", m.H1); err != nil {
				return err
			}
		}
		if m.Tree != "" {
			if err := checkSRI("module "+m.Path+" tree", m.Tree, "sha256"); err != nil {
				return err
			}
		}
		if m.Go != "" && !goVerRe().MatchString(m.Go) {
			return fmt.Errorf("module %s: go version %q is invalid", m.Path, clip(m.Go))
		}
	}
	return nil
}

var editions = []string{"2015", "2018", "2021", "2024"}

func (s *RustSection) validate() error {
	if err := checkInputs(s.Inputs); err != nil {
		return err
	}
	if s.Index != "" { // "" = the default index
		if err := checkHTTPURL("index", s.Index, "sparse+"); err != nil {
			return err
		}
	}
	for _, c := range s.Crates {
		if err := validateCrate(c); err != nil {
			return &ItemError{Item: "crates.io:" + clip(c.Name) + "@" + clip(c.Version), Err: err}
		}
	}
	return nil
}

// validateCrate checks one locked crate (registry metadata: hostile input).
func validateCrate(c Crate) error {
	if !crateRe().MatchString(c.Name) {
		return fmt.Errorf("crate name %q is invalid", clip(c.Name))
	}
	if !semverRe().MatchString(c.Version) {
		return fmt.Errorf("crate %s: version %q is not a semantic version", c.Name, clip(c.Version))
	}
	if err := checkSRI("crate "+c.Name+" checksum", c.Checksum, "sha256"); err != nil {
		return err
	}
	if err := checkSRI("crate "+c.Name+" narHash", c.NarHash, "sha256"); err != nil {
		return err
	}
	if c.Links != "" && !crateRe().MatchString(c.Links) {
		return fmt.Errorf("crate %s: links %q is invalid", c.Name, clip(c.Links))
	}
	for _, d := range c.Deps {
		if !crateRe().MatchString(d.Name) {
			return fmt.Errorf("crate %s: dependency name %q is invalid", c.Name, clip(d.Name))
		}
		n, v, ok := strings.Cut(d.Crate, "@")
		if !ok || !crateRe().MatchString(n) || !semverRe().MatchString(v) {
			return fmt.Errorf("crate %s: dependency %q is not name@version", c.Name, clip(d.Crate))
		}
		if !slices.Contains([]string{"", "build", "dev"}, d.Kind) {
			return fmt.Errorf("crate %s: dependency kind %q is invalid", c.Name, clip(d.Kind))
		}
		if d.Target != "" {
			if err := checkTarget("crate "+c.Name+" dependency target", d.Target); err != nil {
				return err
			}
		}
		for _, f := range d.Features {
			if !featureRe().MatchString(f) {
				return fmt.Errorf("crate %s: feature %q is invalid", c.Name, clip(f))
			}
		}
	}
	for f, fs := range c.Features {
		if !featureRe().MatchString(f) {
			return fmt.Errorf("crate %s: feature %q is invalid", c.Name, clip(f))
		}
		for _, x := range fs {
			if !featureRe().MatchString(x) {
				return fmt.Errorf("crate %s: feature %q enables invalid %q", c.Name, f, clip(x))
			}
		}
	}
	m := c.Meta
	if !slices.Contains(editions, m.Edition) {
		return fmt.Errorf("crate %s: edition %q is invalid", c.Name, clip(m.Edition))
	}
	if m.LibName != "" && !crateRe().MatchString(m.LibName) {
		return fmt.Errorf("crate %s: libName %q is invalid", c.Name, clip(m.LibName))
	}
	for _, p := range []string{m.LibPath, m.Build} {
		if p != "" {
			if err := checkRelDir("crate "+c.Name+" path", p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *NpmSection) validate() error {
	if err := checkInputs(s.Inputs); err != nil {
		return err
	}
	return validateNpm(s.Registry, s.Importers, s.Packages)
}

func validateNpm(registry string, importers map[string]NpmImporter, packages map[string]NpmPackage) error {
	if registry != "" { // "" = the default registry
		if err := checkHTTPURL("registry", registry, ""); err != nil {
			return err
		}
	}
	for dir, imp := range importers {
		if err := checkRelDir("importer", dir); err != nil {
			return err
		}
		if err := checkNpmDeps("importer "+dir, imp.Deps, true); err != nil {
			return err
		}
		for _, a := range imp.Dev {
			if _, ok := imp.Deps[a]; !ok {
				return fmt.Errorf("importer %s: dev dependency %q is not a dependency", dir, clip(a))
			}
		}
	}
	for key, p := range packages {
		if err := checkText("package key", key); err != nil {
			return err
		}
		if len(p.Name) > 214 || !npmNameRe().MatchString(p.Name) {
			return fmt.Errorf("package %s: name %q is invalid", clip(key), clip(p.Name))
		}
		if !semverRe().MatchString(p.Version) {
			return fmt.Errorf("package %s: version %q is not a semantic version", clip(key), clip(p.Version))
		}
		if err := checkSRI("package "+clip(key)+" integrity", p.Integrity, "sha512", "sha256", "sha1"); err != nil {
			return err
		}
		if p.Tarball != "" {
			if err := checkHTTPURL("package "+clip(key)+" tarball", p.Tarball, ""); err != nil {
				return err
			}
		}
		if err := checkNpmDeps("package "+clip(key), p.Deps, false); err != nil {
			return err
		}
		for _, list := range [][]string{p.Peers, p.Bundled} {
			for _, n := range list {
				if len(n) > 214 || !npmNameRe().MatchString(n) {
					return fmt.Errorf("package %s: name %q is invalid", clip(key), clip(n))
				}
			}
		}
		for _, list := range [][]string{p.OS, p.CPU, p.Libc} {
			for _, w := range list {
				if !npmWordRe().MatchString(w) {
					return fmt.Errorf("package %s: platform value %q is invalid", clip(key), clip(w))
				}
			}
		}
		for cmd, rel := range p.Bin {
			if err := checkText("package "+clip(key)+" command", cmd); err != nil || strings.ContainsAny(cmd, "/\\") {
				return fmt.Errorf("package %s: command %q is invalid", clip(key), clip(cmd))
			}
			if err := checkRelDir("package "+clip(key)+" bin", rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkNpmDeps validates alias → instance key (or "link:<dir>" when links are allowed).
func checkNpmDeps(what string, deps map[string]string, links bool) error {
	for alias, target := range deps {
		if len(alias) > 214 || !npmNameRe().MatchString(alias) {
			return fmt.Errorf("%s: dependency name %q is invalid", what, clip(alias))
		}
		if dir, ok := strings.CutPrefix(target, "link:"); ok && links {
			if err := checkRelDir(what+" link", dir); err != nil {
				return err
			}
			continue
		}
		if err := checkText(what+" dependency "+alias, target); err != nil {
			return err
		}
	}
	return nil
}

func (s *OpamSection) validate() error {
	if err := checkInputs(s.Inputs); err != nil {
		return err
	}
	r := s.Repository
	if err := checkText("repository url", r.URL); err != nil {
		return err
	}
	if !hex40Re().MatchString(r.Commit) {
		return fmt.Errorf("repository commit %q is not 40 hex digits", clip(r.Commit))
	}
	if err := checkSRI("repository narHash", r.NarHash, "sha256"); err != nil {
		return err
	}
	for name, p := range s.Packages {
		if len(name) > maxName || !opamNameRe().MatchString(name) {
			return fmt.Errorf("opam package name %q is invalid", clip(name))
		}
		if len(p.Version) > maxName || !opamVerRe().MatchString(p.Version) {
			return fmt.Errorf("opam package %s: version %q is invalid", name, clip(p.Version))
		}
		if p.URL != "" || p.Hash != "" { // virtual packages (base-bytes) and conf-* have no source
			if err := checkHTTPURL("opam package "+name+" url", p.URL, ""); err != nil {
				return err
			}
			if err := checkSRI("opam package "+name+" hash", p.Hash, "sha512", "sha256"); err != nil {
				return err
			}
		}
		if !hex64Re().MatchString(p.Opam) {
			return fmt.Errorf("opam package %s: opam digest %q is not sha256 hex", name, clip(p.Opam))
		}
		for _, d := range p.Deps {
			if len(d) > maxName || !opamNameRe().MatchString(d) {
				return fmt.Errorf("opam package %s: dependency %q is invalid", name, clip(d))
			}
		}
	}
	return nil
}

// maxStrip bounds the leading path components an archive may drop.
const maxStrip = 64

func (s *CCSection) validate() error {
	if err := checkInputs(s.Inputs); err != nil {
		return err
	}
	for l, e := range s.Externals {
		if len(l) > maxName {
			return fmt.Errorf("external label %q is too long", clip(l))
		}
		if _, err := bob.ParseLabel(l); err != nil {
			return fmt.Errorf("external label %q is invalid", clip(l))
		}
		if err := checkHTTPURL("external "+l+" url", e.URL, ""); err != nil {
			return err
		}
		if e.Strip < 0 || e.Strip > maxStrip {
			return fmt.Errorf("external %s: strip %d is out of range 0..%d", l, e.Strip, maxStrip)
		}
		if err := checkSRI("external "+l+" narHash", e.NarHash, "sha256"); err != nil {
			return err
		}
	}
	return nil
}

func (f *NixpkgsFacts) validate() error {
	for sys, attrs := range *f {
		if !slices.Contains([]string{"aarch64-linux", "x86_64-linux"}, sys) {
			return fmt.Errorf("nixpkgsFacts: unknown system %q", clip(sys))
		}
		for a, fact := range attrs {
			if err := checkText("nixpkgsFacts attribute", a); err != nil {
				return err
			}
			if fact.Version != "" {
				if err := checkText("nixpkgsFacts version", fact.Version); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
