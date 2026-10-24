package crates

import (
	"fmt"
	"strings"
)

// Platform describes one Rust target as cargo sees it for dependency filtering and build scripts.
type Platform struct {
	Triple string
	Cfg    map[string][]string // key -> values ("" value for name-only cfgs like unix)
}

// Built-in cfg tables for bob's supported systems (taken from `rustc --print cfg --target T`
// of rustc 1.95.0; a test must compare them with the pinned toolchain).
var Platforms = map[string]*Platform{
	"aarch64-unknown-linux-gnu": {Triple: "aarch64-unknown-linux-gnu", Cfg: map[string][]string{
		"debug_assertions": {}, "panic": {"unwind"}, "target_abi": {""}, "target_arch": {"aarch64"},
		"target_endian": {"little"}, "target_env": {"gnu"}, "target_family": {"unix"}, "target_feature": {"neon"},
		"target_has_atomic": {"128", "16", "32", "64", "8", "ptr"}, "target_os": {"linux"},
		"target_pointer_width": {"64"}, "target_vendor": {"unknown"}, "unix": {}}},
	"x86_64-unknown-linux-gnu": {Triple: "x86_64-unknown-linux-gnu", Cfg: map[string][]string{
		"debug_assertions": {}, "panic": {"unwind"}, "target_abi": {""}, "target_arch": {"x86_64"},
		"target_endian": {"little"}, "target_env": {"gnu"}, "target_family": {"unix"},
		"target_feature": {"fxsr", "sse", "sse2"}, "target_has_atomic": {"16", "32", "64", "8", "ptr"},
		"target_os": {"linux"}, "target_pointer_width": {"64"}, "target_vendor": {"unknown"}, "unix": {}}},
}

// MatchTarget evaluates a dependency's `target` field ("" = always, a triple, or cfg(...)).
func (p *Platform) MatchTarget(t string) (bool, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return true, nil
	}
	if !strings.HasPrefix(t, "cfg(") {
		return t == p.Triple, nil
	}
	toks, err := lexCfg(t)
	if err != nil {
		return false, err
	}
	ps := &cfgParser{toks: toks}
	if !ps.eat("cfg") || !ps.eat("(") {
		return false, fmt.Errorf("bad cfg %q", t)
	}
	v, err := ps.expr(p)
	if err != nil {
		return false, fmt.Errorf("bad cfg %q: %w", t, err)
	}
	if !ps.eat(")") || ps.i != len(ps.toks) {
		return false, fmt.Errorf("bad cfg %q", t)
	}
	return v, nil
}

func lexCfg(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
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
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t(),=\"", rune(s[j])) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out, nil
}

type cfgParser struct {
	toks []string
	i    int
}

func (p *cfgParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *cfgParser) eat(t string) bool {
	if p.peek() == t {
		p.i++
		return true
	}
	return false
}

func (p *cfgParser) list(pl *Platform) ([]bool, error) {
	if !p.eat("(") {
		return nil, fmt.Errorf("expected (")
	}
	var vs []bool
	for !p.eat(")") {
		v, err := p.expr(pl)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
		if !p.eat(",") && p.peek() != ")" {
			return nil, fmt.Errorf("expected , or )")
		}
	}
	return vs, nil
}

func (p *cfgParser) expr(pl *Platform) (bool, error) {
	t := p.peek()
	p.i++
	switch t {
	case "all", "any", "not":
		if p.peek() == "(" {
			vs, err := p.list(pl)
			if err != nil {
				return false, err
			}
			switch t {
			case "all":
				for _, v := range vs {
					if !v {
						return false, nil
					}
				}
				return true, nil
			case "any":
				for _, v := range vs {
					if v {
						return true, nil
					}
				}
				return false, nil
			default:
				if len(vs) != 1 {
					return false, fmt.Errorf("not() takes one argument")
				}
				return !vs[0], nil
			}
		}
	case "", "(", ")", ",", "=":
		return false, fmt.Errorf("unexpected %q", t)
	}
	vals, has := pl.Cfg[t]
	if p.eat("=") {
		lit := p.peek()
		p.i++
		if len(lit) < 2 || lit[0] != '"' {
			return false, fmt.Errorf("expected string")
		}
		lit = lit[1 : len(lit)-1]
		for _, v := range vals {
			if v == lit {
				return true, nil
			}
		}
		return false, nil
	}
	return has, nil
}
