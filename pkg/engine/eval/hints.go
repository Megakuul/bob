package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Hints for compile errors of the typed nixpkgs surface (SPEC §1.7, ADR 0017 amendment,
// BOB-E-1020): a misspelt handle gets "did you mean nixpkgs.<Name>?", the removed string role
// functions (nixpkgs.Lib(p, "openssl")) get the typed form of the same call.

var (
	undefinedNixRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^undefined: nixpkgs\.([A-Za-z_][A-Za-z0-9_]*)$`) })
	handleRe       = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?m)^\t([A-Za-z][A-Za-z0-9_]*) = nixext\.New\("([^"\\]+)"\)$`)
	})
	stringRoleRe = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`nixpkgs\.(Lib|Tool|Pkg|Exe)\(\s*[A-Za-z_][A-Za-z0-9_.()]*\s*,\s*"([^"\\]*)"`)
	})
)

// handles is the generated surface of one framework (Go name → attribute and back), read
// once per framework hash from its attrs_gen.go: the designed memoisation of a per-version
// constant (R-FW-011 spirit; the file never changes within one bob).
type handles struct {
	names  []string          // sorted Go names
	byAttr map[string]string // attribute → Go name
}

var (
	handlesMu   sync.Mutex
	handlesMemo = map[string]*handles{}
)

func surfaceOf(fw *framework) *handles {
	if fw == nil {
		return &handles{byAttr: map[string]string{}}
	}
	handlesMu.Lock()
	defer handlesMu.Unlock()
	if h, ok := handlesMemo[fw.h1]; ok {
		return h
	}
	h := &handles{byAttr: map[string]string{}}
	for _, m := range handleRe().FindAllSubmatch(fw.files["pkg/nixpkgs/attrs_gen.go"], -1) {
		h.names = append(h.names, string(m[1]))
		h.byAttr[string(m[2])] = string(m[1])
	}
	sort.Strings(h.names)
	handlesMemo[fw.h1] = h
	return h
}

// nixpkgsHint returns the hint for a compile message of the user's file at line ("" = none).
func nixpkgsHint(msg, root, file string, line int, fw *framework) string {
	m := undefinedNixRe().FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	name := m[1]
	h := surfaceOf(fw)
	switch name {
	case "Lib", "Tool", "Pkg", "Exe":
		attr, ok := stringArg(root, file, line, name)
		if !ok {
			return "nixpkgs packages are typed: write nixpkgs.<Name>." + name + "(p) with the generated handle of the package (go doc nixpkgs)"
		}
		if goName, ok := h.byAttr[attr]; ok {
			return "nixpkgs packages are typed: write nixpkgs." + goName + "." + name + "(p)"
		}
		return "nixpkgs packages are typed: write nixpkgs.<Name>." + name + "(p); nixpkgs \"" + attr + "\" has no generated handle (see the exclusions in go doc nixpkgs)"
	}
	if s := suggest(name, h.names); len(s) > 0 {
		return "did you mean nixpkgs." + strings.Join(s, ", nixpkgs.") + "?"
	}
	return ""
}

// stringArg returns the string literal passed to nixpkgs.<fn>(p, "…") on the line.
func stringArg(root, file string, line int, fn string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return "", false
	}
	lines := bytes.Split(b, []byte("\n"))
	if line < 1 || line > len(lines) {
		return "", false
	}
	for _, m := range stringRoleRe().FindAllSubmatch(lines[line-1], -1) {
		if string(m[1]) == fn {
			return string(m[2]), true
		}
	}
	return "", false
}

// suggest returns up to three generated names closest to name: the smallest edit distance
// (≤ 2, a transposition counts one), else names that start with it.
func suggest(name string, names []string) []string {
	best, out := 3, []string(nil)
	for _, n := range names {
		if abs(len(n)-len(name)) > 2 {
			continue
		}
		d := editDistance(strings.ToLower(name), strings.ToLower(n))
		switch {
		case d < best:
			best, out = d, []string{n}
		case d == best:
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		for _, n := range names {
			if strings.HasPrefix(n, name) {
				out = append(out, n)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// editDistance is the optimal string alignment distance of a and b.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// withHint sets the hint of a compile error (BOB-E-1020's "{hint}" template argument).
func withHint(e *diag.Error, hint string) {
	if hint != "" {
		e.Args["hint"] = hint
	}
}
