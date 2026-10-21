package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Style decides how human output looks.
type Style struct {
	Color   bool // ANSI colors
	Glyphs  bool // ✓ ✗ symbols and the live status line (TTY only)
	Verbose bool // -v: raw Nix messages under each error
}

// StyleFor returns the style for an output stream [proposal]:
// colors only on a TTY, never with NO_COLOR set (any value) or TERM=dumb; glyphs and the live
// status line only on a TTY; BOB_COLOR=always forces colors (CI logs that render ANSI).
func StyleFor(isTTY bool, getenv func(string) string, verbose bool) Style {
	s := Style{Color: isTTY, Glyphs: isTTY, Verbose: verbose}
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		s.Color = false
	}
	if getenv("TERM") == "dumb" {
		s.Glyphs = false
	}
	if getenv("BOB_COLOR") == "always" && getenv("NO_COLOR") == "" {
		s.Color = true
	}
	return s
}

const (
	cRed    = "\x1b[31;1m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[2m"
	cReset  = "\x1b[0m"
)

func (s Style) c(code, text string) string {
	if !s.Color {
		return text
	}
	return code + text + cReset
}

// Human writes the final part of `bob build` output: package results, error blocks, summary.
// (The live status line on a TTY is not part of the golden output.)
func Human(w io.Writer, r *Report, s Style) {
	for _, p := range r.Packages {
		var mark string
		switch p.Status {
		case StBuilt:
			mark = s.pick("✓", "built  ", cGreen)
		case StSubstituted:
			mark = s.pick("↓", "cached ", cGreen)
		case StFailed:
			mark = s.pick("✗", "FAILED ", cRed)
		case StSkipped:
			mark = s.pick("-", "skipped", cYellow)
		case StCancelled:
			mark = s.pick("-", "stopped", cYellow)
		default:
			continue
		}
		line := mark + " " + p.Label
		if p.Status == StSkipped {
			line += s.c(cDim, "  (a dependency failed)")
		}
		fmt.Fprintln(w, line)
	}
	first := len(r.Packages) == 0
	for _, e := range append(append([]*Error{}, r.Warnings...), r.Errors...) {
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		ErrorBlock(w, e, s)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, summary(r, s))
}

func (s Style) pick(glyph, word, color string) string {
	if s.Glyphs {
		return s.c(color, glyph)
	}
	return s.c(color, word)
}

// ErrorBlock renders one error [proposal: exact layout].
//
//	error[BOB-E-2001]: //api:api: compile error in fixture/api/cmd/api/main.go
//	  --> fixture/api/cmd/api/main.go:59:28
//	  package defined at fixture/api/bob.go:13:11
//	  cause: cannot use "not a number" (untyped string constant) as int value in return statement
//	   │ cmd/api/main.go:59:28: cannot use …
//	  hint: …
//	  full log: bob log //api:api
func ErrorBlock(w io.Writer, e *Error, s Style) {
	if e.Severity == "warning" {
		fmt.Fprintf(w, "%s %s\n", s.c(cYellow, "warning["+string(e.Code)+"]:"), s.c(cBold, e.Message))
	} else {
		fmt.Fprintf(w, "%s %s\n", s.c(cRed, "error["+string(e.Code)+"]:"), s.c(cBold, e.Message))
	}
	if e.Location != "" {
		fmt.Fprintf(w, "  %s %s\n", s.c(cCyan, "-->"), e.Location)
	}
	if e.DefinedAt != "" {
		fmt.Fprintf(w, "  package defined at %s\n", e.DefinedAt)
	}
	if e.Cause != "" {
		fmt.Fprintf(w, "  cause: %s\n", e.Cause)
	}
	bar := "|"
	if s.Glyphs {
		bar = "│"
	}
	for _, l := range e.Excerpt {
		fmt.Fprintf(w, "   %s %s\n", s.c(cDim, bar), strings.TrimRight(l, " "))
	}
	if e.Hint != "" {
		fmt.Fprintf(w, "  %s %s\n", s.c(cGreen, "hint:"), e.Hint)
	}
	if e.LogRef != "" {
		fmt.Fprintf(w, "  %s\n", s.c(cDim, "full log: "+e.LogRef))
	}
	if s.Verbose {
		for _, r := range e.Raw {
			if r == "" {
				continue
			}
			for _, l := range strings.Split(r, "\n") {
				fmt.Fprintf(w, "  %s %s\n", s.c(cDim, "nix:"), strings.TrimRight(l, " "))
			}
		}
	}
}

func summary(r *Report, s Style) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(r.Counts.Failed, "failed")
	add(r.Counts.Skipped, "skipped")
	add(r.Counts.Cancelled, "stopped")
	add(r.Counts.Built, "built")
	add(r.Counts.Cached, "cached")
	if len(parts) == 0 {
		parts = []string{"nothing to do"}
		if r.ExitCode != 0 {
			parts = []string{"nothing was built"}
		}
	}
	head := "bob: "
	if r.ExitCode != 0 {
		return s.c(cRed, head+strings.Join(parts, ", ")) + fmt.Sprintf(" (exit %d)", r.ExitCode)
	}
	return s.c(cGreen, head+strings.Join(parts, ", "))
}

// JSONLines writes events as NDJSON.
func JSONLines(w io.Writer, evs []Event) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, e := range evs {
		e.V = SchemaVersion
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// FinalEvents are emitted when the run ends (after the live events).
func FinalEvents(r *Report) []Event {
	var evs []Event
	for _, p := range r.Packages {
		evs = append(evs, Event{Event: "package.finish", Label: p.Label, Status: p.Status})
	}
	code := r.ExitCode
	evs = append(evs, Event{Event: "run.finish", ExitCode: &code, Counts: &r.Counts, Drift: r.Drift})
	return evs
}
