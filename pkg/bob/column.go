package bob

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Columns of constructor calls (R-FW-016: bob graph shows file:line:col). The runtime knows
// only the line of a call, so the evaluator remembers which function each bob.go line called
// and finds that call in the line's text when it writes the IR.

// noteCall records fn (a runtime function name) as the function called at pos; the first call
// seen at a position wins.
func (ev *evaluation) noteCall(pos Position, fn string) {
	if ev.calls == nil {
		ev.calls = map[Position]string{}
	}
	if _, ok := ev.calls[pos]; !ok {
		ev.calls[pos] = fn
	}
}

// column is the 1-based byte column of the call recorded at pos, 0 when unknown. name is the
// package's name: of several calls on one line, the one naming it wins.
func (ev *evaluation) column(pos Position, name string) int {
	fn, ok := ev.calls[pos]
	if !ok || pos.Line < 1 {
		return 0
	}
	if ev.lines == nil {
		ev.lines = map[string][][]byte{}
	}
	lines, ok := ev.lines[pos.File]
	if !ok {
		if b, err := os.ReadFile(filepath.Join(ev.root, filepath.FromSlash(pos.File))); err == nil {
			lines = bytes.Split(b, []byte("\n"))
		}
		ev.lines[pos.File] = lines
	}
	if pos.Line > len(lines) {
		return 0
	}
	return callColumn(string(lines[pos.Line-1]), funcName(fn), name)
}

// funcName is the plain name of a runtime function name: "example.com/x/cc.SharedLibrary" →
// "SharedLibrary", "x/cc.(*Lib).With" → "With", "x/cc.New[...]" → "New"; closures
// ("x/cc.F.func1") give their enclosing function.
func funcName(fn string) string {
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		fn = fn[i+1:]
	}
	if i := strings.IndexByte(fn, '['); i >= 0 {
		fn = fn[:i] + fn[strings.LastIndexByte(fn, ']')+1:]
	}
	parts := strings.Split(fn, ".")
	for i := len(parts) - 1; i > 0; i-- {
		p := parts[i]
		if p == "" || strings.HasPrefix(p, "func") && strings.TrimLeft(p[4:], "0123456789") == "" ||
			strings.TrimLeft(p, "0123456789") == "" {
			continue
		}
		return strings.Trim(p, "(*)")
	}
	return ""
}

// callColumn finds the calls of fn in line ("fn(" or "fn[", optionally qualified as
// "pkg.fn") and returns the 1-based byte column of the call expression: the first call whose
// text up to the next call holds the string literal "name", else the first call; 0 if there is
// none.
func callColumn(line, fn, name string) int {
	starts := callStarts(line, fn)
	if len(starts) == 0 {
		return 0
	}
	if name != "" {
		lit := `"` + name + `"`
		for i, s := range starts {
			end := len(line)
			if i+1 < len(starts) {
				end = starts[i+1]
			}
			if strings.Contains(line[s:end], lit) {
				return s + 1
			}
		}
	}
	return starts[0] + 1
}

// callStarts returns the byte offsets of the calls of fn in line, qualifier included.
func callStarts(line, fn string) []int {
	if fn == "" {
		return nil
	}
	var out []int
	for from := 0; ; {
		i := strings.Index(line[from:], fn)
		if i < 0 {
			return out
		}
		start, end := from+i, from+i+len(fn)
		from = end
		if !boundary(line, start) || end < len(line) && isIdent(rune(line[end])) {
			continue
		}
		rest := strings.TrimLeft(line[end:], " \t")
		if rest == "" || rest[0] != '(' && rest[0] != '[' {
			continue
		}
		// Include a qualifier: "cc.SharedLibrary" starts at "cc".
		if start > 0 && line[start-1] == '.' {
			q := start - 1
			for q > 0 {
				r, size := utf8.DecodeLastRuneInString(line[:q])
				if !isIdent(r) {
					break
				}
				q -= size
			}
			if q < start-1 {
				start = q
			}
		}
		out = append(out, start)
	}
}

// boundary reports whether an identifier may start at i (the byte before is no identifier
// character; a '.' is allowed for a qualified call).
func boundary(line string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(line[:i])
	return !isIdent(r)
}

func isIdent(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
