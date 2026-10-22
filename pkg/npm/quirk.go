package npm

import "github.com/megakuul/bob/pkg/bob/diag"

// Quirk is one curated entry; the table is Go data shipped with bob (D32).
type Quirk struct {
	Package string            // exact npm name
	Range   string            // node-semver range
	Why     string            // required justification
	Skip    bool              // never run the script
	Tools   []string          // extra nixpkgs attributes for the script
	Env     map[string]string // extra env
	Test    string            // command run in the package dir after the build (required)
}

// Quirks returns the shipped table.
func Quirks() ([]Quirk, error) { return nil, diag.NotImplemented("npm quirks table") }

// CheckQuirks validates table entries: Package, Why and Test must be non-empty, otherwise the
// error is E-QUIRK-TEST (BOB-E-9006) naming the entry.
func CheckQuirks(table []Quirk) error { return diag.NotImplemented("npm quirks check") }
