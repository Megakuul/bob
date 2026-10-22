package build

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// DevVersion is the release version of a build without --version (R-OUT-022).
const DevVersion = "0.0.0-dev"

// KindRelease is the node kind of release.New (pkg/release).
const KindRelease = "release.release"

var versionRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`) })

// CheckVersion validates a release version given with --version (R-OUT-023): a definition
// error BOB-E-1020 (exit 3).
func CheckVersion(v string) error {
	if versionRe().MatchString(v) {
		return nil
	}
	return diag.New(1020, "label", "--version", "option", fmt.Sprintf("release version %q", v),
		"reason", "a version is 1 to 64 letters, digits and . + _ - starting with a letter or digit")
}

// releaseVersion is the version releases of g are built with: Options.Version, or DevVersion
// with a note when g builds a release and no version was given.
func (r *runner) releaseVersion(g *ir.Graph) string {
	if r.o.Version != "" {
		return r.o.Version
	}
	for _, n := range g.Nodes {
		if n.Kind == KindRelease {
			r.emit(Event{Event: "note", Note: &Note{Code: "no-version", Text: "no version given; using " + DevVersion}})
			break
		}
	}
	return DevVersion
}
