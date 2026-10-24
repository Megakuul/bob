package ir

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// The evaluator's side channel (R-FW-043.6): besides the IR on fd 3 it writes diagnostics to
// stderr, one per line, as
//
//	bob:: {"type":"diag","severity":"error","code":"BOB-E-1006","label":"//a:b","file":"a/bob.go","line":3,"msg":"…"}
//
// This file turns them, and the other failures of an evaluation, into bob errors. It lives in
// E0 because the orchestration layer cannot import diag (R-ARCH-003).

// Errors is every bob error of one failed evaluation, in report order.
type Errors []*diag.Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Code.ID() + ": " + e.Error()
	}
	return strings.Join(lines, "\n")
}

// Unwrap lets errors.As find the single errors.
func (es Errors) Unwrap() []error {
	out := make([]error, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// DiagPrefix starts a diagnostic line of the evaluator.
const DiagPrefix = "bob:: "

// maxDiagLine bounds one diagnostic line; longer lines are not diagnostics.
const maxDiagLine = 64 << 10

type diagLine struct {
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Label    string `json:"label"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col,omitempty"`
	Hint     string `json:"hint,omitempty"` // a hint of the error's own (BOB-E-1020 "{hint}")
	Msg      string `json:"msg"`
	// Chain is the path from a requested package to the one the error is about (BOB-E-0006).
	Chain []string `json:"chain"`
}

// maxChain bounds the dependency chain shown with an error (hostile evaluator output).
const maxChain = 64

var codeRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^BOB-E-([0-9]{4})$`) })

// Diagnostics parses the evaluator's stderr. It returns the errors of its bob:: lines (file
// names passed through file, which maps evaluator positions to what the user sees) and the
// other lines, which are the evaluator's own output (user prints, a crash).
func Diagnostics(stderr []byte, file func(string) string) (Errors, []string) {
	var errs Errors
	var other []string
	sc := bufio.NewScanner(bytes.NewReader(stderr))
	sc.Buffer(make([]byte, 0, 4096), maxDiagLine)
	for sc.Scan() {
		line := sc.Text()
		rest, ok := strings.CutPrefix(line, DiagPrefix)
		var d diagLine
		if !ok || json.Unmarshal([]byte(rest), &d) != nil || d.Type != "diag" || d.Msg == "" {
			if strings.TrimSpace(line) != "" {
				other = append(other, line)
			}
			continue
		}
		pos := diag.Pos{Line: d.Line, Col: max(d.Col, 0)}
		if d.File != "" {
			pos.File = file(d.File)
		}
		e := evalDiag(d, pos)
		if d.Severity == string(diag.SeverityWarning) {
			e.Severity = diag.SeverityWarning
		}
		errs = append(errs, e)
	}
	return errs, other
}

// evalDiag maps one diagnostic line: a known code becomes that error with its template
// arguments recovered from the message; anything else is a definition error at its position.
func evalDiag(d diagLine, pos diag.Pos) *diag.Error {
	if m := codeRe().FindStringSubmatch(d.Code); m != nil {
		n, _ := strconv.Atoi(m[1])
		if args, ok := templateArgs(diag.Code(n), d.Msg); ok {
			if d.Label != "" && args["label"] == "" {
				args["label"] = d.Label
			}
			if d.Hint != "" && len(d.Hint) <= 500 {
				args["hint"] = d.Hint
			}
			e := &diag.Error{Code: diag.Code(n), Severity: diag.SeverityError, Label: args["label"], Args: args}
			if pos.File != "" {
				e.DefinedAt = pos
			}
			if len(d.Chain) > 1 && len(d.Chain) <= maxChain {
				e.Excerpt = []string{strings.Join(d.Chain, " → ")}
			}
			return e
		}
	}
	msg := d.Msg
	// pkg/bob writes code-less messages as "<file>:<line>: text"; the position is shown once.
	if pos.File != "" {
		msg = strings.TrimPrefix(msg, fmt.Sprintf("%s:%d: ", pos.File, pos.Line))
		msg = strings.TrimPrefix(msg, fmt.Sprintf("%s:%d: ", d.File, pos.Line))
	}
	return DefinitionError(pos.File, pos.Line, 0, d.Label, msg)
}

// templateArgs recovers the arguments of code's message template from a rendered message.
func templateArgs(code diag.Code, msg string) (map[string]string, bool) {
	ent, ok := diag.Lookup(code)
	if !ok {
		return nil, false
	}
	for _, v := range ent.Variants {
		re, names, err := templateRegexp(v.Message)
		if err != nil {
			continue
		}
		m := re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		args := map[string]string{}
		if v.Name != "" {
			args["variant"] = v.Name
		}
		for i, n := range names {
			if _, seen := args[n]; !seen {
				args[n] = m[i+1]
			}
		}
		return args, true
	}
	return nil, false
}

var templateArgRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`) })

func templateRegexp(tmpl string) (*regexp.Regexp, []string, error) {
	var b strings.Builder
	var names []string
	b.WriteString("^")
	last := 0
	for _, loc := range templateArgRe().FindAllStringSubmatchIndex(tmpl, -1) {
		b.WriteString(regexp.QuoteMeta(tmpl[last:loc[0]]))
		b.WriteString("(.*?)")
		names = append(names, tmpl[loc[2]:loc[3]])
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(tmpl[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return re, names, err
}

// DefinitionError is an error in the build definitions without a code of its own: a bob.go
// that does not compile, or an evaluation error of pkg/bob without a SPEC code. It is reported
// as BOB-E-1020 "<position or label>: invalid build definition: <message>" at the position.
func DefinitionError(file string, line, col int, label, msg string) *diag.Error {
	pos := diag.Pos{File: file, Line: line, Col: col}
	where := label
	if pos.File != "" {
		where = pos.File
		if pos.Line > 0 {
			where += ":" + strconv.Itoa(pos.Line)
			if pos.Col > 0 {
				where += ":" + strconv.Itoa(pos.Col)
			}
		}
	}
	if where == "" {
		where = "bob.go"
	}
	e := diag.New(1020, "label", where, "option", "build definition", "reason", msg)
	e.Label = label
	if pos.File != "" {
		p := pos
		e.Location = &p
	}
	return e
}

// RuleError is a violation of the bob.go rules R1–R7 (R-FW-002, BOB-E-1024); args are the
// template arguments of the rule's variant.
func RuleError(rule string, args ...string) *diag.Error {
	return diag.New(1024, append([]string{"variant", rule}, args...)...)
}

// Nondeterministic is BOB-E-1018: two evaluations of the same bob.go files differ (R-FW-008).
func Nondeterministic(label, field string) *diag.Error {
	return diag.New(1018, "label", label+" differs between two runs (field "+field+")")
}

// FrameworkTooOld is a dependency bob repository that needs a newer bob (R-FW-043.4).
func FrameworkTooOld(module, need, have string) *diag.Error {
	return ToolchainMismatch(module, "needs bob "+need+" or newer; this is bob "+have)
}

// ToolchainMismatch is BOB-E-1021.
func ToolchainMismatch(label, reason string) *diag.Error {
	return diag.New(1021, "label", label, "reason", reason)
}

// EvaluatorBug is an evaluation failure that only a bob bug can cause (internal, exit 70).
func EvaluatorBug(format string, args ...any) *diag.Error {
	return diag.New(9004, "label", "the evaluator", "reason", fmt.Sprintf(format, args...))
}
