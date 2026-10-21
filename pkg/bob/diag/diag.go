// Package diag defines bob's user-facing errors. Every error a user sees is a *Error with a
// stable Code; raw tool output is kept in Raw and shown only with -v.
//
// The code table (SPEC §02.3) is in codes.go and is append-only (R-NIX-028).
package diag

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Code is a stable error number, e.g. 2003 for "header not found". Codes are never reused.
type Code uint16

// Class is the first digit of a Code. It decides the exit status (see ExitCode).
type Class uint8

const (
	ClassUsage      Class = 0
	ClassDefinition Class = 1
	ClassBuild      Class = 2
	ClassFetch      Class = 3
	ClassCache      Class = 4 // cache and server
	ClassUpstream   Class = 5
	ClassResource   Class = 6
	ClassStore      Class = 7 // store and host setup
	ClassInterrupt  Class = 8
	ClassInternal   Class = 9
)

var classNames = [...]string{"usage", "definition", "build", "fetch", "cache", "upstream",
	"resource", "store", "interrupt", "internal"}

// String returns the class name used in SPEC §02.2 ("build", "cache", …).
func (c Class) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return fmt.Sprintf("class%d", uint8(c))
}

// CodeNotImplemented marks a command or mode that is not built yet (BOB-E-9008, internal).
const CodeNotImplemented Code = 9008

// Severity is "error" or "warning".
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Pos is a source position in the repository, relative to the repo root ("libs/foo/bob.go:12:3").
type Pos struct {
	File      string
	Line, Col int
}

// Error is one diagnostic. Message and Hint come from the code's templates filled with Args.
type Error struct {
	Code      Code
	Severity  Severity
	Label     string            // package label, "" when not about a package
	DefinedAt Pos               // where the package was defined (bob.go position)
	Location  *Pos              // compiler diagnostic position, when one exists
	Cause     string            // one line
	Excerpt   []string          // ≤ 12 lines; store paths already rewritten (§02.5 rule 3)
	Args      map[string]string // template arguments
	Log       string            // "bob log //x:y" command, when a log exists
	Raw       []string          // raw Nix/tool text; output only with -v
}

var index = func() map[Code]*Entry {
	m := make(map[Code]*Entry, len(table))
	for i := range table {
		m[table[i].Code] = &table[i]
	}
	return m
}()

// Lookup returns the table entry for code.
func Lookup(code Code) (Entry, bool) {
	e, ok := index[code]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Codes returns every code in the table, ascending.
func Codes() []Code {
	cs := make([]Code, len(table))
	for i, e := range table {
		cs[i] = e.Code
	}
	return cs
}

var argRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// TemplateArgs returns the argument names of a template, sorted and unique.
func TemplateArgs(tmpl string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range argRe.FindAllStringSubmatch(tmpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func fill(tmpl string, args map[string]string) string {
	return argRe.ReplaceAllStringFunc(tmpl, func(s string) string { return args[s[1:len(s)-1]] })
}

func hasAll(tmpl string, args map[string]string) bool {
	for _, a := range TemplateArgs(tmpl) {
		if _, ok := args[a]; !ok {
			return false
		}
	}
	return true
}

// variant returns the variant selected by args (the "variant" argument when the code has more
// than one).
func (e *Entry) variant(args map[string]string) (*Variant, error) {
	if len(e.Variants) == 1 {
		return &e.Variants[0], nil
	}
	name := args["variant"]
	for i := range e.Variants {
		if e.Variants[i].Name == name {
			return &e.Variants[i], nil
		}
	}
	return nil, fmt.Errorf("BOB-E-%04d: unknown variant %q", uint16(e.Code), name)
}

func build(sev Severity, code Code, args []string) *Error {
	ent, ok := index[code]
	if !ok {
		panic(fmt.Sprintf("diag: unknown code %d", code)) // R-NIX-028: constant codes only
	}
	if len(args)%2 != 0 {
		panic(fmt.Sprintf("diag: BOB-E-%04d: odd number of arguments", uint16(code)))
	}
	m := make(map[string]string, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		m[args[i]] = args[i+1]
	}
	v, err := ent.variant(m)
	if err != nil {
		panic("diag: " + err.Error())
	}
	for _, a := range TemplateArgs(v.Message) {
		if _, ok := m[a]; !ok {
			panic(fmt.Sprintf("diag: BOB-E-%04d: missing argument %q", uint16(code), a))
		}
	}
	return &Error{Code: code, Severity: sev, Label: m["label"], Args: m}
}

// New returns an Error for code with the template arguments in args (key, value, key, value…).
// It panics if code is not in the table or an argument of the template is missing (tested).
// Codes with several message variants (1024, 1025, 4009) take the argument "variant".
//
//	err := diag.New(2003, "label", "//api:api", "header", "rcore.h")
//	err.Error() == "//api:api: header rcore.h not found"
func New(code Code, args ...string) *Error { return build(SeverityError, code, args) }

// Warn is New with Severity "warning".
//
//	w := diag.Warn(4001, "cache", "bob.example.com")
func Warn(code Code, args ...string) *Error { return build(SeverityWarning, code, args) }

// NotImplemented returns the error every not-yet-built feature returns (BOB-E-9008).
//
//	err := diag.NotImplemented("bob build")
//	err.Error() == "bob build is not implemented yet"
func NotImplemented(feature string) *Error { return New(CodeNotImplemented, "feature", feature) }

// ID returns the code in the form "BOB-E-NNNN".
func (c Code) ID() string { return fmt.Sprintf("BOB-E-%04d", uint16(c)) }

// Error returns the filled message template (the first line of the human block).
func (e *Error) Error() string {
	ent, ok := index[e.Code]
	if !ok {
		return e.Code.ID()
	}
	v, err := ent.variant(e.Args)
	if err != nil {
		return e.Code.ID()
	}
	return fill(v.Message, e.Args)
}

// Hint returns the filled default hint: the first hint template whose arguments are all in
// Args, or "" when there is none.
func (e *Error) Hint() string {
	ent, ok := index[e.Code]
	if !ok {
		return ""
	}
	v, err := ent.variant(e.Args)
	if err != nil {
		return ""
	}
	for _, h := range v.Hints {
		if hasAll(h, e.Args) {
			return strings.TrimSpace(fill(h, e.Args))
		}
	}
	return ""
}

// Class returns the code's class.
func (c Code) Class() Class { return Class(c / 1000) }

// exitPriority lists classes from highest to lowest priority with their exit status (§02.2).
var exitPriority = []struct {
	class Class
	exit  int
}{
	{ClassInterrupt, 130},
	{ClassInternal, 70},
	{ClassStore, 7},
	{ClassResource, 6},
	{ClassCache, 4},
	{ClassFetch, 4},
	{ClassUpstream, 5},
	{ClassDefinition, 3},
	{ClassBuild, 1},
	{ClassUsage, 2},
}

// ExitCode returns the exit status for a set of errors, using the priority order of §02.2.
// No errors (or warnings only) → 0.
//
//	diag.ExitCode([]*diag.Error{diag.New(2001, …), diag.New(4003, …)}) == 4
func ExitCode(errs []*Error) int {
	for _, p := range exitPriority {
		for _, e := range errs {
			if e != nil && e.Severity != SeverityWarning && e.Code.Class() == p.class {
				return p.exit
			}
		}
	}
	return 0
}
