package resolve

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A small TOML reader, enough to read the facts bob needs from published Cargo.toml files
// (the layer rules of R-ARCH-003 allow no third-party parser here). It parses the whole TOML
// 1.0 grammar except that date-times are kept as raw strings; values are string, int64,
// float64, bool, []any and map[string]any.

const (
	maxTOML      = 4 << 20
	maxTOMLDepth = 64
)

type tomlParser struct {
	s    string
	i    int
	line int
	root map[string]any
	// tables defined by a [header] (redefinition is an error) and arrays of tables
	defined map[string]bool
}

func parseTOML(src string) (map[string]any, error) {
	if len(src) > maxTOML {
		return nil, fmt.Errorf("manifest larger than %d bytes", maxTOML)
	}
	if !utf8.ValidString(src) {
		return nil, fmt.Errorf("manifest is not UTF-8")
	}
	p := &tomlParser{s: src, line: 1, root: map[string]any{}, defined: map[string]bool{}}
	if err := p.document(); err != nil {
		return nil, fmt.Errorf("line %d: %w", p.line, err)
	}
	return p.root, nil
}

func (p *tomlParser) eof() bool { return p.i >= len(p.s) }

func (p *tomlParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.i]
}

func (p *tomlParser) skipSpace() {
	for !p.eof() && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *tomlParser) skipComment() {
	if p.peek() == '#' {
		for !p.eof() && p.s[p.i] != '\n' {
			p.i++
		}
	}
}

// endLine consumes trailing space, a comment and the newline.
func (p *tomlParser) endLine() error {
	p.skipSpace()
	p.skipComment()
	switch {
	case p.eof():
		return nil
	case p.s[p.i] == '\n':
		p.i++
		p.line++
		return nil
	case strings.HasPrefix(p.s[p.i:], "\r\n"):
		p.i += 2
		p.line++
		return nil
	}
	return fmt.Errorf("unexpected %q", p.s[p.i])
}

// skipBlank skips whitespace, comments and newlines (inside arrays).
func (p *tomlParser) skipBlank() {
	for !p.eof() {
		switch p.s[p.i] {
		case ' ', '\t', '\r':
			p.i++
		case '\n':
			p.i++
			p.line++
		case '#':
			p.skipComment()
		default:
			return
		}
	}
}

func (p *tomlParser) document() error {
	cur := p.root
	for {
		p.skipBlank()
		if p.eof() {
			return nil
		}
		if p.peek() == '[' {
			t, err := p.header()
			if err != nil {
				return err
			}
			cur = t
		} else if err := p.keyValue(cur); err != nil {
			return err
		}
		if err := p.endLine(); err != nil {
			return err
		}
	}
}

func (p *tomlParser) header() (map[string]any, error) {
	array := strings.HasPrefix(p.s[p.i:], "[[")
	if array {
		p.i += 2
	} else {
		p.i++
	}
	p.skipSpace()
	keys, err := p.key()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	closer := "]"
	if array {
		closer = "]]"
	}
	if !strings.HasPrefix(p.s[p.i:], closer) {
		return nil, fmt.Errorf("unterminated table header")
	}
	p.i += len(closer)
	t := p.root
	for _, k := range keys[:len(keys)-1] {
		if t, err = descend(t, k); err != nil {
			return nil, err
		}
	}
	last := keys[len(keys)-1]
	path := strings.Join(keys, "\x00")
	if array {
		arr, _ := t[last].([]any)
		if _, exists := t[last]; exists && arr == nil {
			return nil, fmt.Errorf("%s is not an array of tables", last)
		}
		nt := map[string]any{}
		t[last] = append(arr, nt)
		return nt, nil
	}
	if p.defined[path] {
		return nil, fmt.Errorf("table %s defined twice", strings.Join(keys, "."))
	}
	p.defined[path] = true
	return descend(t, last)
}

// descend returns the table under k, creating it (the last element of an array of tables
// when k names one).
func descend(t map[string]any, k string) (map[string]any, error) {
	switch v := t[k].(type) {
	case nil:
		nt := map[string]any{}
		t[k] = nt
		return nt, nil
	case map[string]any:
		return v, nil
	case []any:
		if len(v) > 0 {
			if m, ok := v[len(v)-1].(map[string]any); ok {
				return m, nil
			}
		}
	}
	return nil, fmt.Errorf("%s is not a table", k)
}

func (p *tomlParser) keyValue(t map[string]any) error {
	keys, err := p.key()
	if err != nil {
		return err
	}
	p.skipSpace()
	if p.peek() != '=' {
		return fmt.Errorf("expected = after key")
	}
	p.i++
	p.skipSpace()
	v, err := p.value(0)
	if err != nil {
		return err
	}
	for _, k := range keys[:len(keys)-1] {
		if t, err = descend(t, k); err != nil {
			return err
		}
	}
	last := keys[len(keys)-1]
	if _, dup := t[last]; dup {
		return fmt.Errorf("key %s defined twice", last)
	}
	t[last] = v
	return nil
}

// key parses a dotted key of bare and quoted parts.
func (p *tomlParser) key() ([]string, error) {
	var keys []string
	for {
		p.skipSpace()
		var k string
		switch c := p.peek(); {
		case c == '"':
			s, err := p.basicString()
			if err != nil {
				return nil, err
			}
			k = s
		case c == '\'':
			s, err := p.literalString()
			if err != nil {
				return nil, err
			}
			k = s
		default:
			j := p.i
			for !p.eof() && isBare(p.s[p.i]) {
				p.i++
			}
			if j == p.i {
				return nil, fmt.Errorf("expected a key")
			}
			k = p.s[j:p.i]
		}
		keys = append(keys, k)
		p.skipSpace()
		if p.peek() != '.' {
			return keys, nil
		}
		p.i++
	}
}

func isBare(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func (p *tomlParser) value(depth int) (any, error) {
	if depth > maxTOMLDepth {
		return nil, fmt.Errorf("values nested too deeply")
	}
	switch c := p.peek(); {
	case strings.HasPrefix(p.s[p.i:], `"""`):
		return p.multiBasic()
	case strings.HasPrefix(p.s[p.i:], `'''`):
		return p.multiLiteral()
	case c == '"':
		return p.basicString()
	case c == '\'':
		return p.literalString()
	case c == '[':
		return p.array(depth)
	case c == '{':
		return p.inlineTable(depth)
	case strings.HasPrefix(p.s[p.i:], "true"):
		p.i += 4
		return true, nil
	case strings.HasPrefix(p.s[p.i:], "false"):
		p.i += 5
		return false, nil
	}
	j := p.i
	for !p.eof() && strings.IndexByte(" \t\r\n,]}#", p.s[p.i]) < 0 {
		p.i++
	}
	raw := p.s[j:p.i]
	if raw == "" {
		return nil, fmt.Errorf("expected a value")
	}
	// a date-time may hold one space between date and time
	if len(raw) == 10 && raw[4] == '-' && p.peek() == ' ' && p.i+1 < len(p.s) && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		p.i++
		for !p.eof() && strings.IndexByte(" \t\r\n,]}#", p.s[p.i]) < 0 {
			p.i++
		}
		return p.s[j:p.i], nil
	}
	clean := strings.ReplaceAll(raw, "_", "")
	if n, err := strconv.ParseInt(clean, 0, 64); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil {
		return f, nil
	}
	switch clean {
	case "inf", "+inf", "-inf", "nan", "+nan", "-nan":
		return clean, nil
	}
	if strings.ContainsAny(raw, "-:") && raw[0] >= '0' && raw[0] <= '9' {
		return raw, nil // date or time, kept raw
	}
	return nil, fmt.Errorf("invalid value %q", raw)
}

func (p *tomlParser) array(depth int) ([]any, error) {
	p.i++
	out := []any{}
	for {
		p.skipBlank()
		if p.peek() == ']' {
			p.i++
			return out, nil
		}
		if p.eof() {
			return nil, fmt.Errorf("unterminated array")
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipBlank()
		switch p.peek() {
		case ',':
			p.i++
		case ']':
		default:
			return nil, fmt.Errorf("expected , or ] in array")
		}
	}
}

func (p *tomlParser) inlineTable(depth int) (map[string]any, error) {
	p.i++
	t := map[string]any{}
	p.skipSpace()
	if p.peek() == '}' {
		p.i++
		return t, nil
	}
	for {
		p.skipSpace()
		keys, err := p.key()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.peek() != '=' {
			return nil, fmt.Errorf("expected = in inline table")
		}
		p.i++
		p.skipSpace()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		cur := t
		for _, k := range keys[:len(keys)-1] {
			if cur, err = descend(cur, k); err != nil {
				return nil, err
			}
		}
		if _, dup := cur[keys[len(keys)-1]]; dup {
			return nil, fmt.Errorf("key %s defined twice", keys[len(keys)-1])
		}
		cur[keys[len(keys)-1]] = v
		p.skipSpace()
		switch p.peek() {
		case ',':
			p.i++
		case '}':
			p.i++
			return t, nil
		default:
			return nil, fmt.Errorf("expected , or } in inline table")
		}
	}
}

func (p *tomlParser) literalString() (string, error) {
	p.i++
	j := strings.IndexAny(p.s[p.i:], "'\n")
	if j < 0 || p.s[p.i+j] != '\'' {
		return "", fmt.Errorf("unterminated string")
	}
	s := p.s[p.i : p.i+j]
	p.i += j + 1
	return s, nil
}

func (p *tomlParser) multiLiteral() (string, error) {
	p.i += 3
	if strings.HasPrefix(p.s[p.i:], "\r\n") {
		p.i += 2
	} else if p.peek() == '\n' {
		p.i++
	}
	j := strings.Index(p.s[p.i:], "'''")
	if j < 0 {
		return "", fmt.Errorf("unterminated string")
	}
	n := 3 // up to two extra quotes belong to the content
	for n < 5 && p.i+j+n < len(p.s) && p.s[p.i+j+n] == '\'' {
		n++
	}
	s := p.s[p.i : p.i+j+n-3]
	p.line += strings.Count(s, "\n")
	p.i += j + n
	return s, nil
}

func (p *tomlParser) basicString() (string, error) {
	p.i++
	var b strings.Builder
	for {
		if p.eof() || p.s[p.i] == '\n' {
			return "", fmt.Errorf("unterminated string")
		}
		c := p.s[p.i]
		if c == '"' {
			p.i++
			return b.String(), nil
		}
		if c == '\\' {
			if err := p.escape(&b, false); err != nil {
				return "", err
			}
			continue
		}
		b.WriteByte(c)
		p.i++
	}
}

func (p *tomlParser) multiBasic() (string, error) {
	p.i += 3
	if strings.HasPrefix(p.s[p.i:], "\r\n") {
		p.i += 2
	} else if p.peek() == '\n' {
		p.i++
	}
	var b strings.Builder
	for {
		if p.eof() {
			return "", fmt.Errorf("unterminated string")
		}
		if strings.HasPrefix(p.s[p.i:], `"""`) {
			// up to two extra quotes belong to the content
			n := 3
			for n < 5 && p.i+n < len(p.s) && p.s[p.i+n] == '"' {
				n++
			}
			b.WriteString(strings.Repeat(`"`, n-3))
			p.i += n
			return b.String(), nil
		}
		c := p.s[p.i]
		if c == '\\' {
			if err := p.escape(&b, true); err != nil {
				return "", err
			}
			continue
		}
		if c == '\n' {
			p.line++
		}
		b.WriteByte(c)
		p.i++
	}
}

func (p *tomlParser) escape(b *strings.Builder, multi bool) error {
	p.i++
	if p.eof() {
		return fmt.Errorf("unterminated escape")
	}
	c := p.s[p.i]
	p.i++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case 'e':
		b.WriteByte(0x1b)
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if p.i+n > len(p.s) {
			return fmt.Errorf("short unicode escape")
		}
		r, err := strconv.ParseUint(p.s[p.i:p.i+n], 16, 32)
		if err != nil || !utf8.ValidRune(rune(r)) {
			return fmt.Errorf("bad unicode escape")
		}
		b.WriteRune(rune(r))
		p.i += n
	case 'x':
		if p.i+2 > len(p.s) {
			return fmt.Errorf("short escape")
		}
		r, err := strconv.ParseUint(p.s[p.i:p.i+2], 16, 8)
		if err != nil {
			return fmt.Errorf("bad escape")
		}
		b.WriteRune(rune(r))
		p.i += 2
	case ' ', '\t', '\r', '\n':
		if !multi {
			return fmt.Errorf("bad escape")
		}
		// line-ending backslash: trim whitespace and newlines up to the next content
		p.i--
		for !p.eof() && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
			if p.s[p.i] == '\n' {
				p.line++
			}
			p.i++
		}
	default:
		return fmt.Errorf("bad escape \\%c", c)
	}
	return nil
}
