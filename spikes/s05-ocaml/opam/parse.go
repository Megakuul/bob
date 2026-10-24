// Package opam is a spike-quality reader for opam files and an opam-repository resolver.
package opam

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind of a parsed opam value.
type Kind int

const (
	Str Kind = iota
	Ident
	Bool
	Int
	List  // [ a b ]
	Group // ( a b )
	And
	Or
	Not
	Def    // ?x
	PreRel // >= "1.0" (constraint on the enclosing package version)
	BinRel // a = b
	EnvOp  // FOO += "x"
)

// V is one opam value. Opt holds a trailing { ... } option list.
type V struct {
	K    Kind
	S    string // Str/Ident text, Bool "true"/"false", Int digits, operator for rels
	L    []*V   // List/Group items, And/Or/BinRel operands (2), Not/Def/PreRel operand (1)
	Opt  []*V
	Line int
}

// File is a parsed opam file: top-level fields and named sections (url, extra-source).
type File struct {
	Fields   map[string]*V
	Sections map[string]map[string]*V // "url" or "extra-source name" -> fields
}

type tok struct {
	k    string // "str" "id" "field" "int" "bool" "op" "(" ")" "[" "]" "{" "}"
	s    string
	line int
}

func lex(src string) ([]tok, error) {
	var ts []tok
	line := 1
	i := 0
	isId := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_-+:.~", c) >= 0
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], "(*"):
			j := strings.Index(src[i+2:], "*)")
			if j < 0 {
				return nil, fmt.Errorf("line %d: unterminated comment", line)
			}
			line += strings.Count(src[i:i+2+j], "\n")
			i += j + 4
		case c == '"':
			l0 := line
			var b strings.Builder
			triple := strings.HasPrefix(src[i:], `"""`)
			if triple {
				i += 3
			} else {
				i++
			}
			for {
				if i >= len(src) {
					return nil, fmt.Errorf("line %d: unterminated string", l0)
				}
				if triple && strings.HasPrefix(src[i:], `"""`) {
					i += 3
					break
				}
				if !triple && src[i] == '"' {
					i++
					break
				}
				if src[i] == '\\' && i+1 < len(src) {
					n := src[i+1]
					switch n {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case '\n':
						line++
						i += 2
						for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
							i++
						}
						continue
					default:
						b.WriteByte(n)
					}
					i += 2
					continue
				}
				if src[i] == '\n' {
					line++
				}
				b.WriteByte(src[i])
				i++
			}
			ts = append(ts, tok{"str", b.String(), l0})
		case strings.IndexByte("()[]{}", c) >= 0:
			ts = append(ts, tok{string(c), "", line})
			i++
		case strings.HasPrefix(src[i:], "!="), strings.HasPrefix(src[i:], "<="), strings.HasPrefix(src[i:], ">="),
			strings.HasPrefix(src[i:], "+="), strings.HasPrefix(src[i:], "=+"), strings.HasPrefix(src[i:], ":="),
			strings.HasPrefix(src[i:], "=:"):
			ts = append(ts, tok{"op", src[i : i+2], line})
			i += 2
		case strings.IndexByte("=<>&|!?", c) >= 0:
			ts = append(ts, tok{"op", string(c), line})
			i++
		case isId(c):
			j := i
			for j < len(src) && isId(src[j]) {
				j++
			}
			w := src[i:j]
			i = j
			if !strings.HasSuffix(w, ":") {
				k := i
				for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
					k++
				}
				if k > i && k < len(src) && src[k] == ':' && (k+1 >= len(src) || src[k+1] != '=') {
					w += ":"
					i = k + 1
				}
			}
			switch {
			case strings.HasSuffix(w, ":"):
				ts = append(ts, tok{"field", strings.TrimSuffix(w, ":"), line})
			case w == "true" || w == "false":
				ts = append(ts, tok{"bool", w, line})
			default:
				if _, err := strconv.Atoi(w); err == nil {
					ts = append(ts, tok{"int", w, line})
				} else {
					ts = append(ts, tok{"id", w, line})
				}
			}
		default:
			return nil, fmt.Errorf("line %d: unexpected %q", line, c)
		}
	}
	return ts, nil
}

type parser struct {
	ts []tok
	i  int
}

func (p *parser) peek() tok {
	if p.i < len(p.ts) {
		return p.ts[p.i]
	}
	return tok{k: "eof"}
}
func (p *parser) next() tok { t := p.peek(); p.i++; return t }

// Parse reads an opam file.
func Parse(src string) (*File, error) {
	ts, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{ts: ts}
	f := &File{Fields: map[string]*V{}, Sections: map[string]map[string]*V{}}
	for p.peek().k != "eof" {
		t := p.next()
		switch t.k {
		case "field":
			v, err := p.or()
			if err != nil {
				return nil, fmt.Errorf("field %s: %v", t.s, err)
			}
			f.Fields[t.s] = v
		case "id": // section: name ["label"] { fields }
			name := t.s
			if p.peek().k == "str" {
				name += " " + p.next().s
			}
			if p.next().k != "{" {
				return nil, fmt.Errorf("line %d: expected { after section %s", t.line, name)
			}
			sec := map[string]*V{}
			for p.peek().k == "field" {
				ft := p.next()
				v, err := p.or()
				if err != nil {
					return nil, err
				}
				sec[ft.s] = v
			}
			if p.next().k != "}" {
				return nil, fmt.Errorf("line %d: expected } closing section %s", t.line, name)
			}
			f.Sections[name] = sec
		default:
			return nil, fmt.Errorf("line %d: unexpected token %s %q", t.line, t.k, t.s)
		}
	}
	return f, nil
}

func isRel(s string) bool {
	switch s {
	case "=", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

func (p *parser) or() (*V, error) {
	a, err := p.and()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.k == "op" && t.s == "|"; t = p.peek() {
		p.next()
		b, err := p.and()
		if err != nil {
			return nil, err
		}
		a = &V{K: Or, L: []*V{a, b}, Line: t.line}
	}
	return a, nil
}

func (p *parser) and() (*V, error) {
	a, err := p.rel()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.k == "op" && t.s == "&"; t = p.peek() {
		p.next()
		b, err := p.rel()
		if err != nil {
			return nil, err
		}
		a = &V{K: And, L: []*V{a, b}, Line: t.line}
	}
	return a, nil
}

func (p *parser) rel() (*V, error) {
	if t := p.peek(); t.k == "op" && isRel(t.s) {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &V{K: PreRel, S: t.s, L: []*V{x}, Line: t.line}, nil
	}
	a, err := p.unary()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.k == "op" && isRel(t.s) {
		p.next()
		b, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &V{K: BinRel, S: t.s, L: []*V{a, b}, Line: t.line}, nil
	}
	if t := p.peek(); t.k == "op" && (t.s == "+=" || t.s == "=+" || t.s == ":=" || t.s == "=:") {
		p.next()
		b, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &V{K: EnvOp, S: t.s, L: []*V{a, b}, Line: t.line}, nil
	}
	return a, nil
}

func (p *parser) unary() (*V, error) {
	t := p.peek()
	if t.k == "op" && (t.s == "!" || t.s == "?") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		k := Not
		if t.s == "?" {
			k = Def
		}
		return &V{K: k, L: []*V{x}, Line: t.line}, nil
	}
	v, err := p.atom()
	if err != nil {
		return nil, err
	}
	if p.peek().k == "{" {
		p.next()
		for p.peek().k != "}" {
			if p.peek().k == "eof" {
				return nil, fmt.Errorf("line %d: unterminated {", t.line)
			}
			o, err := p.or()
			if err != nil {
				return nil, err
			}
			v.Opt = append(v.Opt, o)
		}
		p.next()
	}
	return v, nil
}

func (p *parser) atom() (*V, error) {
	t := p.next()
	switch t.k {
	case "str":
		return &V{K: Str, S: t.s, Line: t.line}, nil
	case "id":
		return &V{K: Ident, S: t.s, Line: t.line}, nil
	case "bool":
		return &V{K: Bool, S: t.s, Line: t.line}, nil
	case "int":
		return &V{K: Int, S: t.s, Line: t.line}, nil
	case "[", "(":
		end, k := "]", List
		if t.k == "(" {
			end, k = ")", Group
		}
		v := &V{K: k, Line: t.line}
		for p.peek().k != end {
			if p.peek().k == "eof" {
				return nil, fmt.Errorf("line %d: unterminated %s", t.line, t.k)
			}
			x, err := p.or()
			if err != nil {
				return nil, err
			}
			v.L = append(v.L, x)
		}
		p.next()
		return v, nil
	}
	return nil, fmt.Errorf("line %d: unexpected %s %q", t.line, t.k, t.s)
}

// Items returns the elements of a list value (a single non-list value is a one-item list).
func Items(v *V) []*V {
	if v == nil {
		return nil
	}
	if v.K == List {
		return v.L
	}
	return []*V{v}
}

// StrOf returns the string of a Str value or "".
func StrOf(v *V) string {
	if v != nil && v.K == Str {
		return v.S
	}
	return ""
}
