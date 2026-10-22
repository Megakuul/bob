package resolve

import (
	"fmt"
	"strconv"
	"strings"
)

// ModFile is the part of a dependency's go.mod that resolution reads. A dependency's
// replace, exclude and retract lines do not affect its dependents (cmd/go), so they are
// ignored.
type ModFile struct {
	Module  string
	Go      string // "" when the file has no go directive
	Require []Module
}

// Pruned reports whether the module's requirements are pruned for its dependents (go ≥ 1.17,
// cmd/go's graph pruning).
func (m *ModFile) Pruned() bool { return m.Go != "" && GoCompare(m.Go, "1.17") >= 0 }

// ParseModFile reads module, go and require directives (line and block forms). It is
// strict about what it reads and lenient about the rest: unknown directives are skipped.
func ParseModFile(b []byte) (*ModFile, error) {
	m := &ModFile{}
	block := ""
	for i, raw := range strings.Split(string(b), "\n") {
		f, err := modFields(raw)
		if err != nil {
			return nil, fmt.Errorf("go.mod line %d: %w", i+1, err)
		}
		if len(f) == 0 {
			continue
		}
		if block != "" {
			if f[0] == ")" {
				block = ""
				continue
			}
			f = append([]string{block}, f...)
		} else if len(f) == 2 && f[1] == "(" {
			block = f[0]
			continue
		}
		switch f[0] {
		case "module":
			if len(f) != 2 {
				return nil, fmt.Errorf("go.mod line %d: malformed module directive", i+1)
			}
			m.Module = f[1]
		case "go":
			if len(f) != 2 {
				return nil, fmt.Errorf("go.mod line %d: malformed go directive", i+1)
			}
			if _, ok := parseGo(f[1]); !ok {
				return nil, fmt.Errorf("go.mod line %d: invalid go version %q", i+1, f[1])
			}
			m.Go = f[1]
		case "require":
			if len(f) != 3 {
				return nil, fmt.Errorf("go.mod line %d: malformed require directive", i+1)
			}
			if err := checkPath(f[1]); err != nil {
				return nil, fmt.Errorf("go.mod line %d: %w", i+1, err)
			}
			if !ValidVersion(f[2]) {
				return nil, fmt.Errorf("go.mod line %d: invalid version %q of %s", i+1, f[2], f[1])
			}
			m.Require = append(m.Require, Module{Path: f[1], Version: f[2]})
		}
	}
	if block != "" {
		return nil, fmt.Errorf("go.mod: unterminated %s block", block)
	}
	return m, nil
}

// modFields splits a go.mod line into tokens: comments dropped, quoted strings unquoted.
func modFields(line string) ([]string, error) {
	var out []string
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case strings.HasPrefix(line[i:], "//"):
			return out, nil
		case c == '"' || c == '`':
			j := i + 1
			for j < len(line) && line[j] != c {
				if c == '"' && line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(line) {
				return nil, fmt.Errorf("unterminated string")
			}
			s, err := strconv.Unquote(line[i : j+1])
			if err != nil {
				return nil, err
			}
			out = append(out, s)
			i = j + 1
		case c == '(' || c == ')':
			out = append(out, string(c))
			i++
		default:
			j := i
			for j < len(line) && !strings.ContainsRune(" \t\r()\"`", rune(line[j])) && !strings.HasPrefix(line[j:], "//") {
				j++
			}
			out = append(out, line[i:j])
			i = j
		}
	}
	return out, nil
}

// checkPath accepts module paths as the proxy protocol can carry them: non-empty, no spaces,
// no "..", no leading or trailing slash, printable ASCII.
func checkPath(p string) error {
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return fmt.Errorf("invalid module path %q", p)
	}
	for _, e := range strings.Split(p, "/") {
		if e == "." || e == ".." {
			return fmt.Errorf("invalid module path %q", p)
		}
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c <= ' ' || c > '~' || strings.IndexByte("\"'`\\:*?<>|", c) >= 0 {
			return fmt.Errorf("invalid module path %q", p)
		}
	}
	return nil
}
