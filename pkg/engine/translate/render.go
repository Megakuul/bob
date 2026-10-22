package translate

import (
	"fmt"
	"io"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Style decides how human output looks (SPEC §02.5 rules 6 and 7).
type Style struct {
	Color   bool // ANSI colours
	Glyphs  bool // ✓ ✗ ↓ glyphs (TTY only)
	Verbose bool // -v: raw Nix text as "nix:" lines under each block
}

// StyleFor returns the style for an output stream: colour only on a TTY whose TERM is not
// dumb, never with NO_COLOR set (any value); BOB_COLOR=always forces colour unless NO_COLOR is
// set. Glyphs only on a TTY whose TERM is not dumb.
func StyleFor(isTTY bool, getenv func(string) string, verbose bool) Style {
	dumb := getenv("TERM") == "dumb"
	s := Style{Color: isTTY && !dumb, Glyphs: isTTY && !dumb, Verbose: verbose}
	if getenv("BOB_COLOR") == "always" {
		s.Color = true
	}
	if getenv("NO_COLOR") != "" {
		s.Color = false
	}
	return s
}

const (
	cRedBold    = "\x1b[1;31m"
	cYellowBold = "\x1b[1;33m"
	cGreen      = "\x1b[32m"
	cYellow     = "\x1b[33m"
	cBlue       = "\x1b[34m"
	cCyan       = "\x1b[36m"
	cBold       = "\x1b[1m"
	cDim        = "\x1b[2m"
	cReset      = "\x1b[0m"
)

func (s Style) c(code, text string) string {
	if !s.Color {
		return text
	}
	return code + text + cReset
}

func (s Style) pick(glyph, word, color string) string {
	if s.Glyphs {
		return s.c(color, glyph)
	}
	return s.c(color, word)
}

// Human writes the final part of a build's human output: package lines, warning and error
// blocks, the summary (SPEC §02.5 rules 1–5). The live status line is not part of it.
func Human(w io.Writer, r *Report, s Style) { HumanTo(w, w, r, s) }

// HumanTo is Human with the package lines on out and, when the run has warning or error
// blocks, the blocks and the summary on errw (stdout followed by stderr reads as Human).
func HumanTo(out, errw io.Writer, r *Report, s Style) {
	w := out
	for _, p := range r.Packages {
		var mark string
		switch p.Status {
		case StBuilt:
			mark = s.pick("✓", "built  ", cGreen)
		case StCached:
			mark = s.pick("↓", "cached ", cGreen)
		case StFailed:
			mark = s.pick("✗", "FAILED ", cRedBold)
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
	blocks := append(append([]*diag.Error{}, r.Warnings...), r.Errors...)
	if len(blocks) > 0 {
		w = errw
	}
	for _, e := range blocks {
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		Block(w, e, r.Hint(e), s)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, summary(r, s))
}

// Block writes one error or warning block (SPEC §02.5 rule 2) with the given hint line:
//
//	error[BOB-E-2003]: //api:api: header rcore.h not found
//	  --> api/cmd/api/main.go:7:10
//	  package defined at api/bob.go:13:11
//	   | cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory
//	  hint: add the package that provides rcore.h to Deps
//	  full log: bob log //api:api
func Block(w io.Writer, e *diag.Error, hint string, s Style) {
	head, color := "error", cRedBold
	if e.Severity == diag.SeverityWarning {
		head, color = "warning", cYellowBold
	}
	if line, ok := definitionLine(e); ok {
		fmt.Fprintf(w, "%s %s\n", s.c(color, "✗"), s.c(cBold, line))
	} else if e.Code == cBuilderWarning && e.Severity == diag.SeverityWarning {
		// bob-builder's warnings read as SPEC writes them (R-OUT-019): `warning: <label>: <reason>`
		fmt.Fprintf(w, "%s %s\n", s.c(color, "warning:"), s.c(cBold, e.Error()))
	} else {
		fmt.Fprintf(w, "%s %s\n", s.c(color, head+"["+e.Code.ID()+"]:"), s.c(cBold, e.Error()))
	}
	if e.Location != nil && e.Location.File != "" {
		fmt.Fprintf(w, "  %s %s\n", s.c(cBlue, "-->"), PosString(*e.Location))
	}
	if e.DefinedAt.File != "" {
		fmt.Fprintf(w, "  package defined at %s\n", PosString(e.DefinedAt))
	}
	if e.Cause != "" {
		fmt.Fprintf(w, "  cause: %s\n", e.Cause)
	}
	bar := "|"
	if s.Glyphs {
		bar = "│"
	}
	for i, l := range e.Excerpt {
		if i == maxServiceTail {
			break
		}
		fmt.Fprintf(w, "   %s %s\n", s.c(cDim, bar), strings.TrimRight(l, " "))
	}
	if hint != "" {
		fmt.Fprintf(w, "  %s %s\n", s.c(cCyan, "hint:"), hint)
	}
	if e.Log != "" {
		fmt.Fprintf(w, "  %s\n", s.c(cDim, "full log: "+e.Log))
	}
	if s.Verbose {
		for _, r := range e.Raw {
			for l := range strings.SplitSeq(r, "\n") {
				if l = strings.TrimRight(l, " "); l != "" {
					fmt.Fprintf(w, "  %s %s\n", s.c(cDim, "nix:"), l)
				}
			}
		}
	}
}

// definitionLine is the first line of the errors SPEC writes as `✗ <message>` lines (§03.1.3
// R-CC-002a, §04 R-PLAT-006, §03.4.4): a package's invalid option (BOB-E-1020), a package that
// does not support the requested platform (BOB-E-0006), the platform usage errors (0008, 0010, 0011, §04 R-PLAT-001/005), the nixpkgs mismatch (1026) and the npm runtime translations
// (BOB-E-2013, BOB-E-2014). A definition error without a code of its
// own (option "build definition") shows its reason, since `-->` gives the position. Every other
// error keeps the `error[CODE]:` head of the S15 goldens.
func definitionLine(e *diag.Error) (string, bool) {
	if e.Severity == diag.SeverityWarning {
		return "", false
	}
	switch e.Code {
	case 6, 8, 10, 11, 1026, cPruned: // errors SPEC writes as `✗ <message>` (2015: R-OUT-007)
		return e.Error(), true
	case 1020:
		if e.Args["option"] == "build definition" && e.Location != nil && e.Args["reason"] != "" {
			return e.Args["reason"], true
		}
		return e.Error(), true
	case 1015: // R-ML-002: `✗ cannot resolve opam packages: <reason>`; other ecosystems keep the error[] head
		if e.Args["ecosystem"] == "opam" && e.Args["reason"] != "" {
			return "cannot resolve opam packages: " + e.Args["reason"], true
		}
		return "", false
	case cBuildFailed: // R-GEN-001a: a code generator failed
		if e.Args["phase"] == "codegen" {
			return fmt.Sprintf("%s code generation failed (defined at %s:%d)", e.Args["label"], e.DefinedAt.File, e.DefinedAt.Line), true
		}
		return "", false
	case 1021: // R-GEN-006: a generator's runtime contradicts an explicit pin (§03.6.4)
		if e.Args["runtime"] != "" && e.Args["reason"] != "" {
			return e.Args["label"] + ": " + e.Args["reason"], true
		}
		return "", false
	case cScriptDenied: // §03.4.4 runtime translations
		return fmt.Sprintf("%s needs its %s script (%s). Add npm.AllowScripts(%q) to %s",
			e.Args["package"], e.Args["script"], e.Args["reason"], e.Args["package"], e.Args["label"]), true
	case cPhantomDep:
		return fmt.Sprintf("%s uses %s without declaring it. Add npm.Extend(%q, npm.Peer(%q))",
			e.Args["package"], e.Args["dep"], e.Args["package"], e.Args["dep"]), true
	}
	return "", false
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
	add(r.BuiltPackages, "built") // packages; counts.built (--json) counts derivations
	add(r.Counts.Cached, "cached")
	if len(parts) == 0 {
		parts = []string{"nothing to do"}
		if r.ExitCode != 0 {
			parts = []string{"nothing was built"}
		}
	}
	text := "bob: " + strings.Join(parts, ", ")
	if r.ExitCode != 0 {
		return s.c(cRedBold, text) + fmt.Sprintf(" (exit %d)", r.ExitCode)
	}
	return s.c(cGreen, text)
}

// FinalEvents returns the events that end a run: package.finish per package, then run.finish.
func FinalEvents(r *Report) []Event {
	evs := make([]Event, 0, len(r.Packages)+1)
	for _, p := range r.Packages {
		evs = append(evs, Event{Event: "package.finish", Label: p.Label, Status: p.Status})
	}
	return append(evs, Event{Event: "run.finish", ExitCode: r.ExitCode, Counts: r.Counts, Drift: r.Drift})
}
