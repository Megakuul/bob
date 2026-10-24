package builder

import (
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Licence is one entry of a bundle's licences.json (SPEC R-OUT-008).
type Licence struct {
	Path     string   `json:"path"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Licenses []string `json:"licenses"`
}

// Licences returns the licences.json entries of a closure, sorted by path: the generator's data
// for a path when it has any (nixpkgs meta), else the store name's pname and version, and
// "unknown" as the licence.
func Licences(storeDir string, closure []string, table map[string]bspec.LicenceInfo) []Licence {
	out := make([]Licence, 0, len(closure))
	for _, p := range closure {
		e := Licence{Path: p}
		if l, ok := table[p]; ok {
			e.Name, e.Version, e.Licenses = l.Name, l.Version, slices.Clone(l.Licenses)
		}
		if e.Name == "" {
			base := strings.TrimPrefix(p, storeDir+"/")
			if len(base) > 33 {
				base = base[33:]
			}
			e.Name, e.Version = SplitName(base)
		}
		if len(e.Licenses) == 0 {
			e.Licenses = []string{"unknown"}
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(x, y Licence) int { return strings.Compare(x.Path, y.Path) })
	return slices.CompactFunc(out, func(x, y Licence) bool { return x.Path == y.Path })
}

// WriteLicences writes the licences.json of a closure (see Licences) to file.
func WriteLicences(file, storeDir string, closure []string, table map[string]bspec.LicenceInfo) error {
	data, err := json.MarshalIndent(Licences(storeDir, closure, table), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}

// SplitName splits a store name ("openssl-3.6.5", "gcc-15.3.0-lib") into pname and version at
// the first "-" followed by a digit, as Nix's parseDrvName does.
func SplitName(name string) (pname, version string) {
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '-' && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
}
