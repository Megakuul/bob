package bob

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// The structs below are the IR v1 document as the evaluator writes it (SPEC §1.6). Package
// pkg/engine/ir reads it; it cannot be imported here (L0 must not import the engine).
type irDoc struct {
	IRVersion  int            `json:"irVersion"`
	BobVersion string         `json:"bobVersion"`
	Module     string         `json:"module"`
	Repo       irRepo         `json:"repo"`
	Configs    []irConfig     `json:"configs"`
	Nodes      []irNode       `json:"nodes"`
	Roots      []string       `json:"roots"`
	Pipelines  []pipelineJSON `json:"pipelines"`
}

type irRepo struct {
	Nixpkgs  string      `json:"nixpkgs"`
	Variants []irVariant `json:"variants"`
}

// irVariant is a named variant applied to the zero variant.
type irVariant struct {
	Name       string              `json:"name"`
	Profile    Profile             `json:"profile"`
	Sanitizers []Sanitizer         `json:"sanitizers"`
	Race       bool                `json:"race"`
	Features   map[string][]string `json:"features"`
}

type irConfig struct {
	ID         string              `json:"id"`
	Platform   Platform            `json:"platform"`
	Profile    Profile             `json:"profile"`
	Sanitizers []Sanitizer         `json:"sanitizers"`
	Race       bool                `json:"race"`
	Features   map[string][]string `json:"features"`
	Named      []string            `json:"named"`
}

type irNode struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Label     string          `json:"label"`
	DefinedAt irPos           `json:"definedAt"`
	Proj      map[string]any  `json:"proj"`
	Deps      []string        `json:"deps"`
	Srcs      []irSrcs        `json:"srcs"`
	Spec      json.RawMessage `json:"spec"`
	Nix       json.RawMessage `json:"nix,omitempty"`
}

type irPos struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
}

type irSrcs struct {
	Dir      string   `json:"dir"`
	Patterns []string `json:"patterns"`
}

// encode writes the canonical IR of the nodes reachable from roots.
func (ev *evaluation) encode(repo RepoOpts, pipes []pipelineJSON, targets []target, roots []*node) ([]byte, error) {
	doc := irDoc{IRVersion: irVersion, BobVersion: ev.env.bobVersion, Module: ev.env.mainModule,
		Repo:      irRepo{Nixpkgs: repo.Nixpkgs, Variants: []irVariant{}},
		Configs:   []irConfig{},
		Nodes:     []irNode{},
		Roots:     nonNil(sortedIDs(roots)),
		Pipelines: nonNil(pipes),
	}
	for _, n := range repo.Variants {
		v := Variant{}
		n.apply(&v)
		v = v.normal()
		doc.Repo.Variants = append(doc.Repo.Variants, irVariant{Name: n.name, Profile: v.Profile,
			Sanitizers: nonNil(v.Sanitize), Race: v.Race, Features: featureMap(v.Features)})
	}
	sort.Slice(doc.Repo.Variants, func(i, j int) bool { return doc.Repo.Variants[i].Name < doc.Repo.Variants[j].Name })
	seenCfg := map[*P]bool{}
	for _, t := range targets {
		if seenCfg[t.p] {
			continue
		}
		seenCfg[t.p] = true
		v := t.p.variant
		doc.Configs = append(doc.Configs, irConfig{ID: t.p.id, Platform: t.p.platform, Profile: v.Profile,
			Sanitizers: nonNil(v.Sanitize), Race: v.Race, Features: featureMap(v.Features), Named: nonNil(v.Named)})
	}
	sort.Slice(doc.Configs, func(i, j int) bool { return doc.Configs[i].ID < doc.Configs[j].ID })

	seen := map[*node]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if seen[n] {
			return
		}
		seen[n] = true
		in := irNode{ID: n.id, Kind: n.kind.Name, Label: n.label.String(), DefinedAt: irPos{n.pos.File, n.pos.Line, ev.column(n.pos, n.label.Nix+n.label.Name)},
			Proj: n.proj.json(), Deps: []string{}, Srcs: []irSrcs{}, Spec: n.spec, Nix: n.nix}
		for _, d := range n.deps {
			in.Deps = append(in.Deps, d.id)
			walk(d)
		}
		for _, g := range n.srcs {
			in.Srcs = append(in.Srcs, irSrcs{Dir: g.dir, Patterns: nonNil(g.patterns)})
		}
		doc.Nodes = append(doc.Nodes, in)
	}
	for _, r := range roots {
		walk(r)
	}
	sort.Slice(doc.Nodes, func(i, j int) bool { return doc.Nodes[i].ID < doc.Nodes[j].ID })
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(b)
}

// canonicalJSON re-encodes JSON with object keys sorted, no insignificant whitespace, no HTML
// escaping and numbers kept as written (SPEC §1.6). It gives exactly what decoding into
// generic values and encoding again (decodeEncode) gives, without building the value tree:
// for 1,000 bob.go files that round trip was most of an evaluation's time (I-B39, A318).
func canonicalJSON(b []byte) ([]byte, error) {
	if !json.Valid(b) {
		return decodeEncode(b) // its error
	}
	var out bytes.Buffer
	out.Grow(len(b))
	c := canon{b: b}
	if err := c.value(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// canon walks a valid JSON document.
type canon struct {
	b []byte
	i int
}

func (c *canon) ws() {
	for c.i < len(c.b) && (c.b[c.i] == ' ' || c.b[c.i] == '\t' || c.b[c.i] == '\n' || c.b[c.i] == '\r') {
		c.i++
	}
}

// value writes the canonical form of the value at c.i and moves past it.
func (c *canon) value(out *bytes.Buffer) error {
	c.ws()
	switch c.b[c.i] {
	case '{':
		return c.object(out)
	case '[':
		c.i++
		out.WriteByte('[')
		c.ws()
		if c.b[c.i] == ']' {
			c.i++
			out.WriteByte(']')
			return nil
		}
		for {
			if err := c.value(out); err != nil {
				return err
			}
			c.ws()
			if c.b[c.i] == ']' {
				c.i++
				out.WriteByte(']')
				return nil
			}
			c.i++ // ','
			out.WriteByte(',')
		}
	case '"':
		start := c.i
		c.skipString()
		return writeCanonString(out, c.b[start:c.i])
	default: // number or literal: kept as written
		start := c.i
		c.skipScalar()
		out.Write(c.b[start:c.i])
		return nil
	}
}

// skipString moves past the string token at c.i.
func (c *canon) skipString() {
	c.i++
	for c.b[c.i] != '"' {
		if c.b[c.i] == '\\' {
			c.i++
		}
		c.i++
	}
	c.i++
}

// skipValue moves past the value at c.i (valid JSON) without writing it.
func (c *canon) skipValue() {
	c.ws()
	switch c.b[c.i] {
	case '"':
		c.skipString()
	case '{', '[':
		depth := 0
		for {
			switch c.b[c.i] {
			case '"':
				c.skipString()
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			c.i++
			if depth == 0 {
				return
			}
		}
	default:
		c.skipScalar()
	}
}

// skipScalar moves past a number or literal.
func (c *canon) skipScalar() {
	for c.i < len(c.b) && !strings.ContainsRune(" \t\n\r,:]}", rune(c.b[c.i])) {
		c.i++
	}
}

// member is one key and the extent of its value.
type member struct {
	key        string
	tok        []byte // the key's token
	start, end int
}

// object writes an object with its members sorted by key (the last of equal keys wins, as
// when decoding into a map).
func (c *canon) object(out *bytes.Buffer) error {
	c.i++ // '{'
	var ms []member
	for {
		c.ws()
		if c.b[c.i] == '}' {
			c.i++
			break
		}
		if c.b[c.i] == ',' {
			c.i++
			continue
		}
		ks := c.i
		c.skipString()
		tok := c.b[ks:c.i]
		key := string(tok[1 : len(tok)-1])
		if !plainString(tok) {
			var err error
			if key, err = decodeString(tok); err != nil {
				return err
			}
		}
		c.ws()
		c.i++ // ':'
		c.ws()
		vs := c.i
		c.skipValue()
		ms = append(ms, member{key: key, tok: tok, start: vs, end: c.i})
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].key < ms[j].key })
	out.WriteByte('{')
	first := true
	for i, m := range ms {
		if i+1 < len(ms) && ms[i+1].key == m.key {
			continue // a later duplicate wins
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		if err := writeCanonString(out, m.tok); err != nil {
			return err
		}
		out.WriteByte(':')
		sub := canon{b: c.b[:m.end], i: m.start}
		if err := sub.value(out); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}

// writeCanonString writes the canonical form of the string token tok: the token itself when it
// holds only printable ASCII without escapes (decoding and encoding give it back unchanged),
// else the decoded string encoded again.
func writeCanonString(out *bytes.Buffer, tok []byte) error {
	if plainString(tok) {
		out.Write(tok)
		return nil
	}
	s, err := decodeString(tok)
	if err != nil {
		return err
	}
	return encodeString(out, s)
}

// plainString reports whether the string token holds only printable ASCII and no escapes.
func plainString(tok []byte) bool {
	for _, ch := range tok[1 : len(tok)-1] {
		if ch < 0x20 || ch >= 0x7f || ch == '\\' || ch == '"' {
			return false
		}
	}
	return true
}

func decodeString(tok []byte) (string, error) {
	var s string
	err := json.Unmarshal(tok, &s)
	return s, err
}

// encodeString writes s as the encoder of decodeEncode does (no HTML escaping).
func encodeString(out *bytes.Buffer, s string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	out.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return nil
}

// decodeEncode is the definition of canonicalJSON: decode into generic values (numbers kept as
// written) and encode them again.
func decodeEncode(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
