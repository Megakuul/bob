package resolve

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Bounds for opam files read from the repository snapshot (hostile input, AGENTS.md §5.4).
const (
	maxFileBytes = 1 << 20 // the largest opam file in opam-repository is far below 100 KiB
	maxDepth     = 64      // nesting of lists, groups, options and operators
	maxValues    = 1 << 17 // values in one file
)

// ErrSyntax is returned (wrapped) for opam files that do not parse.
var ErrSyntax = errors.New("opam syntax error")

// Kind is the kind of a parsed opam value.
type Kind uint8

const (
	Str    Kind = iota // "text"
	Ident              // name, ocaml:version, _:lib
	Bool               // true, false
	Int                // 42, -1
	List               // [ a b ]
	Group              // ( a b )
	And                // a & b
	Or                 // a | b
	Not                // !a
	Def                // ?a
	PreRel             // >= "1.0": a constraint on the version of the enclosing package atom
	BinRel             // a >= b
	EnvOp              // FOO += "x"
)

// Value is one opam value. Opt holds a trailing { … } option list.
type Value struct {
	Kind Kind
	S    string   // Str/Ident text, Bool "true"/"false", Int digits, operator of PreRel/BinRel/EnvOp
	L    []*Value // List/Group items; And/Or/BinRel/EnvOp operands (2); Not/Def/PreRel operand (1)
	Opt  []*Value
	Line int
}

// File is a parsed opam file: top-level fields and sections ("url", "extra-source <name>").
type File struct {
	Fields   map[string]*Value
	Sections map[string]map[string]*Value
}

type tokKind uint8

const (
	tEOF tokKind = iota
	tStr
	tIdent
	tField
	tBool
	tInt
	tOp
	tPunct
)

type token struct {
	k    tokKind
	s    string
	line int
}

func isOp2(s string) bool {
	switch s {
	case "!=", "<=", ">=", "+=", "=+", ":=", "=:":
		return true
	}
	return false
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_-+:.~", c) >= 0
}

func syntaxErr(line int, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrSyntax, line, fmt.Sprintf(format, args...))
}

func lex(src string) ([]token, error) {
	var ts []token
	line := 1
	for i := 0; i < len(src); {
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
				return nil, syntaxErr(line, "unterminated comment")
			}
			line += strings.Count(src[i:i+2+j], "\n")
			i += j + 4
		case c == '"':
			s, n, lines, err := lexString(src[i:], line)
			if err != nil {
				return nil, err
			}
			ts = append(ts, token{tStr, s, line})
			line += lines
			i += n
		case strings.IndexByte("()[]{}", c) >= 0:
			ts = append(ts, token{tPunct, string(c), line})
			i++
		case i+1 < len(src) && isOp2(src[i:i+2]):
			ts = append(ts, token{tOp, src[i : i+2], line})
			i += 2
		case strings.IndexByte("=<>&|!?", c) >= 0:
			ts = append(ts, token{tOp, string(c), line})
			i++
		case isIdentByte(c):
			j := i
			for j < len(src) && isIdentByte(src[j]) {
				j++
			}
			w := src[i:j]
			i = j
			// "name:" (or "name :") starts a field; "pkg:var" stays an identifier.
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
				ts = append(ts, token{tField, strings.TrimSuffix(w, ":"), line})
			case w == "true" || w == "false":
				ts = append(ts, token{tBool, w, line})
			default:
				if _, err := strconv.Atoi(w); err == nil {
					ts = append(ts, token{tInt, w, line})
				} else {
					ts = append(ts, token{tIdent, w, line})
				}
			}
		default:
			return nil, syntaxErr(line, "unexpected character %q", c)
		}
		if len(ts) > maxValues {
			return nil, syntaxErr(line, "more than %d tokens", maxValues)
		}
	}
	return ts, nil
}

// lexString reads a "…" or """…""" string at the start of s; it returns the unescaped text,
// the bytes consumed and the newlines crossed.
func lexString(s string, line int) (string, int, int, error) {
	var b strings.Builder
	triple := strings.HasPrefix(s, `"""`)
	i := 1
	if triple {
		i = 3
	}
	lines := 0
	for {
		if i >= len(s) {
			return "", 0, 0, syntaxErr(line, "unterminated string")
		}
		if triple && strings.HasPrefix(s[i:], `"""`) {
			return b.String(), i + 3, lines, nil
		}
		if !triple && s[i] == '"' {
			return b.String(), i + 1, lines, nil
		}
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			n := s[i+1]
			i += 2
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case '\n':
				lines++
				for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
					i++
				}
			default:
				b.WriteByte(n)
			}
			continue
		}
		if c == '\n' {
			lines++
		}
		b.WriteByte(c)
		i++
	}
}

type parser struct {
	ts    []token
	i     int
	depth int
}

func (p *parser) peek() token {
	if p.i < len(p.ts) {
		return p.ts[p.i]
	}
	return token{k: tEOF}
}

func (p *parser) next() token { t := p.peek(); p.i++; return t }

func (p *parser) enter(line int) error {
	p.depth++
	if p.depth > maxDepth {
		return syntaxErr(line, "nesting deeper than %d", maxDepth)
	}
	return nil
}

// Parse reads an opam file (opam 2.x grammar).
func Parse(src []byte) (*File, error) {
	if len(src) > maxFileBytes {
		return nil, fmt.Errorf("%w: file larger than %d bytes", ErrSyntax, maxFileBytes)
	}
	ts, err := lex(string(src))
	if err != nil {
		return nil, err
	}
	p := &parser{ts: ts}
	f := &File{Fields: map[string]*Value{}, Sections: map[string]map[string]*Value{}}
	for p.peek().k != tEOF {
		t := p.next()
		switch t.k {
		case tField:
			v, err := p.or()
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", t.s, err)
			}
			f.Fields[t.s] = v
		case tIdent: // section: name ["label"] { fields }
			name := t.s
			if p.peek().k == tStr {
				name += " " + p.next().s
			}
			if b := p.next(); b.k != tPunct || b.s != "{" {
				return nil, syntaxErr(t.line, "expected { after section %s", name)
			}
			sec := map[string]*Value{}
			for p.peek().k == tField {
				ft := p.next()
				v, err := p.or()
				if err != nil {
					return nil, fmt.Errorf("section %s field %s: %w", name, ft.s, err)
				}
				sec[ft.s] = v
			}
			if b := p.next(); b.k != tPunct || b.s != "}" {
				return nil, syntaxErr(t.line, "expected } closing section %s", name)
			}
			f.Sections[name] = sec
		default:
			return nil, syntaxErr(t.line, "unexpected %q", t.s)
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

func (p *parser) isOp(s string) bool { t := p.peek(); return t.k == tOp && t.s == s }

func (p *parser) or() (*Value, error) {
	if err := p.enter(p.peek().line); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	a, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isOp("|") {
		t := p.next()
		b, err := p.and()
		if err != nil {
			return nil, err
		}
		a = &Value{Kind: Or, L: []*Value{a, b}, Line: t.line}
	}
	return a, nil
}

func (p *parser) and() (*Value, error) {
	a, err := p.rel()
	if err != nil {
		return nil, err
	}
	for p.isOp("&") {
		t := p.next()
		b, err := p.rel()
		if err != nil {
			return nil, err
		}
		a = &Value{Kind: And, L: []*Value{a, b}, Line: t.line}
	}
	return a, nil
}

func (p *parser) rel() (*Value, error) {
	if t := p.peek(); t.k == tOp && isRel(t.s) {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &Value{Kind: PreRel, S: t.s, L: []*Value{x}, Line: t.line}, nil
	}
	a, err := p.unary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.k != tOp {
		return a, nil
	}
	kind := BinRel
	switch {
	case isRel(t.s):
	case t.s == "+=" || t.s == "=+" || t.s == ":=" || t.s == "=:":
		kind = EnvOp
	default:
		return a, nil
	}
	p.next()
	b, err := p.unary()
	if err != nil {
		return nil, err
	}
	return &Value{Kind: kind, S: t.s, L: []*Value{a, b}, Line: t.line}, nil
}

func (p *parser) unary() (*Value, error) {
	t := p.peek()
	if err := p.enter(t.line); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	if t.k == tOp && (t.s == "!" || t.s == "?") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		k := Not
		if t.s == "?" {
			k = Def
		}
		return &Value{Kind: k, L: []*Value{x}, Line: t.line}, nil
	}
	v, err := p.atom()
	if err != nil {
		return nil, err
	}
	if b := p.peek(); b.k == tPunct && b.s == "{" {
		p.next()
		for {
			c := p.peek()
			if c.k == tPunct && c.s == "}" {
				p.next()
				break
			}
			if c.k == tEOF {
				return nil, syntaxErr(t.line, "unterminated {")
			}
			o, err := p.or()
			if err != nil {
				return nil, err
			}
			v.Opt = append(v.Opt, o)
		}
	}
	return v, nil
}

func (p *parser) atom() (*Value, error) {
	t := p.next()
	switch t.k {
	case tStr:
		return &Value{Kind: Str, S: t.s, Line: t.line}, nil
	case tIdent:
		return &Value{Kind: Ident, S: t.s, Line: t.line}, nil
	case tBool:
		return &Value{Kind: Bool, S: t.s, Line: t.line}, nil
	case tInt:
		return &Value{Kind: Int, S: t.s, Line: t.line}, nil
	case tPunct:
		if t.s != "[" && t.s != "(" {
			break
		}
		end, k := "]", List
		if t.s == "(" {
			end, k = ")", Group
		}
		v := &Value{Kind: k, Line: t.line}
		for {
			c := p.peek()
			if c.k == tPunct && c.s == end {
				p.next()
				return v, nil
			}
			if c.k == tEOF {
				return nil, syntaxErr(t.line, "unterminated %s", t.s)
			}
			x, err := p.or()
			if err != nil {
				return nil, err
			}
			v.L = append(v.L, x)
		}
	case tEOF:
		return nil, syntaxErr(t.line, "unexpected end of file")
	}
	return nil, syntaxErr(t.line, "unexpected %q", t.s)
}

// Items returns the elements of a list value; any other value is a one-item list.
func Items(v *Value) []*Value {
	if v == nil {
		return nil
	}
	if v.Kind == List {
		return v.L
	}
	return []*Value{v}
}

// StrOf returns the text of a Str value, or "".
func StrOf(v *Value) string {
	if v != nil && v.Kind == Str {
		return v.S
	}
	return ""
}
