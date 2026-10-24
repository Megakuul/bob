package bob

import (
	"fmt"
)

// definition is one bob.Define call: a registered, memoised constructor.
type definition struct {
	reg     *registry
	pos     Position // the Define call (the "var X = bob.Define(" line)
	dir     string   // label directory of that bob.go
	mod     string   // module of that bob.go ("" = main module)
	call    func(*P) any
	memo    map[*P]any
	created map[*P][]*node       // nodes the constructor body created itself, per P
	calls   map[*definition]bool // definitions its body called (any P)
}

// registry holds what bob.go files register while their packages initialise: definitions,
// repository settings, pipelines and errors found before evaluation. It is the only mutable
// package state, the designed memoisation of R-FW-011.
type registry struct {
	defs      []*definition
	early     []diagnostic // errors found before evaluation (always reported)
	repos     []repoDecl
	pipelines []pipelineDecl
}

var reg = &registry{}

// Define turns a constructor into a memoised, registered constructor. It must be called in a
// package-level variable declaration of a bob.go file. The returned function calls fn at most
// once per P and returns the same value on every later call with that P (R-FW-011).
// Every value reachable from fn's result that implements Package becomes part of the graph.
func Define[T any](fn func(p *P) T) func(p *P) T {
	file, line, caller := callerFunc()
	r := reg
	zero := func(*P) T {
		var z T
		return z
	}
	mod, posFile, dir, _ := env().locate(file)
	pos := Position{File: posFile, Line: line}
	if ev := active; ev != nil {
		ev.misuse(fmt.Sprintf("%s: bob.Define must be used in a package-level var declaration", pos))
		return zero
	}
	if !env().isVarInit(caller) || fn == nil {
		r.early = append(r.early, diagnostic{pos: pos, msg: fmt.Sprintf("%s: bob.Define must be used in a package-level var declaration", pos)})
		return zero
	}
	d := &definition{
		reg: r, pos: pos, dir: dir, mod: mod,
		call:    func(p *P) any { return fn(p) },
		memo:    map[*P]any{},
		created: map[*P][]*node{},
		calls:   map[*definition]bool{},
	}
	r.defs = append(r.defs, d)
	return func(p *P) T {
		v, _ := d.get(p).(T)
		return v
	}
}

// get returns the memoised value of d for p, calling the constructor the first time.
func (d *definition) get(p *P) any {
	ev := active
	if ev == nil || p == nil || p.ev != ev {
		s := bobSite()
		at := s.pos
		if !s.ok {
			at = d.pos
		}
		d.reg.early = append(d.reg.early, diagnostic{pos: at,
			msg: fmt.Sprintf("bob: %s: %s was called before evaluation; call it only inside another bob.Define function", at, varRef(d))})
		return nil
	}
	if top := ev.top(); top != nil {
		top.def.calls[d] = true
	}
	if v, ok := d.memo[p]; ok {
		return v
	}
	for i, f := range ev.stack {
		if f.def == d && f.p == p {
			ev.cycle(ev.stack[i:], p)
			return nil
		}
	}
	ev.stack = append(ev.stack, frame{def: d, p: p})
	v := d.protect(ev, p)
	ev.stack = ev.stack[:len(ev.stack)-1]
	d.memo[p] = v
	return v
}

// protect runs the constructor and turns a panic of user code into a definition error of d.
func (d *definition) protect(ev *evaluation, p *P) (v any) {
	defer func() {
		if r := recover(); r != nil {
			v = nil
			ev.errorf(d, "%s: %s panicked: %v", d.pos, varRef(d), r)
		}
	}()
	return d.call(p)
}

// varRef names d in messages: the variable it is assigned to when the source line can be read
// (the evaluator's working directory is the repository root), else "the constructor".
func varRef(d *definition) string {
	if name := varName(d); name != "" {
		return name
	}
	return "the constructor"
}
