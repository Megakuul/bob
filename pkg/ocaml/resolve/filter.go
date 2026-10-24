package resolve

import (
	"fmt"
	"strings"
)

// maxAlternatives bounds the disjunctive normal form of one dependency item; real opam
// files stay below 10 (hostile input otherwise grows exponentially).
const maxAlternatives = 256

// Env resolves filter variables; ok is false for undefined variables.
type Env func(name string) (val string, ok bool)

// fval is a filter value: a string (booleans are "true"/"false") or undefined.
type fval struct {
	s   string
	def bool
}

func (v fval) bool() (b, ok bool) {
	if !v.def {
		return false, false
	}
	switch v.s {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func boolVal(b bool) fval { return fval{fmt.Sprint(b), true} }

// EvalFilter evaluates a filter; ok is false when its value is undefined or not a boolean.
func EvalFilter(v *Value, env Env) (b, ok bool) { return eval(v, env).bool() }

// EvalString evaluates a filter to a string (for %{…}% operands and string fields).
func EvalString(v *Value, env Env) (string, bool) {
	r := eval(v, env)
	return r.s, r.def
}

func eval(v *Value, env Env) fval {
	switch v.Kind {
	case Str:
		return fval{Interpolate(v.S, env), true}
	case Int, Bool:
		return fval{v.S, true}
	case Ident:
		s, ok := env(v.S)
		return fval{s, ok}
	case Group:
		if len(v.L) == 1 {
			return eval(v.L[0], env)
		}
	case Not:
		if b, ok := eval(v.L[0], env).bool(); ok {
			return boolVal(!b)
		}
	case Def:
		return boolVal(eval(v.L[0], env).def)
	case And, Or:
		a, aok := eval(v.L[0], env).bool()
		b, bok := eval(v.L[1], env).bool()
		if v.Kind == And {
			if aok && !a || bok && !b {
				return boolVal(false)
			}
		} else if aok && a || bok && b {
			return boolVal(true)
		}
		if aok && bok {
			return boolVal(v.Kind == And)
		}
	case BinRel:
		a, b := eval(v.L[0], env), eval(v.L[1], env)
		if a.def && b.def {
			return boolVal(relHolds(v.S, CompareVersions(a.s, b.s)))
		}
	}
	return fval{}
}

func relHolds(op string, c int) bool {
	switch op {
	case "=":
		return c == 0
	case "!=":
		return c != 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

// Interpolate substitutes %{var}% and %{var?then:else}% in s; undefined variables expand to
// "" (or the else branch).
func Interpolate(s string, env Env) string {
	if !strings.Contains(s, "%{") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "%{")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i:], "}%")
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		expr := s[i+2 : i+j]
		s = s[i+j+2:]
		if name, alts, ok := strings.Cut(expr, "?"); ok {
			then, els, _ := strings.Cut(alts, ":")
			if v, ok := env(name); ok && v == "true" {
				b.WriteString(then)
			} else {
				b.WriteString(els)
			}
			continue
		}
		if v, ok := env(expr); ok {
			b.WriteString(v)
		}
	}
}

// Constraint is a version constraint with its operands evaluated. A leaf has Op set to a
// relation and Version; "&", "|" and "!" combine L. A leaf with Op "" never matches (its
// operand was undefined).
type Constraint struct {
	Op      string
	Version string
	L       []*Constraint
}

// Matches reports whether version v satisfies c; a nil constraint matches every version.
func (c *Constraint) Matches(v string) bool {
	if c == nil {
		return true
	}
	switch c.Op {
	case "&":
		return c.L[0].Matches(v) && c.L[1].Matches(v)
	case "|":
		return c.L[0].Matches(v) || c.L[1].Matches(v)
	case "!":
		return !c.L[0].Matches(v)
	case "":
		return false
	}
	return relHolds(c.Op, CompareVersions(v, c.Version))
}

// String renders c in opam syntax: >= "1.0" & < "2.0".
func (c *Constraint) String() string {
	if c == nil {
		return ""
	}
	switch c.Op {
	case "&":
		return c.L[0].String() + " & " + c.L[1].String()
	case "|":
		return "(" + c.L[0].String() + " | " + c.L[1].String() + ")"
	case "!":
		return "!(" + c.L[0].String() + ")"
	case "":
		return "(undefined)"
	}
	return fmt.Sprintf("%s %q", c.Op, c.Version)
}

// Atom is a dependency on one package with an optional version constraint.
type Atom struct {
	Name string
	C    *Constraint // nil: any version
}

func (a Atom) String() string {
	if a.C == nil {
		return a.Name
	}
	return a.Name + " " + a.C.String()
}

// Alt is a conjunction of atoms; Clause a disjunction of Alts; Formula a conjunction of
// Clauses (the conjunctive normal form of an opam package formula after its filters).
type (
	Alt     []Atom
	Clause  []Alt
	Formula []Clause
)

// Atoms returns every atom of f.
func (f Formula) Atoms() []Atom {
	var out []Atom
	for _, c := range f {
		for _, a := range c {
			out = append(out, a...)
		}
	}
	return out
}

// Deps evaluates the filters of a depends/depopts/conflicts value (flags such as build or
// with-test are variables of env) and returns it in CNF. Atoms whose filter is false or
// undefined are dropped, as opam does.
func Deps(v *Value, env Env) (Formula, error) {
	var f Formula
	for _, it := range Items(v) {
		alts, keep, err := dnf(it, env, 0)
		if err != nil {
			return nil, err
		}
		if keep {
			f = append(f, alts)
		}
	}
	return f, nil
}

// dnf flattens one top-level formula item to a disjunction of conjunctions; keep is false when
// every atom in it was filtered out.
func dnf(v *Value, env Env, depth int) (Clause, bool, error) {
	if depth > maxDepth {
		return nil, false, fmt.Errorf("%w: line %d: formula nested deeper than %d", ErrSyntax, v.Line, maxDepth)
	}
	switch v.Kind {
	case Str:
		c, keep := constraintOf(v.Opt, env)
		if !keep {
			return nil, false, nil
		}
		return Clause{{{Name: v.S, C: c}}}, true, nil
	case Group:
		if len(v.L) == 1 {
			return dnf(v.L[0], env, depth+1)
		}
		// (a b) inside a formula is a conjunction.
		var acc Clause
		kept := false
		for _, x := range v.L {
			c, k, err := dnf(x, env, depth+1)
			if err != nil {
				return nil, false, err
			}
			if !k {
				continue
			}
			if !kept {
				acc, kept = c, true
				continue
			}
			if acc, err = cross(acc, c, v.Line); err != nil {
				return nil, false, err
			}
		}
		return acc, kept, nil
	case Or:
		a, ka, err := dnf(v.L[0], env, depth+1)
		if err != nil {
			return nil, false, err
		}
		b, kb, err := dnf(v.L[1], env, depth+1)
		if err != nil {
			return nil, false, err
		}
		if len(a)+len(b) > maxAlternatives {
			return nil, false, fmt.Errorf("%w: line %d: more than %d alternatives", ErrSyntax, v.Line, maxAlternatives)
		}
		return append(append(Clause{}, a...), b...), ka || kb, nil
	case And:
		a, ka, err := dnf(v.L[0], env, depth+1)
		if err != nil {
			return nil, false, err
		}
		b, kb, err := dnf(v.L[1], env, depth+1)
		if err != nil {
			return nil, false, err
		}
		switch {
		case !ka:
			return b, kb, nil
		case !kb:
			return a, ka, nil
		}
		c, err := cross(a, b, v.Line)
		return c, err == nil, err
	}
	return nil, false, fmt.Errorf("%w: line %d: unsupported package formula", ErrSyntax, v.Line)
}

func cross(a, b Clause, line int) (Clause, error) {
	if len(a)*len(b) > maxAlternatives {
		return nil, fmt.Errorf("%w: line %d: more than %d alternatives", ErrSyntax, line, maxAlternatives)
	}
	var out Clause
	for _, x := range a {
		for _, y := range b {
			out = append(out, append(append(Alt{}, x...), y...))
		}
	}
	return out, nil
}

type tri uint8

const (
	triTrue tri = iota
	triFalse
	triExpr
)

// constraintOf partially evaluates an option list: filters become booleans, version
// constraints stay. keep is false when the filter part is false (or undefined).
func constraintOf(opts []*Value, env Env) (c *Constraint, keep bool) {
	for _, o := range opts {
		s, t := partial(o, env, 0)
		switch t {
		case triFalse:
			return nil, false
		case triExpr:
			if c == nil {
				c = s
			} else {
				c = &Constraint{Op: "&", L: []*Constraint{c, s}}
			}
		}
	}
	return c, true
}

func partial(v *Value, env Env, depth int) (*Constraint, tri) {
	if depth > maxDepth {
		return nil, triFalse
	}
	switch v.Kind {
	case PreRel:
		x := eval(v.L[0], env)
		if !x.def {
			return &Constraint{}, triExpr
		}
		return &Constraint{Op: v.S, Version: x.s}, triExpr
	case Group:
		if len(v.L) == 1 {
			return partial(v.L[0], env, depth+1)
		}
	case Not:
		c, t := partial(v.L[0], env, depth+1)
		switch t {
		case triTrue:
			return nil, triFalse
		case triFalse:
			return nil, triTrue
		}
		return &Constraint{Op: "!", L: []*Constraint{c}}, triExpr
	case And, Or:
		a, ta := partial(v.L[0], env, depth+1)
		b, tb := partial(v.L[1], env, depth+1)
		short, unit := triFalse, triTrue // And
		if v.Kind == Or {
			short, unit = triTrue, triFalse
		}
		switch {
		case ta == short || tb == short:
			return nil, short
		case ta == unit:
			return b, tb
		case tb == unit:
			return a, ta
		}
		op := "&"
		if v.Kind == Or {
			op = "|"
		}
		return &Constraint{Op: op, L: []*Constraint{a, b}}, triExpr
	}
	if b, ok := eval(v, env).bool(); ok && b {
		return nil, triTrue
	}
	return nil, triFalse // undefined filters count as false
}

// ParseConstraint parses a user version requirement such as ">= 5.7", "= 1.26.0" or
// `>= "1.0" & < "2.0"` (quotes optional); "" is no constraint.
func ParseConstraint(req string) (*Constraint, error) {
	req = strings.TrimSpace(req)
	if req == "" {
		return nil, nil
	}
	if len(req) > 256 {
		return nil, fmt.Errorf("%w: version requirement longer than 256 bytes", ErrSyntax)
	}
	f, err := Parse([]byte(`x: "x" {` + quoteVersions(req) + `}`))
	if err != nil {
		return nil, fmt.Errorf("version requirement %q: %w", req, err)
	}
	c, keep := constraintOf(f.Fields["x"].Opt, func(string) (string, bool) { return "", false })
	if !keep || c == nil || hasUndefined(c) {
		return nil, fmt.Errorf("%w: version requirement %q is not a version constraint", ErrSyntax, req)
	}
	return c, nil
}

// quoteVersions quotes bare versions after relational operators (">= 5.7" → `>= "5.7"`).
func quoteVersions(req string) string {
	var b strings.Builder
	for i := 0; i < len(req); {
		c := req[i]
		if c == '"' {
			j := strings.IndexByte(req[i+1:], '"')
			if j < 0 {
				b.WriteString(req[i:])
				break
			}
			b.WriteString(req[i : i+j+2])
			i += j + 2
			continue
		}
		if isIdentByte(c) && c != ':' {
			j := i
			for j < len(req) && isIdentByte(req[j]) {
				j++
			}
			b.WriteString(`"` + req[i:j] + `"`)
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func hasUndefined(c *Constraint) bool {
	if c.Op == "" {
		return true
	}
	for _, x := range c.L {
		if hasUndefined(x) {
			return true
		}
	}
	return false
}
