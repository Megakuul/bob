package npm

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
)

// Quirk is one curated entry; the table is Go data shipped with bob (D32). An entry is admitted
// only if the package cannot work with deny-or-allow alone; phantom dependencies are never
// quirks (they are fixed with npm.Extend).
type Quirk struct {
	Package string            // exact npm name
	Range   string            // node-semver range
	Why     string            // required justification
	Skip    bool              // never run the script
	Tools   []nixext.Attr     // extra host tools of the script
	Env     map[string]string // extra env
	Test    string            // command run in the package dir after the build (required)
}

// quirks is the v1 table (both entries verified in S4, SPEC §03.4.5).
var quirks = []Quirk{
	{
		Package: "better-sqlite3", Range: ">=8",
		Why: "its install script runs prebuild-install, which downloads a prebuilt addon; offline it " +
			"must build the addon from source with node-gyp instead",
		Env:  map[string]string{"npm_config_build_from_source": "true"},
		Test: `node -e "const D = require('./'); const db = new D(':memory:'); process.exit(db.prepare('select sqlite_version() v').get().v ? 0 : 1)"`,
	},
	{
		Package: "esbuild", Range: ">=0.15",
		Why: "its postinstall only re-checks the @esbuild/<platform> binary, which bob already links " +
			"as an optional dependency; it would try to download one when the check fails",
		Skip: true,
		Test: `node -e "process.exit(require('./').transformSync('let a: number = 1', { loader: 'ts' }).code.includes('let a = 1') ? 0 : 1)"`,
	},
}

// Quirks returns the shipped table.
func Quirks() ([]Quirk, error) {
	out := make([]Quirk, len(quirks))
	for i, q := range quirks {
		q.Tools = slices.Clone(q.Tools)
		q.Env = maps.Clone(q.Env)
		out[i] = q
	}
	return out, CheckQuirks(out)
}

// CheckQuirks validates table entries: Package, Range, Why and Test must be non-empty, otherwise
// the error is E-QUIRK-TEST (BOB-E-9006) naming the entry.
func CheckQuirks(table []Quirk) error {
	var errs []error
	for i, q := range table {
		if strings.TrimSpace(q.Package) != "" && strings.TrimSpace(q.Range) != "" &&
			strings.TrimSpace(q.Why) != "" && strings.TrimSpace(q.Test) != "" {
			continue
		}
		name := q.Package
		if strings.TrimSpace(name) == "" {
			name = "#" + strconv.Itoa(i)
		}
		d := diag.New(9006, "entry", name)
		errs = append(errs, fmt.Errorf("%s: %w", d.Code.ID(), d)) // CI output names the code

	}
	return errors.Join(errs...)
}
