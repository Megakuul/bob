package bob

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Platform is an operating system and CPU architecture packages are built for.
type Platform string

const (
	LinuxAMD64 Platform = "linux-amd64" // Nix system x86_64-linux
	LinuxARM64 Platform = "linux-arm64" // Nix system aarch64-linux
)

var platforms = []Platform{LinuxAMD64, LinuxARM64}

// Profile is the optimisation profile of a variant.
type Profile string

const (
	Release Profile = "release"
	Debug   Profile = "debug"
)

// Sanitizer is a C/C++ (and cgo) sanitiser.
type Sanitizer string

const (
	ASan  Sanitizer = "asan"
	UBSan Sanitizer = "ubsan"
)

// Variant is the configuration of one build request. The zero value means release, no extras.
type Variant struct {
	Profile  Profile
	Sanitize []Sanitizer
	Race     bool               // Go -race only
	Features map[Label][]string // Rust features added on top of the crate's declared ones
	Named    []string           // named variants that produced this variant
}

// normal returns v with the release profile spelled out, sanitisers deduplicated in canonical
// order (asan, ubsan), features and named variants sorted, and fresh slices and maps, so that a
// constructor cannot change the variant of its P.
func (v Variant) normal() Variant {
	out := Variant{Profile: v.Profile, Race: v.Race}
	if out.Profile == "" {
		out.Profile = Release
	}
	for _, s := range []Sanitizer{ASan, UBSan} {
		if slices.Contains(v.Sanitize, s) {
			out.Sanitize = append(out.Sanitize, s)
		}
	}
	if len(v.Features) > 0 {
		out.Features = make(map[Label][]string, len(v.Features))
		for l, fs := range v.Features {
			fs = slices.Clone(fs)
			sort.Strings(fs)
			out.Features[l] = slices.Compact(fs)
		}
	}
	out.Named = slices.Clone(v.Named)
	sort.Strings(out.Named)
	out.Named = slices.Compact(out.Named)
	return out
}

// tokens returns the canonical "+" tokens of v: profile (omitted if release), asan, ubsan, race,
// named variants alphabetically (R-FW-050).
func (v Variant) tokens() []string {
	n := v.normal()
	var out []string
	if n.Profile != Release {
		out = append(out, string(n.Profile))
	}
	for _, s := range n.Sanitize {
		out = append(out, string(s))
	}
	if n.Race {
		out = append(out, "race")
	}
	return append(out, n.Named...)
}

// Named is a reusable variant referred to by name from the CLI and pipelines. SPEC §1.9 names
// both this type and its constructor NamedVariant, which Go cannot declare twice; the type is
// Named.
type Named struct {
	name string
	mods []VariantMod
}

// NamedVariant declares a named variant.
//
//	bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))
func NamedVariant(name string, mods ...VariantMod) Named { return Named{name: name, mods: mods} }

// Name returns the variant's name.
func (n Named) Name() string { return n.name }

// apply applies the named variant's modifications to v.
func (n Named) apply(v *Variant) {
	for _, m := range n.mods {
		if m != nil {
			m(v)
		}
	}
}

// VariantMod changes a variant.
type VariantMod func(*Variant)

// WithProfile sets the profile.
func WithProfile(p Profile) VariantMod { return func(v *Variant) { v.Profile = p } }

// WithSanitize adds sanitisers.
func WithSanitize(s ...Sanitizer) VariantMod {
	return func(v *Variant) { v.Sanitize = append(v.Sanitize, s...) }
}

// WithRace enables the Go race detector.
func WithRace() VariantMod { return func(v *Variant) { v.Race = true } }

// WithFeatures adds Rust features of crate.
func WithFeatures(crate Package, features ...string) VariantMod {
	return func(v *Variant) {
		if isNil(crate) {
			return
		}
		if v.Features == nil {
			v.Features = map[Label][]string{}
		}
		l := crate.Label()
		v.Features[l] = append(v.Features[l], features...)
	}
}

// Configured is a configured label: a label, a platform and a variant (R-FW-050).
type Configured struct {
	Label    Label
	Platform Platform
	Variant  Variant
}

// ParseConfigured parses `label "@" [platform] *("+" token)` (SPEC §1.3, §1.9). An omitted
// platform is host; named lists the repository's named variants.
func ParseConfigured(s string, host Platform, named ...string) (Configured, error) {
	c, err := parseConfigured(s, named)
	if err != nil {
		return Configured{}, err
	}
	if c.Platform == "" {
		c.Platform = host
	}
	if err := checkNamed(c.Variant, named); err != nil {
		return Configured{}, err
	}
	return c, nil
}

// checkNamed fails for a named variant of v that is not one of named.
func checkNamed(v Variant, named []string) error {
	for _, n := range v.Named {
		if !slices.Contains(named, n) {
			known := slices.Clone(named)
			sort.Strings(known)
			known = append([]string{"release", "debug", "asan", "ubsan", "race"}, known...)
			return fmt.Errorf("%w: unknown variant %q (known: %s)", ErrSyntax, n, strings.Join(known, ", "))
		}
	}
	return nil
}

// parseConfigured parses a configured label without host or named-variant knowledge.
func parseConfigured(s string, named []string) (Configured, error) {
	if len(s) > maxRefBytes {
		return Configured{}, syntaxErr(s, fmt.Sprintf("longer than %d bytes", maxRefBytes))
	}
	ls, suffix, ok := strings.Cut(s, "@")
	if !ok {
		return Configured{}, syntaxErr(s, `a configured label has the form <label>@[platform][+variant...]`)
	}
	l, err := ParseLabel(ls)
	if err != nil {
		return Configured{}, err
	}
	v, plat, err := parseConfig(suffix, named)
	if err != nil {
		return Configured{}, syntaxErr(s, err.Error())
	}
	return Configured{Label: l, Platform: plat, Variant: v}, nil
}

// parseConfig parses "[platform] *("+" token)". Tokens in named are named variants (a named
// variant may shadow a built-in token); other built-in tokens set their field; any other token
// is kept as a named variant too (checkNamed rejects it when the named variants are known).
func parseConfig(suffix string, named []string) (Variant, Platform, error) {
	parts := strings.Split(suffix, "+")
	plat := Platform(parts[0])
	if plat != "" && !slices.Contains(platforms, plat) {
		return Variant{}, "", fmt.Errorf("unknown platform %q (known: linux-amd64, linux-arm64)", plat)
	}
	var v Variant
	for _, tok := range parts[1:] {
		if err := checkName(tok); err != nil {
			return Variant{}, "", fmt.Errorf("invalid variant token %q", tok)
		}
		if slices.Contains(named, tok) {
			v.Named = append(v.Named, tok)
			continue
		}
		switch tok {
		case "release", "debug":
			if v.Profile != "" && v.Profile != Profile(tok) {
				return Variant{}, "", fmt.Errorf("variants %q and %q conflict: two profiles", v.Profile, tok)
			}
			v.Profile = Profile(tok)
		case "asan", "ubsan":
			v.Sanitize = append(v.Sanitize, Sanitizer(tok))
		case "race":
			v.Race = true
		default:
			v.Named = append(v.Named, tok)
		}
	}
	return v.normal(), plat, nil
}

// configString is the canonical "<platform>[+token...]" of a configuration.
func configString(plat Platform, v Variant) string {
	return strings.Join(append([]string{string(plat)}, v.tokens()...), "+")
}

// String returns the canonical form: platform always printed; tokens ordered profile (omitted
// if release), asan, ubsan, race, named variants alphabetically.
func (c Configured) String() string {
	return c.Label.String() + "@" + configString(c.Platform, c.Variant)
}
