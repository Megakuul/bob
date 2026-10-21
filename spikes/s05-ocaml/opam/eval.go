package opam

import (
	"fmt"
	"strings"
)

// Compare compares opam versions (Debian algorithm: alternating non-digit/digit runs,
// '~' sorts before everything, letters before non-letters).
func Compare(a, b string) int {
	order := func(c byte) int {
		switch {
		case c == '~':
			return -1
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			return int(c)
		default:
			return int(c) + 256
		}
	}
	isD := func(c byte) bool { return c >= '0' && c <= '9' }
	for a != "" || b != "" {
		// non-digit prefix
		i, j := 0, 0
		for i < len(a) && !isD(a[i]) {
			i++
		}
		for j < len(b) && !isD(b[j]) {
			j++
		}
		pa, pb := a[:i], b[:j]
		for k := 0; k < len(pa) || k < len(pb); k++ {
			var ca, cb int
			if k < len(pa) {
				ca = order(pa[k])
			}
			if k < len(pb) {
				cb = order(pb[k])
			}
			if ca != cb {
				if ca < cb {
					return -1
				}
				return 1
			}
		}
		a, b = a[i:], b[j:]
		i, j = 0, 0
		for i < len(a) && isD(a[i]) {
			i++
		}
		for j < len(b) && isD(b[j]) {
			j++
		}
		da := strings.TrimLeft(a[:i], "0")
		db := strings.TrimLeft(b[:j], "0")
		if len(da) != len(db) {
			if len(da) < len(db) {
				return -1
			}
			return 1
		}
		if da != db {
			if da < db {
				return -1
			}
			return 1
		}
		a, b = a[i:], b[j:]
	}
	return 0
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

// Env resolves filter variables. ok=false means undefined.
type Env func(name string) (val string, ok bool)

// Val is a filter value: string, bool, or undefined.
type Val struct {
	S   string
	Def bool
}

func (v Val) Bool() (bool, bool) {
	if !v.Def {
		return false, false
	}
	switch v.S {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// Eval evaluates a filter expression.
func Eval(v *V, env Env) Val {
	switch v.K {
	case Str:
		return Val{Interp(v.S, env), true}
	case Int, Bool:
		return Val{v.S, true}
	case Ident:
		s, ok := env(v.S)
		return Val{s, ok}
	case Group:
		if len(v.L) == 1 {
			return Eval(v.L[0], env)
		}
	case Not:
		if b, ok := Eval(v.L[0], env).Bool(); ok {
			return Val{fmt.Sprint(!b), true}
		}
		return Val{}
	case Def:
		return Val{fmt.Sprint(Eval(v.L[0], env).Def), true}
	case And, Or:
		a, aok := Eval(v.L[0], env).Bool()
		b, bok := Eval(v.L[1], env).Bool()
		if v.K == And {
			if aok && !a || bok && !b {
				return Val{"false", true}
			}
			if aok && bok {
				return Val{"true", true}
			}
		} else {
			if aok && a || bok && b {
				return Val{"true", true}
			}
			if aok && bok {
				return Val{"false", true}
			}
		}
		return Val{}
	case BinRel:
		a, b := Eval(v.L[0], env), Eval(v.L[1], env)
		if !a.Def || !b.Def {
			return Val{}
		}
		return Val{fmt.Sprint(relHolds(v.S, Compare(a.S, b.S))), true}
	}
	return Val{}
}

// Interp substitutes %{var}% and %{var?a:b}% in s.
func Interp(s string, env Env) string {
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
		if q := strings.Index(expr, "?"); q >= 0 {
			v, ok := env(expr[:q])
			alt := strings.SplitN(expr[q+1:], ":", 2)
			if len(alt) == 2 {
				if ok && v == "true" {
					b.WriteString(alt[0])
				} else {
					b.WriteString(alt[1])
				}
			}
			continue
		}
		if v, ok := env(expr); ok {
			b.WriteString(v)
		}
	}
}

// Atom is a dependency on one package with a version predicate.
type Atom struct {
	Name string
	Pred *V // simplified option formula over PreRel nodes, nil = any version
}

// Alt is a conjunction of atoms (one alternative of a disjunction).
type Alt []Atom

// Formula is a dependency formula after filters were applied:
// a conjunction (top-level items) of disjunctions of conjunctions.
type Formula [][]Alt

// Deps applies flag filters (build, with-test, ...) to a depends/conflicts/depopts value and
// returns it in CNF. Filtered-out atoms are dropped, like opam does.
func Deps(v *V, env Env) (Formula, error) {
	var f Formula
	for _, it := range Items(v) {
		alts, keep, err := disj(it, env)
		if err != nil {
			return nil, err
		}
		if keep {
			f = append(f, alts)
		}
	}
	return f, nil
}

// disj flattens one top-level item (an atom, an Or, or a group) to a disjunction.
func disj(v *V, env Env) ([]Alt, bool, error) {
	switch v.K {
	case Str:
		pred, keep := simplify(v.Opt, env)
		if !keep {
			return nil, false, nil
		}
		return []Alt{{{Name: v.S, Pred: pred}}}, true, nil
	case Or:
		a, ka, err := disj(v.L[0], env)
		if err != nil {
			return nil, false, err
		}
		b, kb, err := disj(v.L[1], env)
		if err != nil {
			return nil, false, err
		}
		return append(a, b...), ka || kb, nil
	case Group:
		if len(v.L) == 1 {
			return disj(v.L[0], env)
		}
	case And:
		a, ka, err := disj(v.L[0], env)
		if err != nil {
			return nil, false, err
		}
		b, kb, err := disj(v.L[1], env)
		if err != nil {
			return nil, false, err
		}
		if !ka {
			return b, kb, nil
		}
		if !kb {
			return a, ka, nil
		}
		var out []Alt
		for _, x := range a {
			for _, y := range b {
				out = append(out, append(append(Alt{}, x...), y...))
			}
		}
		return out, true, nil
	}
	return nil, false, fmt.Errorf("line %d: unsupported dependency formula", v.Line)
}

// simplify partially evaluates an option list: flag idents become booleans, PreRel stays.
// Returns (predicate or nil=any, keep=false if the filter is false).
func simplify(opts []*V, env Env) (*V, bool) {
	if len(opts) == 0 {
		return nil, true
	}
	var acc *V
	for _, o := range opts {
		s, c := simp(o, env)
		if c == cFalse {
			return nil, false
		}
		if c == cExpr {
			if acc == nil {
				acc = s
			} else {
				acc = &V{K: And, L: []*V{acc, s}}
			}
		}
	}
	return acc, true
}

const (
	cTrue = iota
	cFalse
	cExpr
)

func simp(v *V, env Env) (*V, int) {
	switch v.K {
	case PreRel:
		return v, cExpr
	case Group:
		if len(v.L) == 1 {
			return simp(v.L[0], env)
		}
	case And, Or:
		a, ca := simp(v.L[0], env)
		b, cb := simp(v.L[1], env)
		if v.K == And {
			if ca == cFalse || cb == cFalse {
				return nil, cFalse
			}
			if ca == cTrue {
				return b, cb
			}
			if cb == cTrue {
				return a, ca
			}
		} else {
			if ca == cTrue || cb == cTrue {
				return nil, cTrue
			}
			if ca == cFalse {
				return b, cb
			}
			if cb == cFalse {
				return a, ca
			}
		}
		return &V{K: v.K, L: []*V{a, b}}, cExpr
	}
	if b, ok := Eval(v, env).Bool(); ok && b {
		return nil, cTrue
	}
	return nil, cFalse // undefined filters count as false
}

// Match reports whether version ver satisfies pred (nil = any). env resolves variables used
// as operands (e.g. {= version}).
func Match(pred *V, ver string, env Env) bool {
	if pred == nil {
		return true
	}
	switch pred.K {
	case PreRel:
		x := Eval(pred.L[0], env)
		if !x.Def {
			return false
		}
		return relHolds(pred.S, Compare(ver, x.S))
	case And:
		return Match(pred.L[0], ver, env) && Match(pred.L[1], ver, env)
	case Or:
		return Match(pred.L[0], ver, env) || Match(pred.L[1], ver, env)
	case Group:
		if len(pred.L) == 1 {
			return Match(pred.L[0], ver, env)
		}
	}
	return false
}

// String renders a predicate for messages.
func (v *V) String() string {
	if v == nil {
		return "*"
	}
	switch v.K {
	case PreRel:
		return v.S + " " + v.L[0].String()
	case And:
		return v.L[0].String() + " & " + v.L[1].String()
	case Or:
		return "(" + v.L[0].String() + " | " + v.L[1].String() + ")"
	case Str:
		return fmt.Sprintf("%q", v.S)
	}
	return v.S
}
