package translate

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Build-log patterns (ADR 0017 Decision 4, step 3). They come from compilers and tools, not
// from Nix, so they drift with toolchain versions, not with the Nix version.
var (
	reCDiag = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^(\S+?\.(?:c|cc|cpp|cxx|h|hpp|hh|go|m|S)):(\d+):(\d+): (?:(fatal error|error): )?(.*)$`)
	})
	reRustDiag   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^error(\[E\d+\])?: (.*)$`) })
	reRustLoc    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^\s*--> (\S+):(\d+):(\d+)`) })
	reUndefRef   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile("undefined reference to `([^']+)'") })
	reNoLib      = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`cannot find -l(\S+?)(:|$)`) })
	reNoHeader   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`fatal error: ([^:]+): No such file or directory`) })
	reGoTooOld   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`go: (\S+) requires go >= (\S+) \(running go (\S+);`) })
	reGoTestFail = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^--- FAIL: (\S+)`) })
	reRustCrate  = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile("(?:unresolved module or unlinked crate|can't find crate for) `([^`]+)`")
	})
	reExecFail = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^error: executing '([^']+)': (.*)$`) })
	reShSyntax = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(?:bash|sh): (?:-c: )?line \d+: syntax error`) })
	reNetwork  = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`Network is unreachable|Could not resolve host|Temporary failure in name resolution|Name or service not known`)
	})
	reDLFailed   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`error: unable to download '([^']+)': (.*)$`) })
	reBobBuilder = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^bob-builder: `) })
	reBobDiag    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^bob:: (\{.*\})\s*$`) })
	reDiagTimout = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(\S+) timed out after (\d+)s$`) })
)

// classifyLog explains a builder that exited non-zero, from its own log.
func (t *Translator) classifyLog(drv string, p Pkg, exit string) *diag.Error {
	raw := t.logs[drv].lines()
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = t.mapPaths(l)
	}
	if p.Kind == "fetch" && p.Role == "" {
		return t.fetchFailed(drv, p, lines)
	}
	if p.Role == "unpack" {
		e := t.pkgErr(cBuildFailed, drv, nil)
		e.Cause = "unpacking the download failed"
		e.Excerpt = lastN(lines, 8)
		return e
	}
	// bob's own builder is broken (a bob bug)
	for _, l := range lines {
		if m := reExecFail().FindStringSubmatch(l); m != nil {
			return t.pkgErr(cBuilderBroken, drv, map[string]string{"reason": "cannot execute " + describeExe(m[1]) + ": " + m[2]})
		}
		if reShSyntax().MatchString(l) {
			e := t.pkgErr(cBuilderBroken, drv, map[string]string{"reason": "its build script has a syntax error"})
			e.Excerpt = []string{l}
			return e
		}
	}
	for _, l := range lines {
		if reNetwork().MatchString(l) {
			e := t.pkgErr(cNetwork, drv, nil)
			e.Excerpt = []string{l}
			return e
		}
	}
	if strings.HasPrefix(p.Kind, "npm.") {
		if e := t.npmLogError(drv, lines); e != nil {
			return e
		}
	}
	if e := t.testFailure(drv, lines); e != nil {
		return e
	}
	if e := t.builderDiag(drv, p, lines); e != nil {
		return e
	}
	for _, l := range lines {
		if m := reGoTooOld().FindStringSubmatch(l); m != nil {
			e := t.pkgErr(cGoTooOld, drv, map[string]string{"module": m[1], "want": m[2], "have": m[3]})
			e.Excerpt = []string{l}
			return e
		}
	}
	// link errors before compile diagnostics: "collect2: error" is a link error
	for _, l := range lines {
		if m := reUndefRef().FindStringSubmatch(l); m != nil {
			e := t.pkgErr(cUndefinedSymbol, drv, map[string]string{"symbol": m[1]})
			e.Excerpt = grep(lines, func(s string) bool { return reUndefRef().MatchString(s) || strings.Contains(s, ": in function") }, 6)
			return e
		}
		if m := reNoLib().FindStringSubmatch(l); m != nil {
			e := t.pkgErr(cLibMissing, drv, map[string]string{"lib": m[1]})
			e.Excerpt = []string{shortTool(l)}
			return e
		}
	}
	for i, l := range lines {
		if m := reNoHeader().FindStringSubmatch(l); m != nil {
			e := t.pkgErr(cHeaderMissing, drv, map[string]string{"header": m[1]})
			if d := reCDiag().FindStringSubmatch(l); d != nil {
				e.Location = t.location(p, d[1], d[2], d[3])
			}
			e.Excerpt = block(lines, i, 4)
			return e
		}
	}
	if e := t.compileError(drv, p, lines); e != nil {
		return e
	}
	e := t.pkgErr(cBuildFailed, drv, nil)
	e.Cause = "the build step exited with status " + exit
	var tail []string
	for _, l := range lines {
		if !reBobBuilder().MatchString(l) {
			tail = append(tail, l)
		}
	}
	e.Excerpt = lastN(tail, 10)
	return e
}

func (t *Translator) testFailure(drv string, lines []string) *diag.Error {
	for i, l := range lines {
		m := reGoTestFail().FindStringSubmatch(l)
		if m == nil {
			continue
		}
		e := t.pkgErr(cTestsFailed, drv, nil)
		e.Cause = "test " + m[1] + " failed"
		for _, x := range lines[i:] {
			if strings.HasPrefix(x, "ok ") || strings.HasPrefix(x, "?") || reBobBuilder().MatchString(x) {
				continue
			}
			e.Excerpt = append(e.Excerpt, x)
			if len(e.Excerpt) == 10 {
				break
			}
		}
		return e
	}
	return nil
}

func (t *Translator) compileError(drv string, p Pkg, lines []string) *diag.Error {
	for i, l := range lines {
		if m := reCDiag().FindStringSubmatch(l); m != nil && (m[4] != "" || strings.HasSuffix(m[1], ".go")) {
			if strings.HasPrefix(m[5], "note:") || strings.HasPrefix(m[5], "warning:") {
				continue
			}
			loc := t.location(p, m[1], m[2], m[3])
			e := t.pkgErr(cCompile, drv, map[string]string{"file": loc.File})
			e.Location = loc
			e.Excerpt = diagnostics(lines, i)
			return e
		}
		if m := reRustDiag().FindStringSubmatch(l); m != nil && !strings.HasPrefix(m[2], "aborting due to") {
			loc := t.rustLoc(p, lines, i)
			if c := reRustCrate().FindStringSubmatch(strings.Join(lines[i:min(len(lines), i+6)], "\n")); c != nil {
				e := t.pkgErr(cRustCrate, drv, map[string]string{"crate": c[1]})
				e.Excerpt = block(lines, i, 6)
				e.Location = loc
				return e
			}
			file := "?"
			if loc != nil {
				file = loc.File
			}
			e := t.pkgErr(cCompile, drv, map[string]string{"file": file})
			e.Location = loc
			e.Excerpt = block(lines, i, 8)
			return e
		}
	}
	return nil
}

func (t *Translator) location(p Pkg, file, line, col string) *diag.Pos {
	l, _ := strconv.Atoi(line)
	c, _ := strconv.Atoi(col)
	return &diag.Pos{File: t.repoPath(p, file), Line: l, Col: c}
}

func (t *Translator) rustLoc(p Pkg, lines []string, i int) *diag.Pos {
	for _, l := range lines[i:min(len(lines), i+3)] {
		if m := reRustLoc().FindStringSubmatch(l); m != nil {
			return t.location(p, m[1], m[2], m[3])
		}
	}
	return nil
}

// repoPath turns a compiler path into a repo-relative path.
func (t *Translator) repoPath(p Pkg, f string) string {
	if strings.HasPrefix(f, "…/") || strings.HasPrefix(f, "/") {
		return f
	}
	for _, repo := range t.ctx.Sources {
		if strings.HasPrefix(f, repo+"/") {
			return f
		}
	}
	if p.SrcDir != "" {
		return path.Join(p.SrcDir, f)
	}
	return f
}

func describeExe(p string) string {
	if strings.HasPrefix(p, "/nix/store/") {
		return "bob's builder (" + p[strings.LastIndex(p, "/")+1:] + ")"
	}
	return p
}

func (t *Translator) fetchFailed(drv string, p Pkg, lines []string) *diag.Error {
	attempts := 1
	for _, w := range t.warns {
		if p.URL != "" && strings.Contains(w, "'"+p.URL+"'") {
			attempts++
		}
	}
	for _, l := range lines {
		m := reDLFailed().FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if h := reHTTP().FindStringSubmatch(m[2]); h != nil {
			if code, _ := strconv.Atoi(h[1]); code == 404 || code == 403 || code == 410 {
				e := t.pkgErr(cDownloadNotFound, drv, map[string]string{"status": h[1]})
				e.Excerpt = []string{"url: " + p.URL}
				if code == 403 {
					e.Cause = "many file hosts (S3, crates.io) answer 403 instead of 404 for files that do not exist"
				}
				return e
			}
		}
		e := t.pkgErr(cDownloadFailed, drv, map[string]string{"reason": netReason(m[2]), "attempts": strconv.Itoa(attempts)})
		e.Excerpt = []string{"url: " + p.URL}
		return e
	}
	e := t.pkgErr(cDownloadFailed, drv, map[string]string{"reason": "unknown", "attempts": strconv.Itoa(attempts)})
	e.Excerpt = lastN(lines, 6)
	return e
}

func shortTool(l string) string {
	if i := strings.Index(l, ": cannot find"); i > 0 {
		return strings.TrimSpace(l[i+2:])
	}
	return l
}

func lastN(xs []string, n int) []string {
	for len(xs) > 0 && strings.TrimSpace(xs[len(xs)-1]) == "" {
		xs = xs[:len(xs)-1]
	}
	return append([]string{}, xs[max(0, len(xs)-n):]...)
}

func grep(xs []string, f func(string) bool, n int) []string {
	var out []string
	for _, x := range xs {
		if f(x) {
			out = append(out, x)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// block returns lines[i] plus up to n continuation lines (indented or gutter lines).
func block(lines []string, i, n int) []string {
	out := []string{lines[i]}
	for _, l := range lines[i+1:] {
		if len(out) > n || !isContinuation(l) {
			break
		}
		out = append(out, l)
	}
	for len(out) > 1 && (strings.TrimSpace(out[len(out)-1]) == "" || strings.TrimSpace(out[len(out)-1]) == "|") {
		out = out[:len(out)-1]
	}
	return out
}

func isContinuation(l string) bool {
	if l == "" {
		return true
	}
	return l[0] == ' ' || l[0] == '\t' || l[0] == '|' || (l[0] >= '0' && l[0] <= '9' && strings.Contains(l, " |"))
}

// diagnostics returns up to three error diagnostics with their context, starting at i.
func diagnostics(lines []string, i int) []string {
	var out []string
	n := 0
	for j := i; j < len(lines) && n < 3 && len(out) < 12; j++ {
		m := reCDiag().FindStringSubmatch(lines[j])
		if m == nil || strings.HasPrefix(m[5], "note:") || strings.HasPrefix(m[5], "warning:") {
			continue
		}
		out = append(out, block(lines, j, 4)...)
		n++
	}
	return out
}

// builderDiag explains a failure bob-builder diagnosed itself: the last error diagnostic it
// wrote (`bob:: {"type":"diag","severity":"error",…}`, R-NIX-017), e.g. a bob.Run output that
// was not created, a step timeout or a test service that did not become ready. The message is
// bob's own text; its first line is the cause, further lines (or the log before it) the excerpt.
func (t *Translator) builderDiag(drv string, p Pkg, lines []string) *diag.Error {
	msg, at := "", -1
	var code int
	var args map[string]string
	for i, l := range lines {
		m := reBobDiag().FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var d struct {
			Type, Severity, Msg string
			Code                int
			Args                map[string]string
		}
		if json.Unmarshal([]byte(m[1]), &d) == nil && d.Type == "diag" && d.Severity == "error" && d.Msg != "" {
			msg, at, code, args = d.Msg, i, d.Code, d.Args
		}
	}
	if at < 0 {
		return nil
	}
	if code == int(cPruned) { // a bundle's prune check (R-OUT-007): the code's own message
		e := t.pkgErr(cPruned, drv, map[string]string{"pattern": capArg(args["pattern"]), "file": capArg(args["file"]), "user": capArg(args["user"])})
		e.Excerpt = lastN(plainLog(lines[:at]), 10)
		return e
	}
	first, rest, _ := strings.Cut(msg, "\n")
	if m := reDiagTimout().FindStringSubmatch(first); m != nil && m[1] == p.Label {
		e := t.pkgErr(cTimeout, drv, map[string]string{"seconds": m[2]})
		e.Excerpt = lastN(plainLog(lines[:at]), 5)
		return e
	}
	e := t.pkgErr(cBuildFailed, drv, nil)
	e.Cause = first
	if rest != "" {
		// bob-builder's own detail lines: a failed service's last 20 log lines (R-FW-025)
		e.Excerpt = lastN(strings.Split(strings.TrimRight(rest, "\n"), "\n"), maxServiceTail)
	} else {
		e.Excerpt = lastN(plainLog(lines[:at]), 10)
	}
	return e
}

// capArg bounds a message argument a builder reported (its log is untrusted text).
func capArg(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// builderWarning returns the reason of a bob-builder warning line (BOB-E-2016,
// `bob:: {"type":"diag","severity":"warning","code":2016,…}`), or "".
func builderWarning(line string) string {
	m := reBobDiag().FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	var d struct {
		Type, Severity string
		Code           int
		Args           map[string]string
	}
	if json.Unmarshal([]byte(m[1]), &d) != nil || d.Type != "diag" || d.Severity != "warning" || d.Code != int(cBuilderWarning) {
		return ""
	}
	return capArg(d.Args["reason"])
}

// maxServiceTail bounds the excerpt of a bob-builder diagnostic: R-FW-025 shows the last 20
// log lines of a service that failed, the one exception to the 12-line excerpt of §02.3.
const maxServiceTail = 20

// plainLog drops bob-builder's own lines (diagnostics, the failure line) from a log.
func plainLog(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !reBobBuilder().MatchString(l) && !strings.HasPrefix(l, "bob:: ") {
			out = append(out, l)
		}
	}
	return out
}
