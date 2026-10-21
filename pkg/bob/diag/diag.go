// Package diag defines bob's user-facing errors. Every error a user sees is a *Error with a
// stable Code; raw tool output is kept in Raw and shown only with -v.
//
// This is the minimal M0.1 version: types and exit codes. M0.7 adds the complete code table,
// templates, New and Warn (SPEC §02.3).
package diag

import "fmt"

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

// CodeNotImplemented marks a feature that is not built yet. It is a scaffolding code outside
// the SPEC table and is removed before v1 (M0.7 decides its final form).
const CodeNotImplemented Code = 9000

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

// NotImplemented returns the error every not-yet-built feature returns.
//
//	err := diag.NotImplemented("bob build")
//	err.Error() == "bob build is not implemented yet"
func NotImplemented(feature string) *Error {
	return &Error{
		Code:     CodeNotImplemented,
		Severity: SeverityError,
		Args:     map[string]string{"feature": feature},
	}
}

// Error returns the filled message template (the first line of the human block).
func (e *Error) Error() string {
	if e.Code == CodeNotImplemented {
		return e.Args["feature"] + " is not implemented yet"
	}
	return fmt.Sprintf("BOB-E-%04d", uint16(e.Code))
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
