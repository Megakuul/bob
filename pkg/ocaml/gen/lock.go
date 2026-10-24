package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
	"github.com/megakuul/bob/pkg/ocaml/resolve"
)

// Toolchain versions of the nixpkgs pin (SPEC §03.5.2): the resolver fixes ocaml, dune and
// ocamlfind at them; the planner refuses a toolchain of other versions (an OCaml version other
// than the pin's is deferred in v1).
const (
	OCamlVersion   = "5.4.1"
	DuneVersion    = "3.21.1"
	FindlibVersion = "1.9.8"
)

// opamArch is opam's arch variable of the host.
func opamArch() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return runtime.GOARCH
}

// lockInputs is the declaration hash of the opam section: the repository's opam requirements.
func lockInputs(reqs []request) (string, error) {
	if reqs == nil {
		reqs = []request{}
	}
	return lock.Inputs(reqs)
}

// decodeSection reads an opam section (nil when absent or unreadable).
func decodeSection(raw json.RawMessage) *lock.OpamSection {
	if len(raw) == 0 {
		return nil
	}
	v, err := lock.DecodeSection(lock.SectionOpam, raw)
	if err != nil {
		return nil
	}
	return v.(*lock.OpamSection)
}

// Lock resolves the opam requirements of every OCaml package (bob lock) against the pinned
// opam-repository snapshot: the snapshot is imported by the fetcher (narHash), its opam files
// are read from an extracted copy in the user cache, and the solution is recorded with each
// package's source URL and checksum. A section whose inputs are unchanged is returned as it
// is (R-FW-062). bob lock --update opam-repository moves the snapshot to BOB_OPAM_REPOSITORY's
// commit or keeps bob.lock's.
func (Ecosystem) Lock(ctx context.Context, req engine.LockRequest) (json.RawMessage, error) {
	ts, err := targets(req.Graph)
	if err != nil {
		return nil, err
	}
	reqs, first := requests(ts)
	inputs, err := lockInputs(reqs)
	if err != nil {
		return nil, err
	}
	prev := decodeSection(req.Prev)
	if prev != nil && prev.Inputs == inputs && len(req.Update) == 0 {
		return lock.EncodeSection(lock.SectionOpam, prev)
	}
	commit := ""
	if prev != nil && !slices.Contains(req.Update, "opam-repository") {
		commit = prev.Repository.Commit
	}
	snap, err := snapshotFor(commit)
	if err != nil {
		return nil, err
	}
	sec := &lock.OpamSection{Inputs: inputs, Repository: lock.OpamRepository{URL: RepositoryURL, Commit: snap.commit},
		Packages: map[string]lock.OpamPackage{}}
	if prev != nil && prev.Repository.Commit == snap.commit && prev.Repository.NarHash != "" {
		sec.Repository.NarHash = prev.Repository.NarHash
	} else {
		if req.Fetch == nil {
			return nil, errors.New("opam: no fetcher to pin the opam repository")
		}
		_, narHash, err := req.Fetch.Resolve(ctx, snap.request(""))
		if err != nil {
			return nil, err
		}
		sec.Repository.NarHash = narHash
	}
	var rs []resolve.Request
	for _, r := range reqs {
		if _, err := resolve.ParseConstraint(r.Req); err != nil {
			n := first[r]
			return nil, engine.Invalid(n, "Deps", fmt.Sprintf("opam %s: %q is not an opam version formula (for example \">= 5.7\")", r.Name, r.Req))
		}
		rs = append(rs, resolve.Request{Name: r.Name, Constraint: r.Req})
	}
	if len(rs) == 0 {
		return lock.EncodeSection(lock.SectionOpam, sec)
	}
	dir, err := opamFiles(ctx, snap)
	if err != nil {
		return nil, err
	}
	sol, err := resolve.Solve(ctx, resolve.NewRepository(os.DirFS(dir)),
		resolve.Options{Arch: opamArch(), Provided: resolve.Toolchain(OCamlVersion, DuneVersion, FindlibVersion)}, rs)
	if err != nil {
		return nil, resolveError(ts, reqs, first, err)
	}
	mapped := confMapped(ts)
	for _, p := range sol.Packages {
		if strings.HasPrefix(p.Name, "conf-") && !mapped[p.Name] {
			return nil, diag.New(1023, "package", "opam:"+p.Name+"."+p.Version,
				"reason", fmt.Sprintf("no nixpkgs mapping for this system package (add ocaml.Conf(%q, nixpkgs.Lib(p, \"<attr>\")) or extend bob's table)", p.Name))
		}
		sec.Packages[p.Name] = lock.OpamPackage{Version: p.Version, URL: p.URL, Hash: p.Hash, Opam: p.Opam, Deps: p.Deps}
	}
	return lock.EncodeSection(lock.SectionOpam, sec)
}

// confMapped is the set of conf-* packages with a nixpkgs mapping (bob's table and every
// ocaml.Conf of the graph).
func confMapped(ts []*target) map[string]bool {
	m := map[string]bool{}
	for _, e := range mlspec.ConfTable {
		m[e.Opam] = true
	}
	for _, t := range ts {
		for _, c := range t.spec.Conf {
			m[c.Opam] = true
		}
	}
	return m
}

// resolveError maps the resolver's errors to bob diagnostics: E-RESOLVE (BOB-E-1015) with the
// conflicting constraint chain, BOB-E-1023 for a source bob cannot pin.
func resolveError(ts []*target, reqs []request, first map[request]*ir.Node, err error) error {
	var unsat *resolve.UnsatError
	var sum *resolve.ChecksumError
	switch {
	case errors.As(err, &unsat):
		root, at := "the OCaml packages", ir.Pos{}
		if n := culprit(unsat.Reason, reqs, first, ts); n != nil {
			root, at = n.Label, n.DefinedAt
		}
		e := diag.New(1015, "ecosystem", "opam", "root", root, "reason", unsat.Reason, "label", root)
		e.DefinedAt = diag.Pos{File: at.File, Line: at.Line}
		return e
	case errors.As(err, &sum):
		return diag.New(1023, "package", "opam:"+sum.Name+"."+sum.Version, "reason", "it has no sha256/sha512 checksum")
	}
	return fmt.Errorf("resolving opam packages: %w", err)
}

// culprit is the node that declared the requirement an unsat explanation starts with, else the
// first OCaml node.
func culprit(reason string, reqs []request, first map[request]*ir.Node, ts []*target) *ir.Node {
	for _, r := range reqs {
		if strings.HasPrefix(reason, r.Name+".") || strings.HasPrefix(reason, r.Name+" ") || strings.Contains(reason, "needs "+r.Name) {
			return first[r]
		}
	}
	if len(ts) > 0 {
		return ts[0].n
	}
	return nil
}

// Stale reports whether the opam section still pins every requirement of the graph (R-FW-062).
// Equal inputs mean fresh; otherwise every declared package must be locked at a version its
// formula accepts (a build of part of the repository sees only part of the declarations).
func (Ecosystem) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	ts, err := targets(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	reqs, first := requests(ts)
	inputs, err := lockInputs(reqs)
	if err != nil {
		return "", ir.Pos{}, err
	}
	v, err := lock.DecodeSection(lock.SectionOpam, req.Prev)
	if err != nil {
		return "the opam section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	sec := v.(*lock.OpamSection)
	if sec.Inputs == inputs {
		return "", ir.Pos{}, nil
	}
	for _, r := range reqs {
		n := first[r]
		p, ok := sec.Packages[r.Name]
		if !ok {
			return fmt.Sprintf("opam package %s of %s is not locked", r.Name, n.Label), n.DefinedAt, nil
		}
		c, err := resolve.ParseConstraint(r.Req)
		if err != nil || !c.Matches(p.Version) {
			return fmt.Sprintf("opam %s %s of %s is not satisfied by the locked %s", r.Name, r.Req, n.Label, p.Version), n.DefinedAt, nil
		}
	}
	return "", ir.Pos{}, nil
}
