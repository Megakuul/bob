package bob

import (
	"fmt"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Slot is a typed forward declaration for values of packages that cannot be imported (the
// bob.go of a Go main package, R-FW-002 R3). An importable bob.go declares it, the leaf
// package's bob.go binds its constructor, every other package gets the bound value (R-FW-071,
// ADR 0020 §6):
//
//	// acme/bob.go (importable):        var API = bob.NewSlot[*golang.Binary]()
//	// acme/cmd/api/bob.go (main):      var _ = acme.API.Bind(Binary)
//	// acme/deploy/bob.go:              oci.ImageOpts{Program: acme.API.Get(p)}
type Slot[T any] struct {
	pos   Position // the NewSlot call
	fn    func(p *P) T
	bound Position // the Bind call
}

// NewSlot declares a slot; only in a package-level var declaration of a bob.go.
func NewSlot[T any]() *Slot[T] {
	file, line, caller := callerFunc()
	_, posFile, _, _ := env().locate(file)
	s := &Slot[T]{pos: Position{File: posFile, Line: line}}
	slotDecl(s.pos, caller, "bob.NewSlot")
	return s
}

// Bind binds a defined constructor (a bob.Define result) to s; only in a package-level var
// declaration of a bob.go. A slot is bound exactly once.
func (s *Slot[T]) Bind(fn func(p *P) T) struct{} {
	file, line, caller := callerFunc()
	_, posFile, _, _ := env().locate(file)
	pos := Position{File: posFile, Line: line}
	switch {
	case !slotDecl(pos, caller, "Bind"):
	case s == nil || fn == nil:
		reg.early = append(reg.early, diagnostic{pos: pos, msg: fmt.Sprintf("%s: Bind needs a slot made by bob.NewSlot and a constructor made by bob.Define", pos)})
	case s.fn != nil:
		reg.early = append(reg.early, slotError(pos, fmt.Sprintf("the slot declared at %s is already bound at %s", s.pos, s.bound),
			"bind a slot in exactly one bob.go: the one of the package that defines the value"))
	default:
		s.fn, s.bound = fn, pos
	}
	return struct{}{}
}

// Get returns the bound constructor's value for p (memoised like the constructor itself). Get
// of an unbound slot fails the evaluation and returns the zero value.
func (s *Slot[T]) Get(p *P) T {
	var zero T
	if s != nil && s.fn != nil {
		return s.fn(p)
	}
	at := bobSite()
	ref := "<pkg>.<Var>"
	declared := Position{}
	if s != nil {
		declared = s.pos
		if name, pkg := varAt(s.pos); name != "" && pkg != "" {
			ref = pkg + "." + name
		}
	}
	d := slotError(at.pos, fmt.Sprintf("the slot declared at %s is not bound; bind it with %s.Bind(<constructor>) in the bob.go of the package that defines the value", declared, ref),
		"a Go main package's bob.go binds its binary: var _ = "+ref+".Bind(Binary)")
	if ev := active; ev != nil {
		if top := ev.top(); top != nil {
			d.defs = []*definition{top.def}
		}
		ev.diags = append(ev.diags, d)
	} else {
		reg.early = append(reg.early, d)
	}
	return zero
}

// slotError is BOB-E-1020 for a slot (R-FW-071): "<pos>: invalid slot: <reason>".
func slotError(pos Position, reason, hint string) diagnostic {
	e := diag.New(1020, "label", pos.String(), "option", "slot", "reason", reason)
	return diagnostic{code: e.Code, pos: pos, msg: e.Error(), hint: hint}
}

// slotDecl reports whether the slot call at pos (by caller) is a package-level var
// initialisation of a bob.go, recording the R-FW-011 error otherwise.
func slotDecl(pos Position, caller, what string) bool {
	msg := fmt.Sprintf("%s: %s must be used in a package-level var declaration", pos, what)
	if ev := active; ev != nil {
		ev.misuse(msg)
		return false
	}
	if !env().isVarInit(caller) {
		reg.early = append(reg.early, diagnostic{pos: pos, msg: msg})
		return false
	}
	return true
}
