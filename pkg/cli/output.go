package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// out is the output of one command (SPEC §02.4, §02.5): human text on stdout, or schema-v1
// NDJSON events on stdout with --json and nothing else there. Every command ends with finish,
// which emits run.finish in --json mode and returns the exit status.
type out struct {
	w        io.Writer // stdout
	errw     io.Writer // stderr: error/warning blocks (and the summary after them), -vv debug lines, the live status line
	json     bool
	jsonLog  bool
	quiet    bool
	verbose  int // 1 = -v, 2 = -vv
	style    translate.Style
	isTTY    bool
	start    time.Time
	command  string
	args     []string
	started  bool
	finished bool
	errs     []*diag.Error // errors and warnings reported so far
	now      func() time.Time
}

// timeFormat is RFC 3339 UTC with milliseconds (SPEC §02.4).
const timeFormat = "2006-01-02T15:04:05.000Z"

// event writes one --json line: {"v":1,"event":name,"time":…} followed by the fields of
// fields (a struct or map that encodes to a JSON object). It does nothing in human mode.
func (o *out) event(name string, fields any) {
	if !o.json {
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"v":1,"event":%q,"time":%q`, name, o.now().UTC().Format(timeFormat))
	if fields != nil {
		body, err := json.Marshal(fields)
		if err == nil && len(body) > 2 && body[0] == '{' {
			b.WriteByte(',')
			b.Write(body[1 : len(body)-1])
		}
	}
	b.WriteString("}\n")
	_, _ = o.w.Write(b.Bytes())
}

// begin emits run.start once (SPEC §02.4: the first line of every --json run).
func (o *out) begin(requested []string) {
	if o.started {
		return
	}
	o.started = true
	if requested == nil {
		requested = []string{}
	}
	o.event("run.start", struct {
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Requested []string `json:"requested"`
		Bob       string   `json:"bob"`
		Nix       string   `json:"nix"`
	}{o.command, o.args, requested, bobVersion(), nixVersion})
}

// result emits the `result` event of a query command.
func (o *out) result(data any) {
	o.begin(nil)
	o.event("result", struct {
		Command string `json:"command"`
		Data    any    `json:"data"`
	}{o.command, data})
}

// diagnose reports one error or warning: an error/warning event, or a human block. hint ""
// means the code's default hint.
func (o *out) diagnose(e *diag.Error, hint string) {
	if hint == "" {
		hint = e.Hint()
	}
	o.errs = append(o.errs, e)
	if o.json {
		o.begin(nil)
		o.event(eventName(e), errorEvent(e, hint, o.verbose > 0))
		return
	}
	if e.Code == 7005 {
		o.refusal(e)
		return
	}
	translate.Block(o.errw, e, hint, o.style)
}

// refusal renders BOB-E-7005 in the layout of SPEC R-NIX-022: the cause on its own line and
// the fix indented by four spaces under "fix (run once as an administrator):".
func (o *out) refusal(e *diag.Error) {
	fmt.Fprintf(o.errw, "%s %s\n", paint(o.style, sgrRedBold, "✗"), e.Error())
	if e.Cause != "" {
		fmt.Fprintf(o.errw, "  cause: %s\n", e.Cause)
	}
	fmt.Fprintln(o.errw, "  fix (run once as an administrator):")
	for _, l := range strings.Split(e.Args["fix"], "\n") {
		fmt.Fprintf(o.errw, "    %s\n", l)
	}
	if o.style.Verbose {
		for _, r := range e.Raw {
			fmt.Fprintf(o.errw, "  nix: %s\n", r)
		}
	}
}

func eventName(e *diag.Error) string {
	if e.Severity == diag.SeverityWarning {
		return "warning"
	}
	return "error"
}

// errorEvent is the body of an error/warning event; raw[] only with -v (SPEC §02.4).
func errorEvent(e *diag.Error, hint string, verbose bool) any {
	type ev struct {
		Error translate.ErrorObject `json:"error"`
		Raw   []string              `json:"raw,omitempty"`
	}
	v := ev{Error: translate.NewErrorObject(e, hint)}
	if verbose && len(e.Raw) > 0 {
		v.Raw = e.Raw
	}
	return v
}

// finish ends the run: run.finish (--json) with the exit status, which it returns. exit < 0
// means "derive it from the errors reported" (diag.ExitCode).
func (o *out) finish(exit int, counts translate.Counts, drift []string) int {
	if exit < 0 {
		exit = diag.ExitCode(o.errs)
	}
	if o.finished {
		return exit
	}
	o.finished = true
	if drift == nil {
		drift = []string{}
	}
	o.begin(nil)
	o.event("run.finish", struct {
		ExitCode   int              `json:"exitCode"`
		Counts     translate.Counts `json:"counts"`
		Drift      []string         `json:"drift"`
		DurationMs int64            `json:"durationMs"`
	}{exit, counts, drift, o.now().Sub(o.start).Milliseconds()})
	return exit
}

// fail reports err (a *diag.Error, ir.Errors, or anything else as an internal error) and ends
// the run with the exit status of everything reported.
func (o *out) fail(err error) int {
	for _, e := range diagErrors(err) {
		o.diagnose(e, "")
	}
	return o.finish(-1, translate.Counts{}, nil)
}

// printf writes human text (not in --json mode).
func (o *out) printf(format string, a ...any) {
	if o.json {
		return
	}
	fmt.Fprintf(o.w, format, a...)
}

const sgrRedBold = "\x1b[1;31m"

// paint wraps text in an SGR colour when the style has colour on.
func paint(s translate.Style, sgr, text string) string {
	if !s.Color {
		return text
	}
	return sgr + text + "\x1b[0m"
}
