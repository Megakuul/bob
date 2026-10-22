package resolve

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoChecksum: a chosen package's source has no sha256/sha512 checksum (SPEC §03.5.2).
var ErrNoChecksum = errors.New("opam package has no sha256/sha512 checksum")

// ChecksumError names the package whose source cannot be pinned.
type ChecksumError struct{ Name, Version string }

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("opam:%s.%s has no sha256/sha512 checksum; bob cannot pin it", e.Name, e.Version)
}

// Unwrap makes errors.Is(err, ErrNoChecksum) hold.
func (e *ChecksumError) Unwrap() error { return ErrNoChecksum }

const maxURLLen = 4096

// Locked is one resolved package: the record bob.lock stores (lock.OpamPackage plus the name).
type Locked struct {
	Name    string
	Version string
	URL     string   // source archive; "" for packages without a source (conf-*, virtual)
	Hash    string   // SRI sha512 or sha256 of the archive (flat), "" without a source
	Opam    string   // sha256 hex of the opam file in the snapshot
	Deps    []string // depends + depopts present in the solution, minus provided packages; sorted
}

// Solution is the result of Solve.
type Solution struct {
	Packages []Locked // sorted by name; provided packages are not included
	Steps    int      // search nodes and probes
	Optimal  bool     // false when the search budget ended before optimality was proven
}

func (s *solver) solution() (*Solution, error) {
	out := &Solution{Steps: s.steps, Optimal: s.steps < s.max}
	in := map[string]bool{}
	for n, p := range s.pkgs {
		if s.best[n] >= 0 && !p.provided {
			in[p.name] = true
		}
	}
	for n, p := range s.pkgs {
		if s.best[n] < 0 || p.provided {
			continue
		}
		v := p.vers[s.best[n]]
		l := Locked{Name: p.name, Version: v.version, Opam: v.src.Sum}
		var err error
		if l.URL, l.Hash, err = source(v.src); err != nil {
			return nil, err
		}
		l.Deps, err = buildDeps(v, in)
		if err != nil {
			return nil, err
		}
		out.Packages = append(out.Packages, l)
	}
	slices.SortFunc(out.Packages, func(a, b Locked) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// buildDeps returns the build-order edges of v: its depends without post-only atoms (post
// dependencies are installed after the package, not before) plus its depopts that are in the
// solution.
func buildDeps(v *ver, in map[string]bool) ([]string, error) {
	noPost := func(name string) (string, bool) {
		if name == "post" {
			return "false", true
		}
		return v.env(name)
	}
	var names []string
	for _, field := range []string{"depends", "depopts"} {
		f, err := Deps(v.src.File.Fields[field], noPost)
		if err != nil {
			return nil, fmt.Errorf("opam:%s.%s %s: %w", v.src.Name, v.version, field, err)
		}
		for _, a := range f.Atoms() {
			if in[a.Name] && !slices.Contains(names, a.Name) {
				names = append(names, a.Name)
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

// source returns the archive URL and its SRI hash (sha512 preferred, sha256 accepted).
func source(v *Version) (url, hash string, err error) {
	sec := v.File.Sections["url"]
	if sec == nil {
		return "", "", nil
	}
	for _, k := range []string{"src", "archive", "http"} {
		if url = StrOf(sec[k]); url != "" {
			break
		}
	}
	if url == "" {
		return "", "", nil
	}
	if len(url) > maxURLLen || !(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
		return "", "", &ChecksumError{Name: v.Name, Version: v.Version}
	}
	sums := map[string]string{}
	for _, it := range Items(sec["checksum"]) {
		if algo, sum, ok := strings.Cut(StrOf(it), "="); ok {
			sums[algo] = sum
		}
	}
	for _, alg := range []struct {
		name string
		size int
	}{{"sha512", 64}, {"sha256", 32}} {
		raw, err := hex.DecodeString(sums[alg.name])
		if err == nil && len(raw) == alg.size {
			return url, alg.name + "-" + base64.StdEncoding.EncodeToString(raw), nil
		}
	}
	return "", "", &ChecksumError{Name: v.Name, Version: v.Version}
}

func clauseString(c Clause) string {
	alts := make([]string, len(c))
	for k, alt := range c {
		atoms := make([]string, len(alt))
		for j, a := range alt {
			atoms[j] = a.String()
		}
		alts[k] = strings.Join(atoms, " & ")
	}
	return strings.Join(alts, " | ")
}

// maxExplain bounds how deep an explanation follows dead dependencies.
const maxExplain = 32

// explainRoot says why no version of a requested package can be installed.
func (s *solver) explainRoot(a Atom) string {
	n, ok := s.index[a.Name]
	if !ok || len(s.pkgs[n].vers) == 0 {
		return fmt.Sprintf("%s is not in the opam repository", a.Name)
	}
	p := s.pkgs[n]
	if p.provided {
		return fmt.Sprintf("the request needs %s, but %s is fixed to %s by the toolchain", a, p.name, p.vers[0].version)
	}
	for i, v := range p.vers {
		if a.C.Matches(v.version) {
			return s.explainDead(p, i, 0)
		}
	}
	return fmt.Sprintf("no version of %s matches %s", a.Name, a.C)
}

// explainDead says why version i of p can never be installed: the first dependency without an
// installable version, followed to its root cause.
func (s *solver) explainDead(p *pkg, i, depth int) string {
	v := p.vers[i]
	id := p.name + "." + v.version
	switch {
	case !v.avail:
		return id + ": " + v.why
	case v.deadAt < 0:
		return id + " conflicts with the toolchain"
	}
	c := v.deps[v.deadAt]
	msg := id + " needs " + clauseString(c.src) + ": "
	if t := s.toolchainAtom(c); t != nil {
		return msg + s.fixed(t)
	}
	msg += "no available version matches"
	if cause := s.rootCause(c, depth+1); cause != "" {
		msg += " (" + cause + ")"
	}
	return msg
}

// toolchainAtom returns the atom of a single-atom clause on a provided package.
func (s *solver) toolchainAtom(c cclause) *catom {
	if len(c.alts) == 1 && len(c.alts[0]) == 1 && c.alts[0][0].n >= 0 && s.pkgs[c.alts[0][0].n].provided {
		return &c.alts[0][0]
	}
	return nil
}

func (s *solver) fixed(a *catom) string {
	p := s.pkgs[a.n]
	return fmt.Sprintf("%s is fixed to %s by the toolchain", p.name, p.vers[0].version)
}

// rootCause follows the first dead atom of c through the newest matching versions until it
// reaches the toolchain, an unavailable version or a missing package.
func (s *solver) rootCause(c cclause, depth int) string {
	if depth > maxExplain {
		return ""
	}
	for _, alt := range c.alts {
		for _, a := range alt {
			if a.n < 0 {
				return a.src.Name + " is not in the opam repository"
			}
			t := s.pkgs[a.n]
			if a.set.intersects(t.live) {
				continue
			}
			if t.provided {
				return s.fixed(&a)
			}
			j := a.set.first()
			if j < 0 {
				if len(t.vers) == 0 {
					return t.name + " is not in the opam repository"
				}
				return fmt.Sprintf("no version of %s matches %s", t.name, a.src.C)
			}
			v := t.vers[j]
			switch {
			case !v.avail:
				return t.name + "." + v.version + ": " + v.why
			case v.deadAt < 0:
				return t.name + "." + v.version + " conflicts with the toolchain"
			}
			if tc := s.toolchainAtom(v.deps[v.deadAt]); tc != nil {
				return s.fixed(tc)
			}
			return s.rootCause(v.deps[v.deadAt], depth+1)
		}
	}
	return ""
}
