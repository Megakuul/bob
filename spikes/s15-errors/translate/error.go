package translate

import (
	"sort"
	"strings"
)

// Error is one translated, user-facing bob error.
type Error struct {
	Code      Code              `json:"code"`
	Severity  string            `json:"severity,omitempty"` // "" = error, "warning" = the run still succeeded
	Class     Class             `json:"class"`
	Message   string            `json:"message"`             // rendered title
	Label     string            `json:"label,omitempty"`     // bob package
	DefinedAt string            `json:"definedAt,omitempty"` // Go source position of the package definition
	Location  string            `json:"location,omitempty"`  // position in the user's sources (compiler diagnostic)
	Cause     string            `json:"cause,omitempty"`     // one line: the underlying cause in bob terms
	Excerpt   []string          `json:"excerpt,omitempty"`   // relevant log lines, store paths mapped to repo paths
	Hint      string            `json:"hint,omitempty"`
	LogRef    string            `json:"log,omitempty"` // command that shows the full log
	Args      map[string]string `json:"args,omitempty"`
	Raw       []string          `json:"raw,omitempty"` // raw Nix messages; rendered only with -v
}

func newErr(c Code, args map[string]string) *Error {
	s := SpecOf(c)
	if args == nil {
		args = map[string]string{}
	}
	e := &Error{Code: c, Class: s.Class, Args: args}
	e.Message = fill(s.Title, args)
	if s.Hint != "" {
		e.Hint = fill(s.Hint, args)
	}
	e.Label = args["label"]
	return e
}

// NewError builds an error from the catalogue (used by Go pre-checks too).
func NewError(c Code, args map[string]string) *Error { return newErr(c, args) }

func fill(t string, args map[string]string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t = strings.ReplaceAll(t, "{"+k+"}", args[k])
	}
	return t
}

// ExitCode for a set of errors (0 if none).
func ExitCode(errs []*Error) int {
	if len(errs) == 0 {
		return 0
	}
	have := map[Class]bool{}
	for _, e := range errs {
		have[e.Class] = true
	}
	for _, c := range classPriority {
		if have[c] {
			return ExitCodes[c]
		}
	}
	return 70
}
