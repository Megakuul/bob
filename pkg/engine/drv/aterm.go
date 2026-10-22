package drv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxATerm bounds what Parse accepts; the largest nixpkgs derivations are a few MiB.
const maxATerm = 64 << 20

// ATerm returns the canonical ATerm of d (sorted, escaped; output paths as set).
func (d *Derivation) ATerm() ([]byte, error) {
	return d.aterm(nil)
}

// aterm prints d; with inputs set, it replaces the input derivations (hashDerivationModulo).
func (d *Derivation) aterm(inputs []InputDrv) ([]byte, error) {
	env, err := d.atermEnv()
	if err != nil {
		return nil, err
	}
	if inputs == nil {
		inputs = d.InputDrvs
	}
	var b bytes.Buffer
	b.WriteString("Derive([")
	outs := append([]Output(nil), d.Outputs...)
	sort.Slice(outs, func(i, j int) bool { return outs[i].Name < outs[j].Name })
	for i, o := range outs {
		if i > 0 && outs[i-1].Name == o.Name {
			return nil, fmt.Errorf("derivation %s declares output %q twice", d.Name, o.Name)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		writeStrings(&b, o.Name, o.Path, o.Algo, o.Hash)
		b.WriteByte(')')
	}
	b.WriteString("],[")
	for i, in := range mergeInputs(inputs) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		writeString(&b, in.Path)
		b.WriteString(",[")
		writeStrings(&b, in.Outputs...)
		b.WriteString("])")
	}
	b.WriteString("],[")
	writeStrings(&b, sortedSet(d.InputSrcs)...)
	b.WriteString("],")
	writeStrings(&b, d.System, d.Builder)
	b.WriteString(",[")
	writeStrings(&b, d.Args...)
	b.WriteString("],[")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		writeStrings(&b, k, env[k])
		b.WriteByte(')')
	}
	b.WriteString("])")
	return b.Bytes(), nil
}

// atermEnv is the environment as written into the ATerm: Env, plus `__json` for structured
// attributes.
func (d *Derivation) atermEnv() (map[string]string, error) {
	if d.Structured == nil {
		return d.Env, nil
	}
	if _, ok := d.Env["__json"]; ok {
		return nil, fmt.Errorf("derivation %s has both Structured and an __json variable", d.Name)
	}
	js, err := canonicalJSON(d.Structured)
	if err != nil {
		return nil, fmt.Errorf("derivation %s: structured attributes: %w", d.Name, err)
	}
	env := make(map[string]string, len(d.Env)+1)
	for k, v := range d.Env {
		env[k] = v
	}
	env["__json"] = string(js)
	return env, nil
}

// mergeInputs sorts input derivations by path and merges duplicate paths and output names.
func mergeInputs(in []InputDrv) []InputDrv {
	by := map[string][]string{}
	for _, i := range in {
		by[i.Path] = append(by[i.Path], i.Outputs...)
	}
	out := make([]InputDrv, 0, len(by))
	for p, o := range by {
		out = append(out, InputDrv{Path: p, Outputs: sortedSet(o)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// sortedSet returns the sorted distinct elements of s (a new slice).
func sortedSet(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	n := 0
	for i, v := range out {
		if i == 0 || v != out[n-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

func writeStrings(b *bytes.Buffer, s ...string) {
	for i, v := range s {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, v)
	}
}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// Parse parses an ATerm derivation (nixpkgs drvs are parsed recursively by callers).
func Parse(aterm []byte) (*Derivation, error) {
	if len(aterm) > maxATerm {
		return nil, fmt.Errorf("derivation is larger than %d bytes", maxATerm)
	}
	p := &parser{s: aterm}
	d, err := p.derivation()
	if err != nil {
		return nil, fmt.Errorf("parse derivation: %w", err)
	}
	return d, nil
}

type parser struct {
	s   []byte
	pos int
}

func (p *parser) errorf(format string, a ...any) error {
	return fmt.Errorf("offset %d: "+format, append([]any{p.pos}, a...)...)
}

func (p *parser) expect(lit string) error {
	if !bytes.HasPrefix(p.s[p.pos:], []byte(lit)) {
		return p.errorf("expected %q", lit)
	}
	p.pos += len(lit)
	return nil
}

// more consumes the separator of a list: true when another element follows, false at end.
func (p *parser) more(first bool, end byte) (bool, error) {
	if p.pos >= len(p.s) {
		return false, p.errorf("unexpected end")
	}
	if p.s[p.pos] == end {
		p.pos++
		return false, nil
	}
	if !first {
		if p.s[p.pos] != ',' {
			return false, p.errorf("expected ',' or %q", end)
		}
		p.pos++
	}
	return true, nil
}

func (p *parser) str() (string, error) {
	if err := p.expect(`"`); err != nil {
		return "", err
	}
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		p.pos++
		switch c {
		case '"':
			return b.String(), nil
		case '\\':
			if p.pos >= len(p.s) {
				return "", p.errorf("unterminated escape")
			}
			e := p.s[p.pos]
			p.pos++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", p.errorf("unterminated string")
}

// strList parses ["a","b",…].
func (p *parser) strList() ([]string, error) {
	if err := p.expect("["); err != nil {
		return nil, err
	}
	var out []string
	for first := true; ; first = false {
		ok, err := p.more(first, ']')
		if err != nil || !ok {
			return out, err
		}
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
}

// tuple parses ("a","b",…) of exactly n strings.
func (p *parser) tuple(n int) ([]string, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := range out {
		if i > 0 {
			if err := p.expect(","); err != nil {
				return nil, err
			}
		}
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, p.expect(")")
}

func (p *parser) derivation() (*Derivation, error) {
	d := &Derivation{Env: map[string]string{}}
	if err := p.expect("Derive(["); err != nil {
		return nil, err
	}
	for first := true; ; first = false {
		ok, err := p.more(first, ']')
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		t, err := p.tuple(4)
		if err != nil {
			return nil, err
		}
		d.Outputs = append(d.Outputs, Output{Name: t[0], Path: t[1], Algo: t[2], Hash: t[3]})
	}
	if err := p.expect(",["); err != nil {
		return nil, err
	}
	for first := true; ; first = false {
		ok, err := p.more(first, ']')
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if err := p.expect("("); err != nil {
			return nil, err
		}
		path, err := p.str()
		if err != nil {
			return nil, err
		}
		if err := p.expect(","); err != nil {
			return nil, err
		}
		outs, err := p.strList()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		d.InputDrvs = append(d.InputDrvs, InputDrv{Path: path, Outputs: outs})
	}
	if err := p.expect(","); err != nil {
		return nil, err
	}
	var err error
	if d.InputSrcs, err = p.strList(); err != nil {
		return nil, err
	}
	if err := p.expect(","); err != nil {
		return nil, err
	}
	if d.System, err = p.str(); err != nil {
		return nil, err
	}
	if err := p.expect(","); err != nil {
		return nil, err
	}
	if d.Builder, err = p.str(); err != nil {
		return nil, err
	}
	if err := p.expect(","); err != nil {
		return nil, err
	}
	if d.Args, err = p.strList(); err != nil {
		return nil, err
	}
	if err := p.expect(",["); err != nil {
		return nil, err
	}
	for first := true; ; first = false {
		ok, err := p.more(first, ']')
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		t, err := p.tuple(2)
		if err != nil {
			return nil, err
		}
		if _, dup := d.Env[t[0]]; dup {
			return nil, p.errorf("duplicate environment variable %q", t[0])
		}
		d.Env[t[0]] = t[1]
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	if p.pos != len(p.s) {
		return nil, p.errorf("trailing data")
	}
	d.Name = parsedName(d)
	if d.Name == "" {
		return nil, fmt.Errorf("derivation has no name")
	}
	return d, nil
}

// parsedName recovers the derivation name, which the ATerm does not record directly: the
// `name` variable, the `name` structured attribute, or the name part of the `out` path.
func parsedName(d *Derivation) string {
	if n, ok := d.Env["name"]; ok {
		return n
	}
	if js, ok := d.Env["__json"]; ok {
		var a struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(js), &a) == nil && a.Name != "" {
			return a.Name
		}
	}
	for _, o := range d.Outputs {
		if o.Name == "out" {
			base := strings.TrimPrefix(o.Path, StoreDir+"/")
			if i := strings.IndexByte(base, '-'); i == 32 {
				return base[i+1:]
			}
		}
	}
	return ""
}
