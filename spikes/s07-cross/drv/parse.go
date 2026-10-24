package drv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Parsed is a generic derivation read from a .drv file (any number of outputs).
type Parsed struct {
	Outputs   [][4]string // name, path, hashAlgo, hash
	InputDrvs []struct {
		Path string
		Outs []string
	}
	InputSrcs []string
	System    string
	Builder   string
	Args      []string
	Env       [][2]string
}

func (p *Parsed) Path(out string) string {
	for _, o := range p.Outputs {
		if o[0] == out {
			return o[1]
		}
	}
	return ""
}

type parser struct {
	s   string
	pos int
}

func (p *parser) expect(lit string) {
	if !strings.HasPrefix(p.s[p.pos:], lit) {
		panic(fmt.Sprintf("expected %q at %d", lit, p.pos))
	}
	p.pos += len(lit)
}

func (p *parser) str() string {
	p.expect(`"`)
	var b strings.Builder
	for {
		c := p.s[p.pos]
		p.pos++
		switch c {
		case '"':
			return b.String()
		case '\\':
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
}

func (p *parser) list(item func()) {
	p.expect("[")
	for p.s[p.pos] != ']' {
		item()
		if p.s[p.pos] == ',' {
			p.pos++
		}
	}
	p.pos++
}

func (p *parser) strs() []string {
	var xs []string
	p.list(func() { xs = append(xs, p.str()) })
	return xs
}

// Parse parses ATerm derivation text.
func Parse(s string) (d *Parsed, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("parse drv: %v", r)
		}
	}()
	p := &parser{s: s}
	d = &Parsed{}
	p.expect("Derive(")
	p.list(func() {
		p.expect("(")
		var o [4]string
		for i := range o {
			o[i] = p.str()
			if i < 3 {
				p.expect(",")
			}
		}
		p.expect(")")
		d.Outputs = append(d.Outputs, o)
	})
	p.expect(",")
	p.list(func() {
		p.expect("(")
		path := p.str()
		p.expect(",")
		outs := p.strs()
		p.expect(")")
		d.InputDrvs = append(d.InputDrvs, struct {
			Path string
			Outs []string
		}{path, outs})
	})
	p.expect(",")
	d.InputSrcs = p.strs()
	p.expect(",")
	d.System = p.str()
	p.expect(",")
	d.Builder = p.str()
	p.expect(",")
	d.Args = p.strs()
	p.expect(",")
	p.list(func() {
		p.expect("(")
		k := p.str()
		p.expect(",")
		v := p.str()
		p.expect(")")
		d.Env = append(d.Env, [2]string{k, v})
	})
	p.expect(")")
	return d, nil
}

func (d *Parsed) aterm(repl map[string]string) string {
	var b strings.Builder
	b.WriteString("Derive([")
	for i, o := range d.Outputs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%s,%s,%s,%s)", q(o[0]), q(o[1]), q(o[2]), q(o[3]))
	}
	b.WriteString("],[")
	type in struct {
		k    string
		outs []string
	}
	merged := map[string][]string{}
	for _, x := range d.InputDrvs {
		k := x.Path
		if repl != nil {
			k = repl[x.Path]
		}
		merged[k] = append(merged[k], x.Outs...)
	}
	var ins []in
	for k, outs := range merged {
		set := map[string]bool{}
		var u []string
		for _, o := range outs {
			if !set[o] {
				set[o] = true
				u = append(u, o)
			}
		}
		sort.Strings(u)
		ins = append(ins, in{k, u})
	}
	sort.Slice(ins, func(i, j int) bool { return ins[i].k < ins[j].k })
	for i, x := range ins {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%s,%s)", q(x.k), list(x.outs))
	}
	fmt.Fprintf(&b, "],%s,%s,%s,%s,[", list(d.InputSrcs), q(d.System), q(d.Builder), list(d.Args))
	for i, e := range d.Env {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%s,%s)", q(e[0]), q(e[1]))
	}
	b.WriteString("])")
	return b.String()
}

// ModuloCache computes hashDerivationModulo for .drv files in the local store, memoised.
type ModuloCache struct {
	mu     sync.Mutex
	m      map[string][32]byte
	Parsed int
}

func NewModuloCache() *ModuloCache { return &ModuloCache{m: map[string][32]byte{}} }

func (c *ModuloCache) Set(path string, h [32]byte) {
	c.mu.Lock()
	c.m[path] = h
	c.mu.Unlock()
}

func (c *ModuloCache) Modulo(path string) ([32]byte, bool) {
	c.mu.Lock()
	h, ok := c.m[path]
	c.mu.Unlock()
	if ok {
		return h, true
	}
	h, err := c.compute(path)
	if err != nil {
		return h, false
	}
	return h, true
}

func (c *ModuloCache) compute(path string) ([32]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, err
	}
	d, err := Parse(string(raw))
	if err != nil {
		return [32]byte{}, fmt.Errorf("%s: %w", path, err)
	}
	c.mu.Lock()
	c.Parsed++
	c.mu.Unlock()
	var h [32]byte
	if len(d.Outputs) == 1 && d.Outputs[0][0] == "out" && d.Outputs[0][3] != "" {
		o := d.Outputs[0]
		h = sha256.Sum256([]byte("fixed:out:" + o[2] + ":" + o[3] + ":" + o[1]))
	} else {
		repl := map[string]string{}
		for _, in := range d.InputDrvs {
			ih, ok := c.Modulo(in.Path)
			if !ok {
				return h, fmt.Errorf("%s: cannot hash input %s", path, in.Path)
			}
			repl[in.Path] = hex.EncodeToString(ih[:])
		}
		h = sha256.Sum256([]byte(d.aterm(repl)))
	}
	c.Set(path, h)
	return h, nil
}

// Export / Import let the index persist modulo hashes per nixpkgs revision.
func (c *ModuloCache) Export() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.m))
	for k, v := range c.m {
		out[k] = hex.EncodeToString(v[:])
	}
	return out
}

func (c *ModuloCache) Import(m map[string]string) {
	for k, v := range m {
		b, err := hex.DecodeString(v)
		if err == nil && len(b) == 32 {
			var h [32]byte
			copy(h[:], b)
			c.Set(k, h)
		}
	}
}
