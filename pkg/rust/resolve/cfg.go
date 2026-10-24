package resolve

import (
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/rust/internal/cfgtab"
)

// CfgTable returns bob's built-in cfg set of a target triple as `rustc --print cfg` lines
// (rustc 1.95.0, package cfgtab).
func CfgTable(triple string) ([]string, error) {
	t, ok := cfgtab.Lookup(triple)
	if !ok {
		return nil, fmt.Errorf("no cfg table for target %q", triple)
	}
	return t, nil
}

// Platform is a target as Cargo sees it when filtering platform-specific dependencies.
type Platform struct {
	Triple string
	names  map[string]bool            // name-only cfgs (unix, debug_assertions)
	values map[string]map[string]bool // key -> values
}

// NewPlatform returns the platform of a triple with a built-in cfg table.
func NewPlatform(triple string) (*Platform, error) {
	t, err := CfgTable(triple)
	if err != nil {
		return nil, err
	}
	return PlatformFromCfg(triple, t)
}

// PlatformFromCfg builds a platform from `rustc --print cfg` lines.
func PlatformFromCfg(triple string, lines []string) (*Platform, error) {
	p := &Platform{Triple: triple, names: map[string]bool{}, values: map[string]map[string]bool{}}
	for _, l := range lines {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			p.names[l] = true
			continue
		}
		if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
			return nil, fmt.Errorf("bad cfg line %q", l)
		}
		if p.values[k] == nil {
			p.values[k] = map[string]bool{}
		}
		p.values[k][v[1:len(v)-1]] = true
	}
	return p, nil
}

// Matches evaluates a dependency's target: "" (every platform), a triple, or cfg(...).
func (p *Platform) Matches(target string) (bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return true, nil
	}
	if !strings.HasPrefix(target, "cfg(") {
		return target == p.Triple, nil
	}
	e, err := ParseCfg(target)
	if err != nil {
		return false, err
	}
	return e.eval(p), nil
}

// CfgPredicate is a parsed cfg(...) predicate.
type CfgPredicate struct {
	op    string // "all", "any", "not", "name", "kv"
	key   string
	value string
	args  []*CfgPredicate
}

// eval evaluates the predicate on p.
func (e *CfgPredicate) eval(p *Platform) bool {
	switch e.op {
	case "all":
		for _, a := range e.args {
			if !a.eval(p) {
				return false
			}
		}
		return true
	case "any":
		for _, a := range e.args {
			if a.eval(p) {
				return true
			}
		}
		return false
	case "not":
		return !e.args[0].eval(p)
	case "name":
		return p.names[e.key]
	}
	return p.values[e.key][e.value]
}

const maxCfgDepth = 64

// ParseCfg parses "cfg(<predicate>)" with Cargo's cfg grammar.
func ParseCfg(s string) (*CfgPredicate, error) {
	toks, err := lexCfg(s)
	if err != nil {
		return nil, fmt.Errorf("bad cfg %q: %w", s, err)
	}
	c := &cfgParser{toks: toks}
	if !c.eat("cfg") || !c.eat("(") {
		return nil, fmt.Errorf("bad cfg %q", s)
	}
	e, err := c.expr(0)
	if err != nil {
		return nil, fmt.Errorf("bad cfg %q: %w", s, err)
	}
	if !c.eat(")") || c.i != len(c.toks) {
		return nil, fmt.Errorf("bad cfg %q", s)
	}
	return e, nil
}

func lexCfg(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',' || c == '=':
			out = append(out, string(c))
			i++
		case c == '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, s[i:i+j+2])
			i += j + 2
		case isIdentByte(c):
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q", c)
		}
	}
	return out, nil
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

type cfgParser struct {
	toks []string
	i    int
}

func (c *cfgParser) eat(t string) bool {
	if c.i < len(c.toks) && c.toks[c.i] == t {
		c.i++
		return true
	}
	return false
}

func (c *cfgParser) expr(depth int) (*CfgPredicate, error) {
	if depth > maxCfgDepth {
		return nil, fmt.Errorf("nested too deeply")
	}
	if c.i >= len(c.toks) {
		return nil, fmt.Errorf("unexpected end")
	}
	id := c.toks[c.i]
	if !isIdentByte(id[0]) || id[0] >= '0' && id[0] <= '9' {
		return nil, fmt.Errorf("expected an identifier, got %q", id)
	}
	c.i++
	if (id == "all" || id == "any" || id == "not") && c.eat("(") {
		e := &CfgPredicate{op: id}
		for !c.eat(")") {
			a, err := c.expr(depth + 1)
			if err != nil {
				return nil, err
			}
			e.args = append(e.args, a)
			if !c.eat(",") {
				if !c.eat(")") {
					return nil, fmt.Errorf("expected , or )")
				}
				break
			}
		}
		if id == "not" && len(e.args) != 1 {
			return nil, fmt.Errorf("not() takes one predicate")
		}
		return e, nil
	}
	if c.eat("=") {
		if c.i >= len(c.toks) || !strings.HasPrefix(c.toks[c.i], `"`) {
			return nil, fmt.Errorf("expected a string after %s =", id)
		}
		v := c.toks[c.i]
		c.i++
		return &CfgPredicate{op: "kv", key: id, value: v[1 : len(v)-1]}, nil
	}
	return &CfgPredicate{op: "name", key: id}, nil
}
