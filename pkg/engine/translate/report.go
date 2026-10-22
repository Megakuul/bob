package translate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Status is a package's state in a run (SPEC §02.4 package.finish).
type Status string

const (
	StPending   Status = "pending"
	StBuilding  Status = "building"
	StBuilt     Status = "built"
	StCached    Status = "cached" // downloaded from a binary cache
	StFailed    Status = "failed"
	StSkipped   Status = "skipped" // a dependency failed
	StCancelled Status = "cancelled"
)

// PkgState is one package's result.
type PkgState struct {
	Label     string
	DefinedAt string
	Status    Status
	Phase     string // last phase the builder reported
	drv       string
}

// Counts summarises a run (SPEC §02.4 run.finish counts).
type Counts struct {
	Built      int   `json:"built"`
	Cached     int   `json:"cached"`
	Failed     int   `json:"failed"`
	Skipped    int   `json:"skipped"`
	Cancelled  int   `json:"cancelled"`
	Upstream   int   `json:"upstream"`   // nixpkgs packages built from source
	Downloaded int64 `json:"downloaded"` // bytes
}

// Report is the result of one translated Nix run.
type Report struct {
	Errors   []*diag.Error // severity error, in output order
	Warnings []*diag.Error // severity warning (the run may still succeed)
	Packages []*PkgState   // in output order (failed, stopped, skipped, built, cached; by label)
	Drift    []string      // log-format surprises (unknown types or keys, unparsable lines)
	ExitCode int
	Counts   Counts

	// hints holds hint texts that differ from the code's default hint (the error table hint
	// extended with what bob knows in this run, e.g. "bob built locally instead").
	hints map[*diag.Error]string
}

// Hint returns the hint line of e: the run-specific hint if the translator set one, else the
// code's default hint.
func (r *Report) Hint(e *diag.Error) string {
	if h, ok := r.hints[e]; ok {
		return h
	}
	return e.Hint()
}

// Event is one progress or result event of a run, in the order it happened. The CLI turns
// events into `--json` lines (SPEC §02.4) and adds the envelope fields (time, command, versions).
type Event struct {
	Event     string      // run.start | package.start | package.wait | upstream.start | log | error | warning | package.finish | run.finish
	Label     string      // package events, log
	Action    string      // package.start: build | download
	Cache     string      // package.start: substituter name, "" for builds
	Name      string      // upstream.start: nixpkgs package name
	Reason    string      // package.wait
	Line      string      // log: one build log line, store paths already rewritten
	Status    Status      // package.finish
	Error     *diag.Error // error, warning
	Hint      string      // error, warning: the hint line (Report.Hint)
	Requested []string    // run.start
	ExitCode  int         // run.finish
	Counts    Counts      // run.finish
	Drift     []string    // run.finish
}

// ErrorObject is the `error` object of the `--json` schema v1 (SPEC §02.4); every field is
// always present. Raw Nix text is not part of it: it goes to the event's `raw[]`, only with -v.
type ErrorObject struct {
	Code      string            `json:"code"`
	Severity  string            `json:"severity"`
	Class     string            `json:"class"`
	Message   string            `json:"message"`
	Label     string            `json:"label"`
	DefinedAt string            `json:"definedAt"`
	Location  *string           `json:"location"`
	Cause     string            `json:"cause"`
	Excerpt   []string          `json:"excerpt"`
	Hint      string            `json:"hint"`
	Log       string            `json:"log"`
	Args      map[string]string `json:"args"`
}

// NewErrorObject returns the schema object of e with the given hint line.
func NewErrorObject(e *diag.Error, hint string) ErrorObject {
	o := ErrorObject{
		Code:      e.Code.ID(),
		Severity:  string(e.Severity),
		Class:     e.Code.Class().String(),
		Message:   e.Error(),
		Label:     e.Label,
		DefinedAt: PosString(e.DefinedAt),
		Cause:     e.Cause,
		Excerpt:   e.Excerpt,
		Hint:      hint,
		Log:       e.Log,
		Args:      e.Args,
	}
	if o.Severity == "" {
		o.Severity = string(diag.SeverityError)
	}
	if e.Location != nil {
		s := PosString(*e.Location)
		o.Location = &s
	}
	if o.Excerpt == nil {
		o.Excerpt = []string{}
	}
	if o.Args == nil {
		o.Args = map[string]string{}
	}
	return o
}

// PosString renders a position as "file:line:col" ("file" alone when the line is unknown).
func PosString(p diag.Pos) string {
	switch {
	case p.File == "":
		return ""
	case p.Line == 0:
		return p.File
	case p.Col == 0:
		return fmt.Sprintf("%s:%d", p.File, p.Line)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// ParsePos parses "file:line:col" or "file:line"; anything else is a position with only a file.
func ParsePos(s string) diag.Pos {
	if s == "" {
		return diag.Pos{}
	}
	rest, last, ok := cutLast(s)
	n1, err1 := strconv.Atoi(last)
	if !ok || err1 != nil || n1 <= 0 {
		return diag.Pos{File: s}
	}
	file, mid, ok := cutLast(rest)
	if n2, err := strconv.Atoi(mid); ok && err == nil && n2 > 0 {
		return diag.Pos{File: file, Line: n2, Col: n1}
	}
	return diag.Pos{File: rest, Line: n1}
}

func cutLast(s string) (before, after string, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}
