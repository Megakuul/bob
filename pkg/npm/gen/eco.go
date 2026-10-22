package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
	"github.com/megakuul/bob/pkg/npm/resolve"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem is the TypeScript/npm ecosystem (SPEC §03.4, ADR 0006): engine.Ecosystem and
// engine.LockFreshness.
type Ecosystem struct{}

// Name is the bob.lock section key and the kind prefix.
func (Ecosystem) Name() string { return lock.SectionNpm }

// registryURL is the npm registry bob resolves against: $BOB_NPM_REGISTRY (test mirrors,
// private registries) or the public registry.
func registryURL() string {
	if u := os.Getenv("BOB_NPM_REGISTRY"); u != "" {
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		return u
	}
	return resolve.DefaultRegistry
}

// metaCache is the registry document cache ($XDG_CACHE_HOME/bob/npm-meta).
func metaCache() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "bob", "npm-meta")
}

// declaration is what the npm section is resolved from (R-FW-062).
type declaration struct {
	Input    resolve.Input
	Registry string
}

func inputsOf(in resolve.Input, registry string) (string, error) {
	return lock.Inputs(declaration{Input: in, Registry: registry})
}

func decodeSection(raw json.RawMessage) (*lock.NpmSection, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	v, err := lock.DecodeSection(lock.SectionNpm, raw)
	if err != nil {
		return nil, err
	}
	return v.(*lock.NpmSection), nil
}

// Lock resolves the npm importers of the graph (SPEC §03.4.2). The previous section is kept
// when the declarations and the registry are unchanged and nothing is to be updated.
func (Ecosystem) Lock(ctx context.Context, req engine.LockRequest) (json.RawMessage, error) {
	ims, byID, err := importers(req.Graph)
	if err != nil {
		return nil, err
	}
	in, err := resolveInput(ims, byID)
	if err != nil {
		return nil, err
	}
	reg := registryURL()
	inputs, err := inputsOf(in, reg)
	if err != nil {
		return nil, err
	}
	if prev, err := decodeSection(req.Prev); err == nil && prev != nil && prev.Inputs == inputs && req.Update == nil {
		return lock.EncodeSection(lock.SectionNpm, prev)
	}
	opts := resolve.Options{Registry: &resolve.Registry{URL: reg, CacheDir: metaCache(), MaxAge: 24 * time.Hour},
		Root: "the repository"}
	res, err := resolve.Resolve(ctx, in, opts)
	if err != nil {
		var re *resolve.Error
		if errors.As(err, &re) {
			return nil, re.Diag()
		}
		return nil, err
	}
	raw, err := json.Marshal(res.Section)
	if err != nil {
		return nil, err
	}
	var sec lock.NpmSection
	if err := json.Unmarshal(raw, &sec); err != nil {
		return nil, err
	}
	sec.Inputs = inputs
	out, err := lock.EncodeSection(lock.SectionNpm, &sec)
	if err != nil {
		// The resolver validated every registry document; a record the lock refuses is
		// hostile or unusual registry data.
		return nil, diag.New(1015, "ecosystem", "npm", "root", "the repository", "reason", err.Error())
	}
	if req.Note != nil {
		for _, w := range res.Warnings {
			req.Note(w)
		}
		for _, n := range scriptNotes(ims, &sec) {
			req.Note(n)
		}
	}
	return out, nil
}

// scriptNotes are the once-per-lock notes about install scripts bob does not run (R-NPM-004):
// one per package version with an install script that some importer reaches without allowing
// it (and that no quirk skips).
func scriptNotes(ims []*importer, sec *lock.NpmSection) []string {
	seen := map[string]bool{}
	var out []string
	for _, im := range ims {
		allowed := map[string]bool{}
		for _, a := range im.spec.Options.AllowScripts {
			allowed[a] = true
		}
		for _, key := range closure(sec, sec.Importers[im.dir].Deps) {
			p := sec.Packages[key]
			id := p.Name + "@" + p.Version
			// Packages that never install on Linux (fsevents) need no note: bob builds for Linux.
			if !p.HasInstallScript || allowed[p.Name] || seen[id] || quirkSkips(p.Name, p.Version) || !matches(p.OS, "linux") {
				continue
			}
			seen[id] = true
			out = append(out, fmt.Sprintf("%s has an install script; bob does not run it. If the package needs it: npm.AllowScripts(%q)", id, p.Name))
		}
	}
	sort.Strings(out)
	return out
}

// closure lists the instance keys reachable from deps (links are left out), sorted.
func closure(sec *lock.NpmSection, deps map[string]string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(key string) {
		if seen[key] || strings.HasPrefix(key, "link:") {
			return
		}
		p, ok := sec.Packages[key]
		if !ok {
			return
		}
		seen[key] = true
		for _, d := range p.Deps {
			walk(d)
		}
	}
	for _, k := range deps {
		walk(k)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quirkFor returns the quirks table entry of name@version.
func quirkFor(name, version string) (npm.Quirk, bool) {
	table, err := npm.Quirks()
	if err != nil {
		return npm.Quirk{}, false
	}
	for _, q := range table {
		if q.Package != name {
			continue
		}
		if resolve.Satisfies(version, q.Range) {
			return q, true
		}
	}
	return npm.Quirk{}, false
}

func quirkSkips(name, version string) bool {
	q, ok := quirkFor(name, version)
	return ok && q.Skip
}

// Stale reports whether the npm section no longer reflects the declarations (R-FW-062): an
// importer without a lock record, a dependency that is not locked, locked at a version its
// range excludes, or no longer declared. Only the importers of the graph are compared with
// their own lock records, so a build of part of the repository sees the same answer as one of
// all of it.
func (Ecosystem) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	ims, byID, err := importers(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	sec, err := decodeSection(req.Prev)
	if err != nil {
		return "the npm section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	if sec == nil {
		return "", ir.Pos{}, nil
	}
	for _, im := range ims {
		if what := staleImporter(im, byID, sec); what != "" {
			return what, im.node.DefinedAt, nil
		}
	}
	return "", ir.Pos{}, nil
}

func staleImporter(im *importer, byID map[string]*importer, sec *lock.NpmSection) string {
	rec, ok := sec.Importers[im.dir]
	if !ok {
		return fmt.Sprintf("npm importer %s is not locked", im.dir)
	}
	dev := map[string]bool{}
	for _, a := range rec.Dev {
		dev[a] = true
	}
	declared := map[string]bool{}
	check := func(d npmspec.Dep, peer bool) string {
		alias := d.Alias
		want := ""
		if d.Link >= 0 && d.Alias == "" {
			lib, err := depImporter(im.node, d.Link, byID)
			if err != nil {
				return err.Error()
			}
			alias, want = lib.spec.Name, "link:"+lib.dir
		}
		declared[alias] = true
		key, ok := rec.Deps[alias]
		if !ok {
			return fmt.Sprintf("npm %s %s is not locked", alias, d.Req)
		}
		if want != "" {
			if key != want {
				return fmt.Sprintf("npm %s is locked as %s, not as the library in %s", alias, key, want)
			}
			return ""
		}
		p, ok := sec.Packages[key]
		name := d.Name
		if name == "" {
			name = d.Alias
		}
		switch {
		case !ok || p.Name != name:
			return fmt.Sprintf("npm %s is locked as %s, not as %s", alias, key, name)
		case !peer && dev[alias] != d.Dev:
			return fmt.Sprintf("npm %s changed between a dependency and npm.Dev()", alias)
		case d.Req != "" && validRange(d.Req) && !resolve.Satisfies(p.Version, d.Req):
			return fmt.Sprintf("npm %s %s is locked at %s", alias, d.Req, p.Version)
		}
		return ""
	}
	for _, d := range im.spec.Deps {
		if what := check(d, false); what != "" {
			return what
		}
	}
	for _, d := range im.spec.Peers {
		if what := check(d, true); what != "" {
			return what
		}
	}
	for _, alias := range sortedKeys(rec.Deps) {
		if !declared[alias] {
			return fmt.Sprintf("npm %s is locked but no longer declared", alias)
		}
	}
	return ""
}

// Native returns package.json and tsconfig.json of every importer (§03.4.6, R-NPM-008).
func (Ecosystem) Native(_ context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	if req.Graph == nil {
		return nil, nil
	}
	ims, byID, err := importers(req.Graph)
	if err != nil {
		return nil, err
	}
	sec, err := decodeSection(req.Lock)
	if err != nil {
		sec = nil // an unreadable section is reported by the lock check; write declared ranges
	}
	return nativeFiles(ims, byID, sec)
}

// validRange reports whether req is a node-semver range (not a dist-tag such as "latest").
func validRange(req string) bool {
	_, err := resolve.ParseRange(req)
	return err == nil
}
