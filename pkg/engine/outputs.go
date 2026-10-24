package engine

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nixidx"
)

// Shared plan-time helpers of the output generators (SPEC §04: OCI images, tarballs, AppImages,
// releases). They work on closures returned by PlanCloser.Closure.

// ClosureOf returns the closure of roots in refs (path → references), sorted.
func ClosureOf(refs map[string][]string, roots ...string) []string {
	seen := map[string]bool{}
	var walk func(p string)
	walk = func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		for _, r := range refs[p] {
			walk(r)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// StoreName splits the name part of a store path the way Nix splits a derivation name: the
// version starts at the first "-" that is followed by a character other than a letter
// ("glibc-2.42-47" → "glibc", "2.42-47"; "nss-cacert-3.115" → "nss-cacert", "3.115").
func StoreName(storePath string) (name, version string) {
	base := path.Base(storePath)
	if i := strings.IndexByte(base, '-'); i == 32 {
		base = base[33:]
	}
	for i := 0; i+1 < len(base); i++ {
		if base[i] == '-' {
			c := base[i+1]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
				return base[:i], base[i+1:]
			}
		}
	}
	return base, ""
}

// Licence is one entry of licences.json (R-OUT-008).
type Licence struct {
	Path     string   `json:"path"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Licenses []string `json:"licenses"`
}

// UnknownLicence is the licence of a path nothing declares one for (R-OUT-008).
const UnknownLicence = "unknown"

// Licences returns the licences.json entries of paths, sorted by path. own holds the entries of
// paths bob built (name = package name, licence from the package, default "unknown"); every
// other path is looked up in nixpkgs by the name part of its store name (meta.license of the
// attribute of that name). A failed or missing lookup gives "unknown"; the lookup is best
// effort and never fails a build.
func Licences(ctx context.Context, idx *nixidx.Index, paths []string, own map[string]Licence) []Licence {
	var attrs []string
	for _, p := range paths {
		if _, ok := own[p]; ok {
			continue
		}
		if n, _ := StoreName(p); attrName.MatchString(n) {
			attrs = append(attrs, n)
		}
	}
	entries := map[string]nixidx.Entry{}
	if idx != nil && len(attrs) > 0 {
		// Attributes that fail to evaluate are only missing from the answer.
		entries, _ = idx.Lookup(ctx, slices.Compact(slices.Sorted(slices.Values(attrs))))
	}
	out := make([]Licence, 0, len(paths))
	for _, p := range paths {
		if l, ok := own[p]; ok {
			l.Path = p
			if len(l.Licenses) == 0 {
				l.Licenses = []string{UnknownLicence}
			}
			out = append(out, l)
			continue
		}
		name, version := StoreName(p)
		l := Licence{Path: p, Name: name, Version: version}
		if e, ok := entries[name]; ok && !e.Missing {
			l.Licenses = entryLicences(e)
		}
		if len(l.Licenses) == 0 {
			l.Licenses = []string{UnknownLicence}
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// entryLicences returns the SPDX ids of a nixpkgs index entry's meta.license, sorted.
func entryLicences(e nixidx.Entry) []string {
	ids := slices.Clone(e.Licenses)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// attrName is the shape of a top-level nixpkgs attribute bob looks licences up under.
var attrName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_'-]*$`)

// SPDXExpression joins the licences of ls into one SPDX expression ("MIT AND Apache-2.0");
// "unknown" becomes NOASSERTION.
func SPDXExpression(ls []Licence) string {
	set := map[string]bool{}
	for _, l := range ls {
		for _, id := range l.Licenses {
			if id == UnknownLicence {
				id = "NOASSERTION"
			}
			set[id] = true
		}
	}
	if len(set) == 0 {
		return "NOASSERTION"
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 1 {
		ids = slices.DeleteFunc(ids, func(s string) bool { return s == "NOASSERTION" })
	}
	return strings.Join(ids, " AND ")
}

// OwnLicences returns the licences.json entries of the paths built by bob packages of the graph
// (outputs of non-nixpkgs nodes in inputs): name = the package name, licence "unknown".
func OwnLicences(g *ir.Graph, inputs map[string]Resolved) map[string]Licence {
	out := map[string]Licence{}
	if g == nil {
		return out
	}
	for _, n := range g.Nodes {
		if n.Nix != nil || strings.HasPrefix(n.Kind, "nixpkgs.") {
			continue
		}
		rv, ok := inputs[n.ID]
		if !ok {
			continue
		}
		name := n.Label
		if i := strings.LastIndexByte(name, ':'); i >= 0 {
			name = name[i+1:]
		}
		for _, p := range rv.Outputs {
			if _, done := out[p]; !done {
				out[p] = Licence{Path: p, Name: name, Licenses: []string{UnknownLicence}}
			}
		}
	}
	return out
}

// PrimaryOutputs returns the outputs a package contributes to a bundle as a root: every output
// a nixpkgs node names explicitly, else its "bin" output when it has one, else "out" (the
// programs of a nixpkgs package; dev, man and doc stay out); for bob packages "out".
func PrimaryOutputs(n *ir.Node, rv Resolved) []PlanOutput {
	var names []string
	switch {
	case n != nil && n.Nix != nil && len(n.Nix.Outputs) > 0:
		names = slices.Clone(n.Nix.Outputs)
	case rv.Outputs["bin"] != "":
		names = []string{"bin"}
	case rv.Outputs["out"] != "":
		names = []string{"out"}
	default:
		for o := range rv.Outputs {
			names = append(names, o)
		}
		sort.Strings(names)
		names = names[:min(1, len(names))]
	}
	var out []PlanOutput
	for _, o := range names {
		if p := rv.Outputs[o]; p != "" {
			out = append(out, PlanOutput{Drv: rv.Drv, Name: o, Path: p})
		}
	}
	return out
}

// PruneGlob compiles a prune pattern (R-OUT-007): matched against /nix/store/<hash>-<name>/<path>;
// "*" and "?" do not cross "/", "**" does.
func PruneGlob(pattern string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "/nix/store/") {
		return nil, fmt.Errorf("prune pattern %q must start with /nix/store/", pattern)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Pruning is the result of applying prune patterns to the files of a closure.
type Pruning struct {
	// Skip lists, per store path, the entries removed from the bundle's copy (absolute
	// /nix/store/... paths; a removed directory stands for its whole subtree), sorted.
	Skip map[string][]string
	// Used lists, per store path, the patterns that removed something from it (in pattern order).
	Used map[string][]string
	// by maps each removed entry to the first pattern that removed it.
	by map[string]string
}

// Prune applies patterns to the files of paths, read through st (host paths). The store paths
// themselves are never modified; only the bundle's copy leaves the entries out (R-OUT-007).
func Prune(st PlanStore, paths, patterns []string) (*Pruning, error) {
	res := &Pruning{Skip: map[string][]string{}, Used: map[string][]string{}, by: map[string]string{}}
	if len(patterns) == 0 {
		return res, nil
	}
	globs := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		g, err := PruneGlob(p)
		if err != nil {
			return nil, err
		}
		globs[i] = g
	}
	for _, sp := range paths {
		host := st.HostPath(sp)
		used := map[int]bool{}
		err := filepath.WalkDir(host, func(hp string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(host, hp)
			if rel == "." {
				return nil
			}
			abs := sp + "/" + filepath.ToSlash(rel)
			for i, g := range globs {
				if g.MatchString(abs) {
					used[i] = true
					res.Skip[sp] = append(res.Skip[sp], abs)
					res.by[abs] = patterns[i]
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("pruning %s: %w", sp, err)
		}
		for i := range patterns {
			if used[i] {
				res.Used[sp] = append(res.Used[sp], patterns[i])
			}
		}
	}
	return res, nil
}

// removedBy returns the pattern that removed the entry abs (or one of its parent directories).
func (r *Pruning) removedBy(abs string) (string, bool) {
	for p := abs; strings.HasPrefix(p, "/nix/store/") && len(p) > len("/nix/store/"); p = path.Dir(p) {
		if pat, ok := r.by[p]; ok {
			return pat, true
		}
	}
	return "", false
}

// CheckPruned reports a pruned file that an ELF file left in the bundle needs (DT_NEEDED,
// resolved through its RUNPATH/RPATH in the closure): BOB-E-2015 for label (R-OUT-007).
// programs (store paths of the bundle's program files) are checked first, together with the
// kept libraries they load, and a hit names the program (SPEC: "removes libssl.so.3, which api
// needs" although api loads libssl through libfoo.so); every other file names itself.
func CheckPruned(st PlanStore, label string, paths []string, r *Pruning, programs ...string) error {
	if r == nil || len(r.by) == 0 {
		return nil
	}
	inClosure := map[string]bool{}
	for _, p := range paths {
		inClosure[p] = true
	}
	// need returns the first library the store file abs needs that a pattern removed, and the
	// kept libraries it loads.
	need := func(abs string) (lib, pat string, kept []string) {
		needed, runpath := elfNeeds(st.HostPath(storeRoot(abs)) + strings.TrimPrefix(abs, storeRoot(abs)))
		for _, l := range needed {
			for _, dir := range runpath {
				dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", path.Dir(abs)), "$ORIGIN", path.Dir(abs))
				cand := path.Clean(dir + "/" + l)
				if !strings.HasPrefix(cand, "/nix/store/") || !inClosure[storeRoot(cand)] {
					continue
				}
				if _, err := os.Lstat(st.HostPath(storeRoot(cand)) + strings.TrimPrefix(cand, storeRoot(cand))); err != nil {
					continue
				}
				if p, gone := r.removedBy(cand); gone {
					return l, p, kept
				}
				kept = append(kept, cand)
				break
			}
		}
		return "", "", kept
	}
	for _, prog := range programs {
		seen := map[string]bool{}
		queue := []string{prog}
		for len(queue) > 0 {
			f := queue[0]
			queue = queue[1:]
			if seen[f] {
				continue
			}
			seen[f] = true
			lib, pat, kept := need(f)
			if lib != "" {
				return diag.New(2015, "label", label, "pattern", pat, "file", lib, "user", path.Base(prog))
			}
			queue = append(queue, kept...)
		}
	}
	for _, sp := range paths {
		host := st.HostPath(sp)
		var found error
		err := filepath.WalkDir(host, func(hp string, d fs.DirEntry, err error) error {
			if err != nil || found != nil {
				return err
			}
			rel, _ := filepath.Rel(host, hp)
			abs := sp
			if rel != "." {
				abs = sp + "/" + filepath.ToSlash(rel)
			}
			if _, gone := r.removedBy(abs); gone {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if lib, pat, _ := need(abs); lib != "" {
				found = diag.New(2015, "label", label, "pattern", pat, "file", lib, "user", path.Base(abs))
				return found
			}
			return nil
		})
		if found != nil {
			return found
		}
		if err != nil {
			return fmt.Errorf("checking the pruned closure of %s: %w", sp, err)
		}
	}
	return nil
}

// storeRoot returns /nix/store/<hash>-<name> of a path below it.
func storeRoot(p string) string {
	rest := strings.TrimPrefix(p, "/nix/store/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return "/nix/store/" + rest
}

// elfNeeds returns the DT_NEEDED entries and the RUNPATH (or RPATH) directories of an ELF file;
// anything else gives nothing.
func elfNeeds(file string) (needed, runpath []string) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || !bytes.Equal(magic, []byte("\x7fELF")) {
		return nil, nil
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return nil, nil
	}
	defer ef.Close()
	needed, _ = ef.DynString(elf.DT_NEEDED)
	rp, _ := ef.DynString(elf.DT_RUNPATH)
	if len(rp) == 0 {
		rp, _ = ef.DynString(elf.DT_RPATH)
	}
	for _, e := range rp {
		runpath = append(runpath, strings.Split(e, ":")...)
	}
	return needed, runpath
}

// Jobs plans the bob-builder derivations of one output generator run (no fixup: output kinds
// keep their bytes exactly).
type Jobs struct {
	req  PlanRequest
	own  map[string]*drv.Derivation
	Drvs []*drv.Derivation // in planning order
	// modulo remembers hashDerivationModulo by drv path: a drv path names its derivation's
	// ATerm, so the answer for a path never changes (a job's inputs were hashed once per input
	// edge before, recursively).
	mu     sync.Mutex
	modulo map[string][32]byte
}

// NewJobs returns an empty job set for req.
func NewJobs(req PlanRequest) *Jobs {
	return &Jobs{req: req, own: map[string]*drv.Derivation{}, modulo: map[string][32]byte{}}
}

// Modulo resolves the derivations planned here first, then the engine's.
func (j *Jobs) Modulo(p string) ([32]byte, error) {
	j.mu.Lock()
	h, ok := j.modulo[p]
	d, own := j.own[p]
	j.mu.Unlock()
	if ok {
		return h, nil
	}
	var err error
	switch {
	case own:
		h, err = d.Modulo(j)
	case j.req.Resolver == nil:
		return [32]byte{}, fmt.Errorf("no resolver for input derivation %s", p)
	default:
		h, err = j.req.Resolver.Modulo(p)
	}
	if err != nil {
		return h, err
	}
	j.mu.Lock()
	if j.modulo == nil {
		j.modulo = map[string][32]byte{}
	}
	j.modulo[p] = h
	j.mu.Unlock()
	return h, nil
}

// Job is one bob-builder job of an output generator.
type Job struct {
	Name    string              // store name of the derivation
	Kind    string              // bob-builder subcommand
	Label   string              // package label for log lines
	Args    any                 // the kind's Args
	Drvs    map[string][]string // input derivation → outputs
	Srcs    []string            // input store paths (besides bob-builder)
	Discard bool                // unsafeDiscardReferences.out (R-OUT-011)
}

// Add finalises jb and records it; it returns the derivation's path and its "out" path.
func (j *Jobs) Add(jb Job) (drvPath, out string, err error) {
	in := NewInputs(j.req.Builder)
	for _, s := range jb.Srcs {
		in.AddSrc(s)
	}
	for p, outs := range jb.Drvs {
		in.AddDrv(p, outs...)
	}
	p, d, err := BuilderJob{Name: jb.Name, System: HostSystem(), Builder: j.req.Builder, Kind: jb.Kind, Label: jb.Label,
		Args: jb.Args, Inputs: in, Discard: jb.Discard}.Finalize(j)
	if err != nil {
		return "", "", err
	}
	if _, ok := j.own[p]; !ok {
		j.own[p] = d
		j.Drvs = append(j.Drvs, d)
	}
	return p, OutPath(d), nil
}
