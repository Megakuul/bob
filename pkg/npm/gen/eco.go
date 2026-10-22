package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
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

// declaration is what the npm section is resolved from (R-FW-062). Node is the pinned
// Node.js version the engines check used ("" when unknown: then the hash is the one of a
// declaration without it).
type declaration struct {
	Input    resolve.Input
	Registry string
	Node     string `json:",omitempty"`
}

func inputsOf(in resolve.Input, registry, node string) (string, error) {
	return lock.Inputs(declaration{Input: in, Registry: registry, Node: node})
}

// nodeVersion is the version of the pinned nodejs the importers run on (the resolver's
// engines.node check, like npm on that Node); "" when the lock request has no nixpkgs index or
// the importers name several Node.js packages (then the check is off).
func nodeVersion(ctx context.Context, req engine.LockRequest, ims []*importer) string {
	if req.Nixpkgs == nil || req.Graph == nil {
		return ""
	}
	byID := map[string]*ir.Node{}
	for _, n := range req.Graph.Nodes {
		byID[n.ID] = n
	}
	attrs := map[string]bool{}
	for _, im := range ims {
		i := im.spec.Tools.Node
		if i < 0 || i >= len(im.node.Deps) {
			continue
		}
		if n := byID[im.node.Deps[i]]; n != nil && n.Nix != nil {
			attrs[n.Nix.Attr] = true
		}
	}
	if len(attrs) != 1 {
		return ""
	}
	var attr string
	for a := range attrs {
		attr = a
	}
	es, err := req.Nixpkgs.Lookup(ctx, []string{attr})
	if err != nil {
		return ""
	}
	e := es[attr]
	if e.Missing || e.Version == "" {
		return ""
	}
	if _, err := resolve.ParseVersion(e.Version, false); err != nil {
		return ""
	}
	return e.Version
}

// previousPins are the versions of the previous section by package name (resolve.Options.Prefer):
// bob lock keeps them unless --update names the package, names npm, or has no names.
func previousPins(prev *lock.NpmSection, update []string) map[string][]string {
	if update != nil && len(update) == 0 {
		return nil // --update without names
	}
	moving := map[string]bool{}
	for _, u := range update {
		moving[u] = true
	}
	if moving[lock.SectionNpm] {
		return nil // --update npm: the whole ecosystem moves
	}
	out := map[string][]string{}
	for _, p := range prev.Packages {
		if moving[p.Name] || hasString(out[p.Name], p.Version) {
			continue
		}
		out[p.Name] = append(out[p.Name], p.Version)
	}
	return out
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
	if err := pinRuntimes(ctx, req, ims); err != nil {
		return nil, err
	}
	in, err := resolveInput(ims, byID)
	if err != nil {
		return nil, err
	}
	reg := registryURL()
	node := nodeVersion(ctx, req, ims)
	inputs, err := inputsOf(in, reg, node)
	if err != nil {
		return nil, err
	}
	prev, perr := decodeSection(req.Prev)
	if perr == nil && prev != nil && prev.Inputs == inputs && req.Update == nil {
		return lock.EncodeSection(lock.SectionNpm, prev)
	}
	opts := resolve.Options{Registry: &resolve.Registry{URL: reg, CacheDir: metaCache(), MaxAge: 24 * time.Hour},
		Root: "the repository", NodeVersion: node}
	if perr == nil && prev != nil && prev.Registry == reg {
		opts.Prefer = previousPins(prev, req.Update)
	}
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
	// A runtime pin that contradicts its generator is the R-GEN-006 error, not a stale lock.
	var mismatch *diag.Error
	if err := pinRuntimes(context.Background(), req, ims); errors.As(err, &mismatch) && mismatch.Code == 1021 {
		return "", ir.Pos{}, mismatch
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

// Materialises reports whether g has npm importers of this repository: their node_modules go
// into the working tree (engine.Materialiser).
func (Ecosystem) Materialises(g *ir.Graph) bool {
	ims, _, err := importers(g)
	return err == nil && len(ims) > 0
}

// pinRuntimes replaces the generator placeholders of codegen runtime dependencies by the
// versions of the generators in the nixpkgs pin (§03.6.4) and checks explicit dependencies on
// a runtime against them (R-GEN-006): a range that excludes the generator's version is
// BOB-E-1021 before anything is resolved or built.
func pinRuntimes(ctx context.Context, req engine.LockRequest, ims []*importer) error {
	nodes := map[string]*ir.Node{}
	if req.Graph != nil {
		for _, n := range req.Graph.Nodes {
			nodes[n.ID] = n
		}
	}
	versions := map[string]string{}
	version := func(attr string) (string, error) {
		if v, ok := versions[attr]; ok {
			return v, nil
		}
		if req.Nixpkgs == nil {
			return "", fmt.Errorf("the version of generator %s needs the nixpkgs index", attr)
		}
		es, err := req.Nixpkgs.Lookup(ctx, []string{attr})
		if err != nil {
			return "", err
		}
		e := es[attr]
		if e.Missing || e.Version == "" {
			return "", fmt.Errorf("nixpkgs has no generator %s", attr)
		}
		versions[attr] = e.Version
		return e.Version, nil
	}
	for _, im := range ims {
		for _, g := range im.spec.Generated {
			label, at := im.node.Label, im.node.DefinedAt
			if g.Dep >= 0 && g.Dep < len(im.node.Deps) && nodes[im.node.Deps[g.Dep]] != nil {
				gn := nodes[im.node.Deps[g.Dep]]
				label, at = gn.Label, gn.DefinedAt
			}
			for _, r := range g.Runtime {
				v, err := version(r.Generator)
				if err != nil {
					return fmt.Errorf("%s: %w", label, err)
				}
				for i, d := range im.spec.Deps {
					if d.Alias != r.Name || d.Link >= 0 && d.Alias == "" {
						continue
					}
					if strings.HasPrefix(d.Req, npmspec.GeneratorPrefix) {
						im.spec.Deps[i].Req = v
						continue
					}
					if !validRange(d.Req) || !resolve.Satisfies(v, d.Req) {
						file := path.Join(im.dir, "bob.go")
						if im.dir == "." {
							file = "bob.go"
						}
						return engine.RuntimeMismatch(label, r.Generator, v, r.Name, v, file, d.Req, at)
					}
				}
			}
		}
	}
	return nil
}
