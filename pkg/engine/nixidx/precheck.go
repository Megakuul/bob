package nixidx

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx/goname"
)

// shortRev is a nixpkgs revision in messages (12 hex digits, like bob lock's change lines).
func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// Need is one use of a nixpkgs package in the graph (an IR node with a "nix" object).
type Need struct {
	Attr          string
	Label         string   // "nixpkgs:<attr>"
	DefinedAt     diag.Pos // the bob.go position of the nixpkgs.Tool/Lib/Pkg call
	AllowUnfree   bool
	AllowInsecure []string // accepted knownVulnerabilities entries
}

// Check runs the pre-checks of SPEC R-NIX-011 that the index answers, before any derivation is
// built: attribute exists (1001, with up to 3 suggestions), broken (1002), insecure without
// AllowInsecure (1003), unfree without AllowUnfree (1004), unavailable on the system (1005). It
// returns the entries of every need and all findings joined (each a *diag.Error); an
// attribute whose evaluation aborts is BOB-E-1017.
func (i *Index) Check(ctx context.Context, needs []Need) (map[string]Entry, error) {
	attrs := make([]string, 0, len(needs))
	for _, n := range needs {
		attrs = append(attrs, n.Attr)
	}
	entries, lookupErr := i.Lookup(ctx, attrs)
	if entries == nil {
		return nil, lookupErr
	}
	var errs []error
	if lookupErr != nil {
		errs = append(errs, at(lookupErr, needs))
	}
	var names []string
	for _, n := range needs {
		e, ok := entries[n.Attr]
		if !ok {
			continue // reported by Lookup (1017)
		}
		var d *diag.Error
		pinned := testMissing(n.Attr)
		switch {
		case pinned || e.Missing && i.cfg.Rev != lock.DefaultRev && !strings.Contains(n.Attr, "."):
			// A typed handle of bob's surface (generated at lock.DefaultRev) that the pinned
			// revision lacks (R-NIX-032, ADR 0020 §3).
			pos := n.DefinedAt.File
			if n.DefinedAt.Line > 0 {
				pos += ":" + strconv.Itoa(n.DefinedAt.Line)
				if n.DefinedAt.Col > 0 {
					pos += ":" + strconv.Itoa(n.DefinedAt.Col)
				}
			}
			d = diag.New(1001, "variant", "pin", "pos", pos, "rev", shortRev(i.cfg.Rev), "attr", n.Attr,
				"name", goname.Of(n.Attr), "surface", "nixpkgs "+shortRev(lock.DefaultRev))
		case e.Missing:
			if names == nil {
				all, err := i.Names(ctx)
				if err != nil {
					return nil, err
				}
				names = all
			}
			args := []string{"attr", n.Attr}
			if s := Suggest(n.Attr, names); len(s) > 0 {
				args = append(args, "suggest", strings.Join(s, ", "))
			}
			d = diag.New(1001, args...)
		case e.Broken:
			d = diag.New(1002, "attr", n.Attr, "version", e.Version)
		case e.Insecure && !allowed(e.KnownVulnerabilities, n.AllowInsecure):
			cves := strings.Join(e.KnownVulnerabilities, ", ")
			if cves == "" {
				cves = "marked insecure by nixpkgs"
			}
			d = diag.New(1003, "attr", n.Attr, "version", e.Version, "cves", cves)
		case e.Unfree && !n.AllowUnfree && !i.cfg.AllowUnfree:
			d = diag.New(1004, "attr", n.Attr)
		case !e.Available:
			d = diag.New(1005, "attr", n.Attr, "system", i.cfg.System)
		}
		if d != nil {
			d.Label, d.DefinedAt = n.Label, n.DefinedAt
			errs = append(errs, d)
		}
	}
	return entries, errors.Join(errs...)
}

// at attaches the label and position of the need to 1017 errors of Lookup.
func at(err error, needs []Need) error {
	var out []error
	for _, e := range flatten(err) {
		var d *diag.Error
		if errors.As(e, &d) && d.Code == 1017 {
			for _, n := range needs {
				if n.Attr == d.Args["attr"] {
					d.Label, d.DefinedAt = n.Label, n.DefinedAt
					break
				}
			}
		}
		out = append(out, e)
	}
	return errors.Join(out...)
}

func flatten(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// allowed reports whether every known vulnerability is accepted (a package marked insecure
// without listed vulnerabilities is never accepted by entry).
func allowed(vulns, accepted []string) bool {
	if len(vulns) == 0 {
		return false
	}
	for _, v := range vulns {
		if !slices.Contains(accepted, v) {
			return false
		}
	}
	return true
}

// Suggest returns up to 3 top-level names close to attr (≤ 2 edits, or sharing a prefix of at
// least 3 characters with it), nearest first.
func Suggest(attr string, names []string) []string {
	top, _, _ := strings.Cut(attr, ".")
	type cand struct {
		name string
		dist int
	}
	var cs []cand
	for _, n := range names {
		if n == top {
			continue
		}
		d := distance(top, n, 2)
		prefix := len(top) >= 3 && len(n) >= 3 && (strings.HasPrefix(n, top) || strings.HasPrefix(top, n))
		if d <= 2 || prefix {
			cs = append(cs, cand{n, d})
		}
	}
	sort.Slice(cs, func(a, b int) bool {
		if cs[a].dist != cs[b].dist {
			return cs[a].dist < cs[b].dist
		}
		return cs[a].name < cs[b].name
	})
	var out []string
	for _, c := range cs {
		if len(out) == 3 {
			break
		}
		out = append(out, c.name)
	}
	return out
}

// distance is the Levenshtein distance of a and b, or limit+1 when it exceeds limit.
func distance(a, b string, limit int) int {
	if d := len(a) - len(b); d > limit || -d > limit {
		return limit + 1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
