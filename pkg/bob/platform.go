package bob

import "github.com/megakuul/bob/pkg/bob/diag"

// Platform is an operating system and CPU architecture packages are built for.
type Platform string

const (
	LinuxAMD64 Platform = "linux-amd64" // Nix system x86_64-linux
	LinuxARM64 Platform = "linux-arm64" // Nix system aarch64-linux
)

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

// Named is a reusable variant referred to by name from the CLI and pipelines (SPEC §1.9
// names both this type and its constructor NamedVariant, which Go cannot declare twice).
type Named struct {
	name string
	mods []VariantMod
}

// NamedVariant declares a named variant.
//
//	bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))
func NamedVariant(name string, mods ...VariantMod) Named { return Named{name: name, mods: mods} }

// VariantMod changes a variant.
type VariantMod func(*Variant)

// WithProfile sets the profile.
func WithProfile(p Profile) VariantMod { return func(*Variant) {} }

// WithSanitize adds sanitisers.
func WithSanitize(s ...Sanitizer) VariantMod { return func(*Variant) {} }

// WithRace enables the Go race detector.
func WithRace() VariantMod { return func(*Variant) {} }

// WithFeatures adds Rust features of crate.
func WithFeatures(crate Package, features ...string) VariantMod { return func(*Variant) {} }

// Configured is a configured label: a label, a platform and a variant (R-FW-050).
type Configured struct {
	Label    Label
	Platform Platform
	Variant  Variant
}

// ParseConfigured parses `label "@" [platform] *("+" token)` (SPEC §1.3, §1.9). An omitted
// platform is host; named lists the repository's named variants.
func ParseConfigured(s string, host Platform, named ...string) (Configured, error) {
	return Configured{}, diag.NotImplemented("bob.ParseConfigured")
}

// String returns the canonical form: platform always printed; tokens ordered profile (omitted
// if release), asan, ubsan, race, named variants alphabetically.
func (c Configured) String() string { return "" }
