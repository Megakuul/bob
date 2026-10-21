package translate

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Build-log patterns. These come from compilers and tools, not from Nix, so they do not drift
// with the Nix version; they drift with toolchain versions (covered by golden tests per toolchain).
var (
	reCDiag      = regexp.MustCompile(`^(\S+?\.(?:c|cc|cpp|cxx|h|hpp|hh|go|m|S)):(\d+):(\d+): (?:(fatal error|error): )?(.*)$`)
	reRustDiag   = regexp.MustCompile(`^error(\[E\d+\])?: (.*)$`)
	reRustLoc    = regexp.MustCompile(`^\s*--> (\S+):(\d+):(\d+)`)
	reUndefRef   = regexp.MustCompile("undefined reference to `([^']+)'")
	reNoLib      = regexp.MustCompile(`cannot find -l(\S+?)(:|$)`)
	reNoHeader   = regexp.MustCompile(`fatal error: ([^:]+): No such file or directory`)
	reGoTooOld   = regexp.MustCompile(`go: (\S+) requires go >= (\S+) \(running go (\S+);`)
	reGoTestFail = regexp.MustCompile(`^--- FAIL: (\S+)`)
	reRustCrate  = regexp.MustCompile("(?:unresolved module or unlinked crate|can't find crate for) `([^`]+)`")
	reExecFail   = regexp.MustCompile(`^error: executing '([^']+)': (.*)$`)
	reShSyntax   = regexp.MustCompile(`^(?:bash|sh): (?:-c: )?line \d+: syntax error`)
	reNetwork    = regexp.MustCompile(`Network is unreachable|Could not resolve host|Temporary failure in name resolution|Name or service not known`)
	reDLFailed   = regexp.MustCompile(`error: unable to download '([^']+)': (.*)$`)
	reBobBuilder = regexp.MustCompile(`^bob-builder: `)
)

// classifyLog explains a builder that exited non-zero, from its log.
func (t *Translator) classifyLog(drv string, p Pkg, exit string) *Error {
	raw := t.logs[drv]
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = t.mapPaths(l, p)
	}
	args := map[string]string{"exit": exit}
	if p.Kind == "fetch" && p.Role == "" {
		return t.fetchFailed(drv, p, lines)
	}
	if p.Role == "unpack" {
		e := t.pkgErr("BOB-E-2000", drv, args)
		e.Cause = "unpacking the download failed"
		e.Excerpt = lastN(lines, 8)
		return e
	}
	// bob's own builder is broken (bob bug)
	for _, l := range lines {
		if m := reExecFail.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-9001", drv, map[string]string{"reason": "cannot execute " + t.describeExe(m[1]) + ": " + m[2]})
			return e
		}
		if reShSyntax.MatchString(l) {
			e := t.pkgErr("BOB-E-9001", drv, map[string]string{"reason": "its build script has a syntax error"})
			e.Excerpt = []string{l}
			return e
		}
	}
	for _, l := range lines {
		if reNetwork.MatchString(l) {
			e := t.pkgErr("BOB-E-2009", drv, nil)
			e.Excerpt = []string{l}
			return e
		}
	}
	// tests
	for i, l := range lines {
		if m := reGoTestFail.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-2005", drv, nil)
			e.Cause = "test " + m[1] + " failed"
			for _, x := range lines[i:] {
				if strings.HasPrefix(x, "ok ") || strings.HasPrefix(x, "?") || reBobBuilder.MatchString(x) {
					continue
				}
				e.Excerpt = append(e.Excerpt, x)
				if len(e.Excerpt) == 10 {
					break
				}
			}
			return e
		}
	}
	for _, l := range lines {
		if m := reGoTooOld.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-2006", drv, map[string]string{"module": m[1], "want": m[2], "have": m[3]})
			e.Excerpt = []string{l}
			return e
		}
	}
	// link errors (before compile diagnostics: "collect2: error" is a link error)
	for _, l := range lines {
		if m := reUndefRef.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-2002", drv, map[string]string{"symbol": m[1]})
			e.Excerpt = grep(lines, func(s string) bool { return reUndefRef.MatchString(s) || strings.Contains(s, ": in function") }, 6)
			return e
		}
		if m := reNoLib.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-2004", drv, map[string]string{"lib": m[1]})
			e.Excerpt = []string{shortTool(l)}
			return e
		}
	}
	for i, l := range lines {
		if m := reNoHeader.FindStringSubmatch(l); m != nil {
			e := t.pkgErr("BOB-E-2003", drv, map[string]string{"header": m[1]})
			if d := reCDiag.FindStringSubmatch(l); d != nil {
				e.Location = t.repoPath(p, d[1]) + ":" + d[2] + ":" + d[3]
			}
			e.Excerpt = block(lines, i, 4)
			return e
		}
	}
	// compile diagnostics
	for i, l := range lines {
		if m := reCDiag.FindStringSubmatch(l); m != nil && (m[4] != "" || strings.HasSuffix(m[1], ".go")) {
			if strings.HasPrefix(m[5], "note:") || strings.HasPrefix(m[5], "warning:") {
				continue
			}
			file := t.repoPath(p, m[1])
			e := t.pkgErr("BOB-E-2001", drv, map[string]string{"file": file})
			e.Location = file + ":" + m[2] + ":" + m[3]
			e.Excerpt = diagnostics(lines, i, reCDiag)
			return e
		}
		if m := reRustDiag.FindStringSubmatch(l); m != nil && !strings.HasPrefix(m[2], "aborting due to") {
			if c := reRustCrate.FindStringSubmatch(strings.Join(lines[i:min(len(lines), i+6)], "\n")); c != nil {
				e := t.pkgErr("BOB-E-2010", drv, map[string]string{"crate": c[1]})
				e.Excerpt = block(lines, i, 6)
				t.rustLoc(e, p, lines, i)
				return e
			}
			e := t.pkgErr("BOB-E-2001", drv, map[string]string{"file": "?"})
			t.rustLoc(e, p, lines, i)
			e.Message = fill(SpecOf("BOB-E-2001").Title, map[string]string{"label": p.Label, "file": strings.SplitN(e.Location, ":", 2)[0]})
			e.Args["file"] = strings.SplitN(e.Location, ":", 2)[0]
			e.Excerpt = block(lines, i, 8)
			return e
		}
	}
	e := t.pkgErr("BOB-E-2000", drv, args)
	e.Cause = "the build step exited with status " + exit
	var tail []string
	for _, l := range lines {
		if !reBobBuilder.MatchString(l) {
			tail = append(tail, l)
		}
	}
	e.Excerpt = lastN(tail, 10)
	return e
}

func (t *Translator) rustLoc(e *Error, p Pkg, lines []string, i int) {
	for _, l := range lines[i:min(len(lines), i+3)] {
		if m := reRustLoc.FindStringSubmatch(l); m != nil {
			e.Location = t.repoPath(p, m[1]) + ":" + m[2] + ":" + m[3]
			return
		}
	}
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

func (t *Translator) describeExe(p string) string {
	if strings.HasPrefix(p, "/nix/store/") {
		name := p[strings.LastIndex(p, "/")+1:]
		return "bob's builder (" + name + ")"
	}
	return p
}

func (t *Translator) fetchFailed(drv string, p Pkg, lines []string) *Error {
	attempts := 1
	for _, w := range t.warns {
		if strings.Contains(w.text, "'"+p.URL+"'") {
			attempts++
		}
	}
	for i, l := range lines {
		if m := reDLFailed.FindStringSubmatch(l); m != nil {
			reason := m[2]
			if h := reHTTP.FindStringSubmatch(reason); h != nil {
				if code, _ := strconv.Atoi(h[1]); code == 404 || code == 403 || code == 410 {
					e := t.pkgErr("BOB-E-3003", drv, map[string]string{"status": h[1]})
					e.Excerpt = []string{"url: " + p.URL}
					if code == 403 {
						e.Cause = "many file hosts (S3, crates.io) answer 403 instead of 404 for files that do not exist"
					}
					return e
				}
			}
			e := t.pkgErr("BOB-E-3002", drv, map[string]string{"reason": netReason(reason), "attempts": strconv.Itoa(attempts)})
			e.Excerpt = []string{"url: " + p.URL}
			_ = i
			return e
		}
	}
	e := t.pkgErr("BOB-E-3002", drv, map[string]string{"reason": "unknown", "attempts": strconv.Itoa(attempts)})
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
	for len(out) > 1 && strings.TrimSpace(out[len(out)-1]) == "" || len(out) > 1 && strings.TrimSpace(out[len(out)-1]) == "|" {
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
func diagnostics(lines []string, i int, re *regexp.Regexp) []string {
	var out []string
	n := 0
	for j := i; j < len(lines) && n < 3 && len(out) < 12; j++ {
		m := re.FindStringSubmatch(lines[j])
		if m == nil || strings.HasPrefix(m[5], "note:") || strings.HasPrefix(m[5], "warning:") {
			continue
		}
		out = append(out, block(lines, j, 4)...)
		n++
	}
	return out
}
