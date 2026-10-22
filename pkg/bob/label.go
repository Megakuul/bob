package bob

import (
	"errors"
	"fmt"
	"strings"
)

// Bounds of label strings (R-FW-015, ruling G9). Labels come from the command line, pipelines and
// evaluator output, so they are hostile input: longer strings are invalid.
const (
	maxRefBytes = 1024 // a whole label, pattern or configured label
	maxSegBytes = 255  // one directory segment
	maxName     = 128  // a package name
)

// ErrSyntax is wrapped by every error of Parse, ParseLabel and ParseConfigured.
var ErrSyntax = errors.New("invalid label")

func syntaxErr(s, reason string) error {
	if len(s) > 80 {
		s = s[:60] + fmt.Sprintf("…(%d bytes)", len(s))
	}
	return fmt.Errorf("%w %q: %s", ErrSyntax, s, reason)
}

// Label identifies a package, e.g. //libs/foo:foo (see SPEC §1.3).
//
// A local label has Dir and Name, a foreign label also Module ("example.com/o//libs/bar:bar"),
// and a nixpkgs label only Nix ("nixpkgs:openssl").
type Label struct {
	Module, Dir, Name string
	Nix               string
}

// String returns the canonical form of l.
func (l Label) String() string {
	if l.Nix != "" {
		return "nixpkgs:" + l.Nix
	}
	if l.Name == "" {
		return ""
	}
	return l.Module + "//" + l.Dir + ":" + l.Name
}

// ParseLabel accepts labels only (local, foreign, nixlabel). Patterns and configured labels are
// errors; use Parse for them.
func ParseLabel(s string) (Label, error) {
	if len(s) > maxRefBytes {
		return Label{}, syntaxErr(s, fmt.Sprintf("longer than %d bytes", maxRefBytes))
	}
	if attr, ok := strings.CutPrefix(s, "nixpkgs:"); ok {
		if err := checkAttr(attr); err != nil {
			return Label{}, syntaxErr(s, err.Error())
		}
		return Label{Nix: attr}, nil
	}
	mod, rest, ok := strings.Cut(s, "//")
	if !ok {
		return Label{}, syntaxErr(s, `a label starts with "//", "<module>//" or "nixpkgs:"`)
	}
	if mod != "" {
		if err := checkModule(mod); err != nil {
			return Label{}, syntaxErr(s, err.Error())
		}
	}
	dir, name, ok := strings.Cut(rest, ":")
	if !ok {
		return Label{}, syntaxErr(s, `missing ":<name>"`)
	}
	if err := checkDir(dir); err != nil {
		return Label{}, syntaxErr(s, err.Error())
	}
	if err := checkName(name); err != nil {
		return Label{}, syntaxErr(s, err.Error())
	}
	if name == "all" {
		return Label{}, syntaxErr(s, `":all" is a pattern, not a label`)
	}
	return Label{Module: mod, Dir: dir, Name: name}, nil
}

// Ref is the typed result of Parse: a Label, a Pattern or a Configured value; String() returns
// the canonical form (SPEC §1.3, R-FW-015).
type Ref interface{ String() string }

// Parse accepts the union of label, pattern and configured forms (R-FW-015) and returns a typed
// value: a Label, a Pattern or a Configured label. Use ParseLabel when only a label is valid.
//
// A configured label is parsed without knowing the host or the repository's named variants: an
// omitted platform stays empty and tokens other than release, debug, asan, ubsan and race are
// kept as named variants. ParseConfigured checks both.
func Parse(s string) (Ref, error) {
	if len(s) > maxRefBytes {
		return nil, syntaxErr(s, fmt.Sprintf("longer than %d bytes", maxRefBytes))
	}
	if strings.Contains(s, "@") {
		c, err := parseConfigured(s, nil)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	if p, ok, err := parsePattern(s); ok || err != nil {
		return p, err
	}
	return ParseLabel(s)
}

// Pattern is a label pattern ("//...", "//dir/...", "//dir:all", a label, "//dir"); a string type
// so that fixtures can write []bob.Pattern{"//..."}.
type Pattern string

// String returns the pattern as written.
func (p Pattern) String() string { return string(p) }

// patternKind is the parsed form of a Pattern.
type patternKind int

const (
	patAll     patternKind = iota // //...
	patBelow                      // //dir/...
	patDir                        // //dir:all
	patLabel                      // a label; //dir is //dir:<last segment of dir>
	patInvalid                    // not a pattern
)

// parsePattern parses the pattern forms that are not plain labels. ok is false for strings that
// are not one of those forms (they may still be a label).
func parsePattern(s string) (Pattern, bool, error) {
	rest, local := strings.CutPrefix(s, "//")
	if !local {
		return "", false, nil
	}
	switch {
	case rest == "...":
		return Pattern(s), true, nil
	case strings.HasSuffix(rest, "/..."):
		if err := checkDir(strings.TrimSuffix(rest, "/...")); err != nil || rest == "/..." {
			return "", true, syntaxErr(s, "invalid directory in pattern")
		}
		return Pattern(s), true, nil
	case strings.HasSuffix(rest, ":all"):
		if err := checkDir(strings.TrimSuffix(rest, ":all")); err != nil {
			return "", true, syntaxErr(s, err.Error())
		}
		return Pattern(s), true, nil
	case !strings.Contains(rest, ":"):
		if rest == "" {
			return "", true, syntaxErr(s, `missing a directory or ":<name>"`)
		}
		if err := checkDir(rest); err != nil {
			return "", true, syntaxErr(s, err.Error())
		}
		return Pattern(s), true, nil
	}
	return "", false, nil
}

// kind classifies p and returns the directory or label it names.
func (p Pattern) kind() (patternKind, string, Label) {
	s := string(p)
	if len(s) > maxRefBytes {
		return patInvalid, "", Label{}
	}
	if pp, ok, err := parsePattern(s); err != nil {
		return patInvalid, "", Label{}
	} else if ok {
		rest := strings.TrimPrefix(string(pp), "//")
		switch {
		case rest == "...":
			return patAll, "", Label{}
		case strings.HasSuffix(rest, "/..."):
			return patBelow, strings.TrimSuffix(rest, "/..."), Label{}
		case strings.HasSuffix(rest, ":all"):
			return patDir, strings.TrimSuffix(rest, ":all"), Label{}
		default:
			name := rest[strings.LastIndex(rest, "/")+1:]
			return patLabel, "", Label{Dir: rest, Name: name}
		}
	}
	l, err := ParseLabel(s)
	if err != nil {
		return patInvalid, "", Label{}
	}
	return patLabel, "", l
}

// Valid reports whether p is one of the pattern forms of R-FW-015.
func (p Pattern) Valid() error {
	if k, _, _ := p.kind(); k == patInvalid {
		if _, err := Parse(string(p)); err != nil {
			return err
		}
		return syntaxErr(string(p), "not a label pattern")
	}
	return nil
}

// Match reports whether p selects the package l. Wildcards select packages of this repository
// only; foreign and nixpkgs packages are selected by their exact label.
func (p Pattern) Match(l Label) bool {
	k, dir, want := p.kind()
	local := l.Nix == "" && l.Module == ""
	switch k {
	case patAll:
		return local
	case patBelow:
		return local && (l.Dir == dir || strings.HasPrefix(l.Dir, dir+"/"))
	case patDir:
		return local && l.Dir == dir
	case patLabel:
		return l == want
	}
	return false
}

// Position is a file and line in a bob.go file, module-relative.
type Position struct {
	File string
	Line int
}

// String returns "libs/foo/bob.go:12" ("" for the zero Position).
func (p Position) String() string {
	if p.File == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", p.File, p.Line)
}

func checkName(name string) error {
	if name == "" {
		return errors.New("empty package name")
	}
	if len(name) > maxName {
		return fmt.Errorf("package name longer than %d bytes", maxName)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		lowerOrDigit := 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
		if i == 0 && !lowerOrDigit {
			return fmt.Errorf("package name %q must start with a lowercase letter or digit", name)
		}
		if !lowerOrDigit && c != '_' && c != '-' && c != '.' {
			return fmt.Errorf("package name %q may contain only a-z, 0-9, '_', '-' and '.'", name)
		}
	}
	return nil
}

// checkDir validates a label directory: "" (the repository root) or segments of letters,
// digits, '_', '-' and '.', none of them "." or "..".
func checkDir(dir string) error {
	if dir == "" {
		return nil
	}
	for _, seg := range strings.Split(dir, "/") {
		switch {
		case seg == "":
			return errors.New("empty directory segment")
		case seg == "." || seg == "..":
			return fmt.Errorf("directory segment %q is not allowed", seg)
		case len(seg) > maxSegBytes:
			return fmt.Errorf("directory segment longer than %d bytes", maxSegBytes)
		}
		for i := 0; i < len(seg); i++ {
			if !segChar(seg[i]) {
				return fmt.Errorf("directory segment %q may contain only letters, digits, '_', '-' and '.'", seg)
			}
		}
	}
	return nil
}

func segChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.'
}

func checkAttr(attr string) error {
	if attr == "" {
		return errors.New("empty nixpkgs attribute")
	}
	for i := 0; i < len(attr); i++ {
		if c := attr[i]; !segChar(c) && c != '\'' {
			return fmt.Errorf("nixpkgs attribute %q may contain only letters, digits, '_', '-', '\\'' and '.'", attr)
		}
	}
	return nil
}

// checkModule validates the module path of a foreign label: slash-separated elements of
// letters, digits and "._~-", the first containing a dot (a Go module path that can be fetched).
func checkModule(mod string) error {
	elems := strings.Split(mod, "/")
	for i, e := range elems {
		if e == "" || e == "." || e == ".." {
			return fmt.Errorf("invalid module path %q", mod)
		}
		for j := 0; j < len(e); j++ {
			if c := e[j]; !segChar(c) && c != '~' {
				return fmt.Errorf("invalid module path %q", mod)
			}
		}
		if i == 0 && !strings.Contains(e, ".") {
			return fmt.Errorf("module path %q needs a domain as its first element", mod)
		}
	}
	return nil
}
