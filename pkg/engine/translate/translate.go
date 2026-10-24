package translate

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Bounds on what the translator keeps (AGENTS.md §5.4: the stream carries builder output).
const (
	logHeadLines = 2000     // first lines kept per build log (compiler diagnostics come early)
	logTailLines = 2000     // last lines kept per build log (excerpts are the tail)
	maxLogLine   = 4096     // bytes kept of one build log line
	maxHeadBytes = 1 << 20  // bytes kept of the head of one build log
	maxTailBytes = 1 << 20  // bytes kept of the tail of one build log
	maxRunLog    = 64 << 20 // bytes of build logs kept for the whole run (F6)
	keptOnEvict  = 16       // lines a log keeps when the run budget evicts it (excerpts use ≤ 8)
	maxWarnings  = 10000
	// maxBuilderWarnings bounds the bob-builder warnings kept per run.
	maxBuilderWarnings = 100
	maxUnknown         = 64 // unknown level-0 messages kept as BOB-E-9999 errors
	maxRawBytes        = 64 << 10
	maxDriftItems      = 64
)

type activity struct {
	id      uint64
	typ     activityType
	text    string
	drv     string // build, post-build-hook
	path    string // substitute, copy-path
	from    string // substitute, copy-path: the substituter URI
	stopped bool
	done    int64 // file-transfer progress (bytes)
}

// buildLog keeps the first and the last lines of one build log, bounded in lines and bytes.
type buildLog struct {
	head      []string
	headBytes int
	tail      []string // queue: tail[start:] are the kept lines
	start     int
	tailBytes int
	dropped   int
}

// add appends a line and returns the change of the bytes kept.
func (l *buildLog) add(s string) int {
	if len(s) > maxLogLine {
		s = s[:maxLogLine]
	}
	before := l.size()
	if len(l.tail) == 0 && len(l.head) < logHeadLines && l.headBytes+len(s) <= maxHeadBytes {
		l.head = append(l.head, s)
		l.headBytes += len(s)
		return len(s)
	}
	l.tail = append(l.tail, s)
	l.tailBytes += len(s)
	for len(l.tail)-l.start > logTailLines || l.tailBytes > maxTailBytes {
		l.tailBytes -= len(l.tail[l.start])
		l.tail[l.start] = ""
		l.start++
		l.dropped++
	}
	if l.start > logTailLines { // compact the queue
		l.tail = append([]string(nil), l.tail[l.start:]...)
		l.start = 0
	}
	return l.size() - before
}

func (l *buildLog) size() int { return l.headBytes + l.tailBytes }

func (l *buildLog) lineCount() int { return len(l.head) + len(l.tail) - l.start }

// shrink keeps only the last n lines and returns the change of the bytes kept.
func (l *buildLog) shrink(n int) int {
	if l.lineCount() <= n {
		return 0
	}
	all := l.lines()
	before := l.size()
	keep := append([]string(nil), all[max(0, len(all)-n):]...)
	*l = buildLog{tail: keep, dropped: l.dropped + len(all) - len(keep)}
	for _, s := range keep {
		l.tailBytes += len(s)
	}
	return l.size() - before
}

func (l *buildLog) lines() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.head)+len(l.tail)-l.start)
	out = append(out, l.head...)
	return append(out, l.tail[l.start:]...)
}

// Translator turns one Nix run into bob errors and events. Feed it every stderr line of
// `nix build --log-format internal-json` (live or replayed), then call Finish once. It is not
// safe for concurrent use.
type Translator struct {
	ctx       *Context
	acts      map[uint64]*activity
	builds    map[string]*activity // drv → last build activity
	logs      map[string]*buildLog // drv → build log (ANSI stripped)
	logBytes  int                  // bytes kept in logs (≤ maxRunLog after each line)
	stops     []string             // drvs whose build stopped, in order (eviction candidates)
	shrunk    int                  // stops[:shrunk] were considered for cutting
	dropped   int                  // stops[:dropped] were considered for dropping
	hookLogs  map[string][]string  // drv → post-build-hook lines
	hookDrv   string               // drv of the last post-build-hook activity
	from      map[string]string    // store path → substituter it was copied from
	pkgs      map[string]*PkgState // label → state
	order     []string             // labels, sorted
	failed    map[string]bool      // drvs that failed with their own error
	skipped   map[string]bool      // drvs that failed because a dependency failed
	explained map[string]bool      // bob drvs whose builder failure has its error already
	upstream  map[string]bool      // non-bob drvs built from source
	hints     map[*diag.Error]string
	deferred  map[*diag.Error]bool // errors of untrusted messages: emitted at Finish only
	claim     *claim               // the untrusted message being translated (plainError)
	claims    []*claim             // untrusted messages, settled at Finish
	warns     []string
	// builderWarns are bob-builder's warnings of successful builds (BOB-E-2016).
	builderWarns []builderWarn
	errs         []*diag.Error
	fatal        []string // drv-less fatal messages (timeouts, interrupts, plain lines)
	drift        []string
	unknown      int

	// Sink receives live events as they happen (optional). Log lines are sent too; the CLI
	// forwards them only with --json-log.
	Sink func(Event)
}

// New returns a translator for one Nix run with bob's facts ctx.
func New(ctx *Context) *Translator {
	if ctx == nil {
		ctx = &Context{}
	}
	t := &Translator{ctx: ctx, acts: map[uint64]*activity{}, builds: map[string]*activity{},
		logs: map[string]*buildLog{}, hookLogs: map[string][]string{}, from: map[string]string{},
		pkgs: map[string]*PkgState{}, failed: map[string]bool{}, skipped: map[string]bool{}, explained: map[string]bool{},
		upstream: map[string]bool{}, hints: map[*diag.Error]string{}, deferred: map[*diag.Error]bool{}}
	for _, d := range slices.Sorted(maps.Keys(ctx.Packages)) {
		p := ctx.Packages[d]
		if p.Role != "" {
			continue
		}
		if _, ok := t.pkgs[p.Label]; !ok {
			t.pkgs[p.Label] = &PkgState{Label: p.Label, DefinedAt: p.DefinedAt, Status: StPending, drv: d}
			t.order = append(t.order, p.Label)
		}
	}
	sort.Strings(t.order)
	return t
}

var ansiRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`) })

func (t *Translator) emit(e Event) {
	if t.Sink != nil {
		t.Sink(e)
	}
}

// Feed processes one stderr line of Nix (without the newline).
func (t *Translator) Feed(line []byte) { t.feed(parseLine(line)) }

func (t *Translator) feed(ev event) {
	switch {
	case ev.plain:
		// Nix prints some fatal errors before its JSON logger exists (bad flags, lock files).
		if s := strings.TrimSpace(stripANSI(ev.raw)); s != "" && len(t.fatal) < maxUnknown {
			t.fatal = append(t.fatal, capRaw(s))
		}
		return
	case ev.bad != "":
		t.driftOnce("unparsable @nix line: " + ev.bad)
		return
	}
	if len(ev.unknown) > 0 {
		t.driftOnce("unknown JSON keys " + strings.Join(ev.unknown, ","))
	}
	switch ev.Action {
	case actStart:
		t.start(ev)
	case actStop:
		if a := t.acts[ev.ID]; a != nil {
			a.stopped = true
			if a.typ == atBuild && a.drv != "" {
				t.stops = append(t.stops, a.drv)
			}
		}
	case actResult:
		t.result(ev)
	case actMsg:
		t.msg(ev)
	default:
		t.driftOnce(fmt.Sprintf("unknown action %q", ev.Action))
	}
}

func (t *Translator) driftOnce(s string) {
	if len(s) > 200 {
		s = s[:200]
	}
	if slices.Contains(t.drift, s) {
		return
	}
	if len(t.drift) < maxDriftItems {
		t.drift = append(t.drift, s)
	}
}

func (t *Translator) start(ev event) {
	at := activityType(ev.Type)
	if !at.known() {
		t.driftOnce(fmt.Sprintf("unknown activity type %d", ev.Type))
	}
	a := &activity{id: ev.ID, typ: at, text: stripANSI(ev.Text)}
	t.acts[ev.ID] = a
	switch at {
	case atBuild:
		a.drv = ev.str(0)
		t.builds[a.drv] = a
		if p, ok := t.ctx.Packages[a.drv]; ok {
			if st := t.pkgs[p.Label]; st != nil {
				st.Status = StBuilding
			}
			t.emit(Event{Event: "package.start", Label: p.Label, Action: "build"})
		} else {
			t.upstream[a.drv] = true
			t.emit(Event{Event: "upstream.start", Name: drvName(a.drv)})
		}
	case atSubstitute:
		a.path, a.from = ev.str(0), ev.str(1)
		t.from[a.path] = a.from
		if d, ok := t.ctx.Outputs[a.path]; ok {
			p := t.ctx.Packages[d]
			if st := t.pkgs[p.Label]; st != nil {
				st.Status = StCached
			}
			t.emit(Event{Event: "package.start", Label: p.Label, Action: "download", Cache: t.cacheName(a.from)})
		}
	case atCopyPath:
		a.path, a.from = ev.str(0), ev.str(1)
		if _, ok := t.from[a.path]; !ok {
			t.from[a.path] = a.from
		}
	case atPostBuildHook:
		a.drv = ev.str(0)
		t.hookDrv = a.drv
		// A failing hook is attributed to the last hook activity: older hook logs are not
		// needed any more (F6).
		for d := range t.hookLogs {
			if d != a.drv {
				delete(t.hookLogs, d)
			}
		}
	case atBuildWaiting:
		// "waiting for lock on '<out>'": another process builds the same path.
		if m := quotedPath().FindStringSubmatch(a.text); m != nil {
			if d, ok := t.ctx.Outputs[m[1]]; ok {
				t.emit(Event{Event: "package.wait", Label: t.ctx.Packages[d].Label, Reason: "another bob process is building it"})
			}
		}
	}
}

var quotedPath = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`'(/nix/store/[^']+)'`) })

// drvName returns the name part of a store path ("/nix/store/<hash>-name.drv" → "name").
func drvName(d string) string {
	b := strings.TrimSuffix(d[strings.LastIndex(d, "/")+1:], ".drv")
	if len(b) > 33 && b[32] == '-' {
		return b[33:]
	}
	return b
}

func (t *Translator) result(ev event) {
	rt := resultType(ev.Type)
	if !rt.known() {
		t.driftOnce(fmt.Sprintf("unknown result type %d", ev.Type))
		return
	}
	a := t.acts[ev.ID]
	if a == nil {
		return
	}
	switch rt {
	case rtBuildLogLine:
		if a.drv == "" {
			return
		}
		line := stripANSI(ev.str(0))
		l := t.logs[a.drv]
		if l == nil {
			l = &buildLog{}
			t.logs[a.drv] = l
		}
		t.logBytes += l.add(line)
		t.boundLogs()
		if r := builderWarning(line); r != "" && len(t.builderWarns) < maxBuilderWarnings {
			t.builderWarns = append(t.builderWarns, builderWarn{drv: a.drv, reason: r})
		}
		if p, ok := t.ctx.Packages[a.drv]; ok && t.Sink != nil {
			t.emit(Event{Event: "log", Label: p.Label, Line: t.mapPaths(line)})
		}
	case rtPostBuildLogLine:
		if hl := t.hookLogs[a.drv]; len(hl) < logTailLines {
			line := stripANSI(ev.str(0))
			if len(line) > maxLogLine {
				line = line[:maxLogLine]
			}
			t.hookLogs[a.drv] = append(hl, line)
		}
	case rtSetPhase:
		if p, ok := t.ctx.Packages[a.drv]; ok && a.drv != "" {
			if st := t.pkgs[p.Label]; st != nil {
				st.Phase = ev.str(0)
			}
		}
	case rtProgress:
		if a.typ == atFileTransfer {
			a.done = ev.num(0)
		}
	}
}

// boundLogs keeps the logs of the whole run within maxRunLog (F6). Over the budget, the logs of
// builds that stopped longest ago without an error are first cut to their last keptOnEvict
// lines (enough for excerpts), then dropped; then the largest remaining logs are cut.
func (t *Translator) boundLogs() {
	for t.logBytes > maxRunLog && t.shrunk < len(t.stops) {
		d := t.stops[t.shrunk]
		t.shrunk++
		if l := t.logs[d]; l != nil && !t.failed[d] {
			t.logBytes += l.shrink(keptOnEvict)
		}
	}
	for t.logBytes > maxRunLog && t.dropped < len(t.stops) {
		d := t.stops[t.dropped]
		t.dropped++
		if l := t.logs[d]; l != nil && !t.failed[d] {
			t.logBytes -= l.size()
			delete(t.logs, d)
		}
	}
	for t.logBytes > maxRunLog {
		var big *buildLog
		for _, d := range slices.Sorted(maps.Keys(t.logs)) {
			if l := t.logs[d]; big == nil || l.size() > big.size() {
				big = l
			}
		}
		if big == nil || big.lineCount() <= keptOnEvict {
			return
		}
		t.logBytes += big.shrink(keptOnEvict)
	}
}

// msg handles log messages: errors (level 0) and warnings (level 1) matter, the rest is noise.
func (t *Translator) msg(ev event) {
	text := ev.RawMsg
	if text == "" {
		text = ev.Msg
	}
	text = capRaw(strings.TrimPrefix(stripANSI(text), "error: "))
	switch ev.Level {
	case lvlWarn:
		if len(t.warns) < maxWarnings {
			t.warns = append(t.warns, strings.TrimPrefix(text, "warning: "))
		}
	case lvlError:
		if ev.hasRaw {
			t.nixError(text)
		} else {
			t.plainError(text)
		}
	}
}

// plainError handles a level-0 message without raw_msg. Nix forwards a builder's
// `@nix {"action":"msg",…}` lines in exactly this form (S15 §3.1, measured), so such a message
// is untrusted (F7): it is translated like any other, but its error events are not emitted live,
// and the packages it names fail only if Finish confirms it structurally: the output is not
// valid and, for a bob package, Nix started building it. Otherwise its errors become warnings.
func (t *Translator) plainError(text string) {
	t.claim = &claim{}
	defer func() {
		t.claims = append(t.claims, t.claim)
		t.claim = nil
	}()
	t.nixError(text)
}

// claim is what one untrusted message asserts: the drvs it fails or skips and its errors.
type claim struct {
	failed, skipped []string
	errs            []*diag.Error
}

// fail marks drv failed (provisionally for an untrusted message).
func (t *Translator) fail(drv string) {
	if t.claim != nil {
		t.claim.failed = append(t.claim.failed, drv)
		return
	}
	t.failed[drv] = true
}

// skip marks drv skipped because a dependency failed (provisionally for an untrusted message).
func (t *Translator) skip(drv string) {
	if t.claim != nil {
		t.claim.skipped = append(t.claim.skipped, drv)
		return
	}
	t.skipped[drv] = true
}

// settleClaims decides the untrusted messages with the outcome: a claim about a drv whose output
// is valid, or that a bob package Nix never started building failed, is false; its errors
// become warnings.
func (t *Translator) settleClaims(valid map[string]bool) (rejected []*diag.Error) {
	for _, c := range t.claims {
		ok := true
		for _, d := range c.failed {
			if _, mine := t.ctx.Packages[d]; valid[d] || mine && t.builds[d] == nil {
				ok = false
			}
		}
		for _, d := range c.skipped {
			ok = ok && !valid[d]
		}
		if ok {
			for _, d := range c.failed {
				t.failed[d] = true
			}
			for _, d := range c.skipped {
				t.skipped[d] = true
			}
			continue
		}
		for _, e := range c.errs {
			if i := slices.Index(t.errs, e); i >= 0 {
				t.errs = slices.Delete(t.errs, i, i+1)
				e.Severity = diag.SeverityWarning
				rejected = append(rejected, e)
			}
		}
	}
	t.claims = nil
	return rejected
}

func capRaw(s string) string {
	if len(s) > maxRawBytes {
		return s[:maxRawBytes]
	}
	return s
}

// Level-0 message patterns of Nix 2.34 (identical on 2.34.8 and 2.34.9, S15 §3.3). Each is
// anchored; a message that matches none is BOB-E-9999.
var (
	reCannotBuild = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^Cannot build '(/nix/store/[^']+\.drv)'\.\s*\n\s*Reason: (.*?)(\n\s*Output paths:.*)?$`)
	})
	reExit   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^builder failed with exit code (\d+)\.?$`) })
	reSignal = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^builder failed due to signal (\d+) \(([^)]*)\)\.?$`)
	})
	reDepsFailed = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(\d+) dependenc(y|ies) failed\.?$`) })
	rePlatform   = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^platform mismatch\s*\n\s*Required system: '([^']*)'.*Current system: '([^']*)'`)
	})
	reNoLocal = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^local builds are disabled`) })
	reHashFOD = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^hash mismatch in fixed-output derivation '(/nix/store/[^']+\.drv)':\s*specified:\s*(\S+)\s*got:\s*(\S+)`)
	})
	reTimedOut    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^timed out after (\d+) seconds`) })
	reInterrupted = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^interrupted by the user`) })
	reNoChroot    = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^derivation '(/nix/store/[^']+\.drv)' has '__noChroot' set`)
	})
	reNoOutput = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^builder for '(/nix/store/[^']+\.drv)' failed to produce output path for output '([^']+)'`)
	})
	reDisallowed = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^output '(/nix/store/[^']+)' is not allowed to refer to the following paths:\s*(.*)$`)
	})
	reLogLimit = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^building derivation '(/nix/store/[^']+\.drv)' killed after writing more than (\d+) bytes of log output`)
	})
	reHookFailed = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^program "([^"]+)" failed with exit code (\d+)`) })
	reLockPerm   = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^opening lock file "([^"]+)": (Permission denied|Read-only file system)`)
	})
	reMissingPath = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^getting (attributes|status) of (path )?["']?(/[^"']+)["']?: No such file or directory`)
	})
	reDiskFull   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(No space left on device|database or disk is full)`) })
	reHashImport = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^hash mismatch importing path '(/nix/store/[^']+)';\s*specified:\s*(\S+)\s*got:\s*(\S+)`)
	})
	reNoSubstituter = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^path '(/nix/store/[^']+)' is required, but there is no substituter that can build it`)
	})
	reSomeSubst = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^some substitutes for the outputs of derivation '(/nix/store/[^']+\.drv)' failed`)
	})
	reBuildOf    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^build of '.*' failed$`) })
	reIgnored    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^error \(ignored\): `) })
	reSQLiteBusy = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`SQLite database '([^']+)' is busy`) })
)

func (t *Translator) nixError(text string) {
	if m := reCannotBuild().FindStringSubmatch(text); m != nil {
		t.cannotBuild(m[1], strings.TrimSpace(m[2]), text)
		return
	}
	switch {
	case reHashFOD().MatchString(text):
		m := reHashFOD().FindStringSubmatch(text)
		t.fail(m[1])
		p := t.ctx.Packages[m[1]]
		e := t.pkgErr(cHashMismatch, m[1], nil)
		e.Excerpt = []string{"url:        " + p.URL, "bob.lock:   " + m[2], "downloaded: " + m[3]}
		e.Cause = "the downloaded file has a different sha256 than bob.lock pins"
		t.add(e, text)
	case reTimedOut().MatchString(text), reInterrupted().MatchString(text):
		t.fatal = append(t.fatal, text)
	case reNoChroot().MatchString(text):
		m := reNoChroot().FindStringSubmatch(text)
		t.fail(m[1])
		t.add(t.pkgErr(cBadStep, m[1], map[string]string{"reason": "it asks to run outside the sandbox"}), text)
	case reNoOutput().MatchString(text):
		m := reNoOutput().FindStringSubmatch(text)
		t.fail(m[1])
		e := t.pkgErr(cNoOutput, m[1], nil)
		e.Cause = "the build step finished successfully but did not create its output"
		e.Excerpt = t.tail(m[1], 5)
		t.add(e, text)
	case reDisallowed().MatchString(text):
		m := reDisallowed().FindStringSubmatch(text)
		d := t.ctx.Outputs[m[1]]
		t.fail(d)
		var refs []string
		for r := range strings.FieldsSeq(m[2]) {
			refs = append(refs, t.describePath(r))
		}
		e := t.pkgErr(cForbiddenRef, d, map[string]string{"ref": strings.Join(refs, ", ")})
		t.hints[e] = "this is a bob bug (missing fixup); please report it"
		t.add(e, text)
	case reLogLimit().MatchString(text):
		m := reLogLimit().FindStringSubmatch(text)
		t.fail(m[1])
		e := t.pkgErr(cLogLimit, m[1], map[string]string{"bytes": m[2]})
		e.Excerpt = t.tail(m[1], 5)
		t.add(e, text)
	case reHookFailed().MatchString(text):
		t.hookFailed(text)
	case reLockPerm().MatchString(text):
		m := reLockPerm().FindStringSubmatch(text)
		t.add(newErr(cStoreAccess, map[string]string{"reason": strings.ToLower(m[2]) + " (" + m[1] + ")"}), text)
	case reMissingPath().MatchString(text):
		m := reMissingPath().FindStringSubmatch(text)
		what := t.describePath(storeSuffix(m[3])) + " is registered but missing on disk"
		t.add(newErr(cStoreDamaged, map[string]string{"what": what}), text)
	case reHashImport().MatchString(text):
		m := reHashImport().FindStringSubmatch(text)
		e := newErr(cCacheCorrupt, map[string]string{"cache": t.cacheName(t.from[m[1]]), "what": t.describePath(m[1])})
		e.Excerpt = []string{"expected: " + m[2], "received: " + m[3]}
		t.add(e, text)
	case reNoSubstituter().MatchString(text), reBuildOf().MatchString(text):
		// follow-up messages of an error that was already reported
	case reSomeSubst().MatchString(text):
		t.fail(reSomeSubst().FindStringSubmatch(text)[1])
	case reIgnored().MatchString(text):
		if reDiskFull().MatchString(text) {
			t.diskFull(text)
		}
	case reDiskFull().MatchString(text):
		t.diskFull(text)
	case reSQLiteBusy().MatchString(text):
		t.add(newErr(cStoreBusy, nil), text)
	default:
		t.unknownError(text)
	}
}

// unknownError is BOB-E-9999 for a level-0 message the pattern table does not know.
func (t *Translator) unknownError(text string) {
	if t.unknown >= maxUnknown {
		return
	}
	t.unknown++
	e := newErr(cUnknown, nil)
	e.Cause = t.safeCause(firstLine(text))
	t.add(e, text)
}

// leakRe matches Nix vocabulary that must never reach default output (R-NIX-010). A cause
// line taken from an unrecognised message is shown only when it is free of it; the raw text
// is always available with -v.
var leakRe = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)/nix/store/|\.drv\b|derivation|Cannot build|Reason:|builder for|substituter|@nix |while evaluating|\.nix:|^\s*error:|max-jobs|sandbox|__`)
})

func (t *Translator) safeCause(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 || leakRe().MatchString(s) {
		return ""
	}
	return s
}

// storeSuffix maps a physical path inside a chroot store back to its /nix/store path.
func storeSuffix(p string) string {
	if i := strings.Index(p, "/nix/store/"); i > 0 {
		return p[i:]
	}
	return p
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func (t *Translator) diskFull(text string) {
	for _, e := range t.errs {
		if e.Code == cDiskFull {
			if len(e.Raw) < maxUnknown {
				e.Raw = append(e.Raw, text)
			}
			return
		}
	}
	what := "a package"
	for _, l := range t.order { // the build that was running
		if t.pkgs[l].Status == StBuilding {
			what = l
		}
	}
	e := newErr(cDiskFull, map[string]string{"what": what})
	if what != "a package" {
		e.Label, e.DefinedAt = what, ParsePos(t.pkgs[what].DefinedAt)
	}
	e.Cause = "the disk holding bob's store is full"
	t.add(e, text)
}

func (t *Translator) add(e *diag.Error, raw string) {
	if raw != "" {
		e.Raw = append(e.Raw, raw)
	}
	t.errs = append(t.errs, e)
	// Unknown and untrusted messages are decided at Finish (a successful run demotes them).
	if t.claim != nil {
		t.claim.errs = append(t.claim.errs, e)
		t.deferred[e] = true
	} else if e.Code != cUnknown {
		t.emit(Event{Event: "error", Error: e, Hint: t.hint(e)})
	}
}

func (t *Translator) hint(e *diag.Error) string {
	if h, ok := t.hints[e]; ok {
		return h
	}
	return e.Hint()
}

// pkgErr returns an error about the package that drv belongs to.
func (t *Translator) pkgErr(c diag.Code, drv string, args map[string]string) *diag.Error {
	if args == nil {
		args = map[string]string{}
	}
	p, ok := t.ctx.Packages[drv]
	if ok {
		args["label"] = p.Label
	} else {
		args["label"] = "nixpkgs " + drvName(drv)
	}
	e := newErr(c, args)
	if ok {
		e.DefinedAt = ParsePos(p.DefinedAt)
		e.Log = "bob log " + p.Label
	}
	return e
}

// describePath names a store path in bob terms.
func (t *Translator) describePath(p string) string {
	if d, ok := t.ctx.Outputs[p]; ok {
		return t.ctx.Packages[d].Label
	}
	if pk, ok := t.ctx.Packages[p]; ok {
		return pk.Label
	}
	return "nixpkgs " + drvName(p)
}

func (t *Translator) cacheName(url string) string {
	url = strings.TrimSuffix(url, "/")
	for _, s := range t.ctx.Substituters {
		if strings.TrimSuffix(s.URL, "/") == url {
			return s.Name
		}
	}
	if url == "" {
		return "(unknown)"
	}
	return url
}

func (t *Translator) cannotBuild(drv, reason, raw string) {
	p, mine := t.ctx.Packages[drv]
	switch {
	case reDepsFailed().MatchString(reason):
		t.skip(drv)
		return
	case rePlatform().MatchString(reason):
		m := rePlatform().FindStringSubmatch(reason)
		t.fail(drv)
		e := t.pkgErr(cNixpkgsUnbuildable, drv, map[string]string{"system": m[1]})
		e.Log = ""
		e.Cause = "this machine is " + m[2]
		t.add(e, raw)
		return
	case reNoLocal().MatchString(reason):
		t.fail(drv)
		t.add(t.cacheOnlyMiss(drv), raw)
		return
	}
	t.fail(drv)
	if !mine {
		t.upstreamFailed(drv, reason, raw)
		return
	}
	if t.explained[drv] {
		// Nix repeats a failed build's message when a requested package depends on it (Nix
		// 2.34 --keep-going): the failure is reported once.
		return
	}
	t.explained[drv] = true
	var e *diag.Error
	if m := reSignal().FindStringSubmatch(reason); m != nil {
		if m[1] == "9" {
			e = t.pkgErr(cOOM, drv, map[string]string{"what": p.Label})
			e.Cause = "the build step was killed with SIGKILL; on Linux this is almost always the out-of-memory killer"
		} else {
			e = t.pkgErr(cKilled, drv, map[string]string{"what": p.Label, "signal": m[2]})
		}
		e.Excerpt = t.tail(drv, 5)
	} else if m := reExit().FindStringSubmatch(reason); m != nil {
		e = t.classifyLog(drv, p, m[1])
	} else {
		e = t.pkgErr(cUnknown, drv, nil)
		e.Cause = t.safeCause(reason)
	}
	t.add(e, raw)
}

func (t *Translator) upstreamFailed(drv, reason, raw string) {
	chain := t.ctx.Chains[drv]
	if chain == nil && t.ctx.ChainOf != nil {
		if chain = t.ctx.ChainOf(drv); len(chain) > 0 {
			if t.ctx.Chains == nil {
				t.ctx.Chains = map[string][]string{}
			}
			t.ctx.Chains[drv] = chain
		}
	}
	attr, users := drvName(drv), []string(nil)
	if len(chain) > 0 {
		if a, ok := t.ctx.Nixpkgs[chain[0]]; ok {
			attr = a
		}
		users = t.ctx.Users[chain[0]]
	}
	shown := "(unknown)"
	if len(users) > 0 {
		shown = strings.Join(users, ", ")
	}
	e := newErr(cUpstream, map[string]string{"attr": attr, "users": shown})
	e.Cause = "nixpkgs " + drvName(drv) + ": " + strings.TrimSuffix(reason, ".")
	if len(chain) > 1 {
		names := make([]string, 0, len(chain))
		for _, c := range chain {
			names = append(names, drvName(c))
		}
		e.Excerpt = append(e.Excerpt, "via "+strings.Join(names, " → "))
	}
	e.Excerpt = append(e.Excerpt, t.tail(drv, 8)...)
	if len(users) > 0 {
		e.Label = users[0]
		if st := t.pkgs[users[0]]; st != nil {
			e.DefinedAt = ParsePos(st.DefinedAt)
		}
	}
	e.Log = "bob log --nixpkgs " + attr
	t.hints[e] = "this is not your code: the package is not in any cache for this nixpkgs revision; pin another revision or ask your bob server admin"
	t.add(e, raw)
}

// cacheOnlyMiss explains why a package could not be downloaded when local builds are off.
func (t *Translator) cacheOnlyMiss(drv string) *diag.Error {
	for _, w := range t.warns {
		if e := t.cacheWarning(w); e != nil {
			if p, ok := t.ctx.Packages[drv]; ok {
				e.Label, e.DefinedAt = p.Label, ParsePos(p.DefinedAt)
			}
			return e
		}
	}
	return t.pkgErr(cCacheOnlyMiss, drv, nil)
}

var (
	reUnableDL = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?s)^unable to download '([^']+)': (.*?)(; retrying in \d+ ms \(attempt (\d+)/(\d+)\))?$`)
	})
	reNotInCache = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^file '([^']+)' does not exist in binary cache '([^']+)'`)
	})
	reUntrusted = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^ignoring substitute for '([^']+)' from '([^']+)', as it's not signed`)
	})
	reHTTP = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`HTTP error (\d+)`) })
)

// cacheHint returns the hint of a cache error, more specific than the table's default hint
// ("" = the default). When Nix worked around the problem (the run succeeded), the hint says so.
func cacheHint(c diag.Code, workedAround bool) string {
	switch {
	case workedAround && c == cCacheAuth:
		return "bob built locally instead, which is slower; run bob login"
	case workedAround && c == cCacheMissing:
		return "bob built locally instead"
	case workedAround && c == cCacheUnreachable:
		return "bob built locally instead; check the network or the cache URL"
	case workedAround && c == cCacheUntrusted:
		return "bob built locally instead; the cache's signing key is not trusted"
	case c == cCacheMissing:
		return "the cache is inconsistent; bob built locally instead or report it to the cache admin"
	case c == cCacheUnreachable:
		return "bob builds locally when a cache is down; check the network or the cache URL"
	case c == cCacheUntrusted:
		return "the cache's signing key is not trusted; run bob login again or check the server key"
	}
	return ""
}

// cacheWarning turns a substituter warning into an error (nil if it is not about a cache bob
// configured).
func (t *Translator) cacheWarning(w string) *diag.Error {
	var e *diag.Error
	switch {
	case reUnableDL().MatchString(w):
		m := reUnableDL().FindStringSubmatch(w)
		base := cacheBase(m[1])
		if !t.isSubstituter(base) {
			return nil
		}
		cache := t.cacheName(base)
		if h := reHTTP().FindStringSubmatch(m[2]); h != nil && (h[1] == "401" || h[1] == "403") {
			e = newErr(cCacheAuth, map[string]string{"cache": cache})
			e.Cause = "the cache answered HTTP " + h[1] + " to " + m[1]
		} else {
			e = newErr(cCacheUnreachable, map[string]string{"cache": cache, "reason": netReason(m[2])})
		}
	case reNotInCache().MatchString(w):
		m := reNotInCache().FindStringSubmatch(w)
		e = newErr(cCacheMissing, map[string]string{"cache": t.cacheName(m[2]), "what": "a package", "status": "404"})
		e.Cause = m[1] + " is missing"
	case reUntrusted().MatchString(w):
		m := reUntrusted().FindStringSubmatch(w)
		e = newErr(cCacheUntrusted, map[string]string{"cache": t.cacheName(m[2]), "what": t.describePath(m[1])})
	default:
		return nil
	}
	e.Raw = []string{w}
	if h := cacheHint(e.Code, false); h != "" {
		t.hints[e] = h
	}
	return e
}

func cacheBase(url string) string {
	switch {
	case strings.HasSuffix(url, "/nix-cache-info"):
		return strings.TrimSuffix(url, "/nix-cache-info")
	case strings.HasSuffix(url, ".narinfo"):
		return url[:strings.LastIndex(url, "/")]
	}
	if i := strings.Index(url, "/nar/"); i > 0 {
		return url[:i]
	}
	return url
}

func (t *Translator) isSubstituter(base string) bool {
	for _, s := range t.ctx.Substituters {
		if strings.TrimSuffix(s.URL, "/") == strings.TrimSuffix(base, "/") {
			return true
		}
	}
	return false
}

// netReason maps curl's wording to plain words. It keys on curl's stable part: the wording
// around it differs between curl builds (S15 §3.3).
func netReason(s string) string {
	switch {
	case strings.Contains(s, "Could not resolve host"):
		return "host not found"
	case strings.Contains(s, "Could not connect to server"), strings.Contains(s, "Connection refused"):
		return "connection refused"
	case strings.Contains(s, "Timeout was reached"):
		return "timed out"
	case strings.Contains(s, "SSL"), strings.Contains(s, "certificate"):
		return "TLS error"
	}
	if h := reHTTP().FindStringSubmatch(s); h != nil {
		return "server error (HTTP " + h[1] + ")"
	}
	return firstLine(s)
}

func (t *Translator) hookFailed(text string) {
	drv := t.hookDrv
	e := t.pkgErr(cUploadFailed, drv, nil)
	for _, l := range t.hookLogs[drv] {
		e.Excerpt = append(e.Excerpt, t.mapPaths(l))
	}
	e.Log = ""
	t.add(e, text)
}

// tail returns the last n log lines of drv with paths mapped.
func (t *Translator) tail(drv string, n int) []string {
	l := t.logs[drv].lines()
	var out []string
	for _, s := range l[max(0, len(l)-n):] {
		out = append(out, t.mapPaths(s))
	}
	return out
}

var storePathRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^/\s'"]+`) })

// mapPaths rewrites store paths (SPEC §02.5 rule 3): a path inside an imported source becomes
// its repo path; any other store path becomes "…/<name>".
func (t *Translator) mapPaths(s string) string {
	if !strings.Contains(s, "/nix/store/") {
		return s
	}
	srcs := slices.Sorted(maps.Keys(t.ctx.Sources))
	sort.SliceStable(srcs, func(i, j int) bool { return len(srcs[i]) > len(srcs[j]) })
	for _, sp := range srcs {
		s = strings.ReplaceAll(s, sp, t.ctx.Sources[sp])
	}
	return storePathRe().ReplaceAllStringFunc(s, func(m string) string {
		return "…/" + m[len("/nix/store/")+33:]
	})
}

// builderWarn is one bob-builder warning of a build.
type builderWarn struct{ drv, reason string }

// Finish ends the run with the process outcome and returns the report.
func (t *Translator) Finish(o Outcome) *Report {
	valid := map[string]bool{}
	for _, d := range o.Valid {
		valid[d] = true
	}
	notes := t.settleClaims(valid)
	// drv-less fatal messages: attribute them to builds that stopped without output or error.
	for _, f := range t.fatal {
		t.fatalErr(f, o, valid)
	}
	if o.Signal != "" && !hasClass(t.errs, diag.ClassInterrupt) {
		e := newErr(cKilled, map[string]string{"what": "bob's build engine", "signal": sigName(o.Signal)})
		e.Cause = "the Nix process stopped before it could report; on Linux SIGKILL usually means the out-of-memory killer or a service manager"
		var running []string
		for _, l := range t.order {
			if t.pkgs[l].Status == StBuilding {
				running = append(running, l)
			}
		}
		if len(running) > 0 {
			e.Excerpt = []string{"was building: " + strings.Join(running, ", ")}
		}
		t.hints[e] = "run the build again; if it repeats, reduce parallel jobs (bob build -j)"
		t.add(e, "")
	}
	// R-NIX-018, ADR 0017 Decision 3: success is structural. Builders can print "@nix" lines
	// that Nix forwards as level-0 messages (measured), so when Nix exits 0 and every bob
	// output it touched is valid, no message fails the run: errors become warnings.
	if t.structuralSuccess(o, valid) {
		for _, e := range t.errs {
			e.Severity = diag.SeverityWarning
			notes = append(notes, e)
		}
		t.errs = nil
		for d := range valid {
			delete(t.failed, d)
			delete(t.skipped, d)
		}
	}
	for _, e := range t.errs {
		if e.Code == cUnknown || t.deferred[e] {
			t.emit(Event{Event: "error", Error: e, Hint: t.hint(e)})
		}
	}
	if o.ExitCode == 0 && o.Signal == "" && !o.Interrupt && len(t.errs) == 0 {
		t.invalidOutputs(valid)
	}
	if o.ExitCode != 0 && len(t.errs) == 0 && o.Signal == "" {
		e := newErr(cUnknown, nil)
		e.Cause = fmt.Sprintf("Nix exited with status %d without an error bob understands", o.ExitCode)
		t.add(e, "")
	}
	// Cache problems Nix worked around (it built locally instead) become warnings: the run
	// succeeds, but the user learns that the cache is not working.
	used := map[string]bool{}
	for _, e := range t.errs {
		for _, r := range e.Raw {
			used[r] = true
		}
	}
	seen := map[diag.Code]bool{}
	for _, w := range t.warns {
		if used[w] {
			continue
		}
		if e := t.cacheWarning(w); e != nil && !seen[e.Code] && !hasCode(t.errs, e.Code) {
			seen[e.Code] = true
			e.Severity = diag.SeverityWarning
			t.hints[e] = cacheHint(e.Code, true)
			notes = append(notes, e)
		}
	}
	// Warnings bob-builder wrote into the logs of bob packages (R-OUT-019), once each.
	seenWarn := map[builderWarn]bool{}
	for _, w := range t.builderWarns {
		if _, ok := t.ctx.Packages[w.drv]; !ok || seenWarn[w] {
			continue
		}
		seenWarn[w] = true
		e := t.pkgErr(cBuilderWarning, w.drv, map[string]string{"reason": w.reason})
		e.Severity = diag.SeverityWarning
		notes = append(notes, e)
	}
	for _, n := range notes {
		t.emit(Event{Event: "warning", Error: n, Hint: t.hint(n)})
	}
	sort.SliceStable(t.errs, func(i, j int) bool {
		a, b := t.errs[i].Label, t.errs[j].Label
		if (a == "") != (b == "") {
			return a != ""
		}
		return a < b
	})
	r := &Report{Drift: t.drift, Warnings: notes, hints: t.hints}
	t.packageResults(r, valid, len(o.Valid) > 0)
	r.Counts.Upstream = len(t.upstream)
	for _, a := range t.acts {
		if a.typ == atFileTransfer {
			r.Counts.Downloaded += a.done
		}
	}
	r.Errors = t.errs
	r.ExitCode = diag.ExitCode(t.errs)
	// ADR 0017 Decision 6: a failure that bob explains only with unknown messages while the
	// log format drifted is reported as a log-format problem.
	if len(t.drift) > 0 && r.ExitCode != 0 && allUnknown(t.errs) {
		e := newErr(cLogFormat, map[string]string{"nixVersion": o.NixVersion})
		e.Excerpt = append([]string(nil), t.drift...)
		r.Errors = append(r.Errors, e)
		r.ExitCode = diag.ExitCode(r.Errors)
	}
	return r
}

// structuralSuccess reports whether Nix exited 0 and every bob drv it built or downloaded,
// and every requested package, has a valid output.
func (t *Translator) structuralSuccess(o Outcome, valid map[string]bool) bool {
	if o.ExitCode != 0 || o.Signal != "" || o.Interrupt {
		return false
	}
	for _, l := range t.order {
		if st := t.pkgs[l]; t.touched(l, st) && !valid[st.drv] {
			return false
		}
	}
	return true
}

// touched reports whether Nix built or downloaded package l in this run, or the user requested
// it: such a package must end with a valid output.
func (t *Translator) touched(l string, st *PkgState) bool {
	return st.Status == StBuilding || st.Status == StCached || slices.Contains(t.ctx.Requested, l)
}

// invalidOutputs reports packages whose output is not valid although Nix exited 0 and said
// nothing (R-NIX-018: bob never trusts Nix's success alone).
func (t *Translator) invalidOutputs(valid map[string]bool) {
	for _, l := range t.order {
		st := t.pkgs[l]
		if !t.touched(l, st) || valid[st.drv] {
			continue
		}
		t.failed[st.drv] = true
		e := t.pkgErr(cNoOutput, st.drv, nil)
		e.Cause = "the build engine reported success, but the output is not in bob's store"
		t.add(e, "")
	}
}

func (t *Translator) packageResults(r *Report, valid map[string]bool, anyValid bool) {
	cancelled := hasClass(t.errs, diag.ClassInterrupt)
	for _, l := range t.order {
		st := t.pkgs[l]
		d := st.drv
		switch {
		case t.failed[d] || t.failedHelper(l):
			st.Status = StFailed
		case t.skipped[d]:
			st.Status = StSkipped
		case valid[d] && st.Status == StBuilding:
			st.Status = StBuilt
		case valid[d] && st.Status == StPending && slices.Contains(t.ctx.PreBuilt, d):
			st.Status = StBuilt
		case valid[d] && st.Status == StCached:
		case st.Status == StBuilding && cancelled:
			st.Status = StCancelled
		case st.Status == StBuilding:
			// Started but its output is not valid (R-NIX-018): never a success.
			st.Status = StFailed
		case !valid[d] && anyValid && slices.Contains(t.ctx.Requested, l):
			st.Status = StSkipped
		}
		if st.Status == StPending {
			continue
		}
		r.Packages = append(r.Packages, st)
		switch st.Status {
		case StBuilt:
			r.BuiltPackages++
		case StCached:
			r.Counts.Cached++
		case StFailed:
			r.Counts.Failed++
		case StSkipped:
			r.Counts.Skipped++
		case StCancelled:
			r.Counts.Cancelled++
		}
	}
	// counts.built is the number of bob derivations Nix built (a package's helper steps
	// included, R-GO-002a "exactly 2 Go derivations rebuild"), not of packages.
	for d := range t.builds {
		if _, bob := t.ctx.Packages[d]; bob && valid[d] && !t.failed[d] {
			r.Counts.Built++
		}
	}
	rank := map[Status]int{StFailed: 0, StCancelled: 1, StSkipped: 2, StBuilt: 3, StCached: 4}
	sort.SliceStable(r.Packages, func(i, j int) bool {
		a, b := r.Packages[i], r.Packages[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.Label < b.Label
	})
}

func allUnknown(errs []*diag.Error) bool {
	for _, e := range errs {
		if e.Code != cUnknown {
			return false
		}
	}
	return true
}

func (t *Translator) failedHelper(label string) bool {
	for d, p := range t.ctx.Packages {
		if p.Label == label && p.Role != "" && t.failed[d] {
			return true
		}
	}
	return false
}

func (t *Translator) fatalErr(f string, o Outcome, valid map[string]bool) {
	// candidates: bob builds that stopped, have no output and no error of their own
	var cands []string
	for _, d := range slices.Sorted(maps.Keys(t.builds)) {
		a := t.builds[d]
		if a.stopped && !valid[d] && !t.failed[d] && !t.skipped[d] {
			cands = append(cands, d)
		}
	}
	switch {
	case reTimedOut().MatchString(f):
		secs, _ := strconv.Atoi(reTimedOut().FindStringSubmatch(f)[1])
		code := cTimeout
		if secs == t.ctx.Limits.MaxSilentS && secs != t.ctx.Limits.TimeoutS {
			code = cSilent
		}
		if len(cands) == 0 {
			if hasCode(t.errs, code) {
				return // Nix reports the same timeout twice
			}
			t.add(newErr(code, map[string]string{"label": "a package", "seconds": strconv.Itoa(secs)}), f)
			return
		}
		for _, d := range cands {
			t.failed[d] = true
			e := t.pkgErr(code, d, map[string]string{"seconds": strconv.Itoa(secs)})
			e.Excerpt = t.tail(d, 5)
			t.add(e, f)
		}
	case reInterrupted().MatchString(f):
		e := newErr(cCancelled, nil)
		if !o.Interrupt {
			e.Cause = "Nix received a termination signal from outside bob (for example a service manager, a container runtime or an OOM policy)"
		}
		var labels []string
		for _, d := range cands {
			if p, ok := t.ctx.Packages[d]; ok {
				labels = append(labels, p.Label)
			}
		}
		if len(labels) > 0 {
			e.Excerpt = []string{"stopped while building: " + strings.Join(labels, ", ")}
		}
		t.add(e, f)
	default:
		if strings.HasPrefix(f, "warning:") {
			return
		}
		t.add(newErr(cNixStopped, map[string]string{"reason": "Nix could not start"}), f)
	}
}

func sigName(s string) string {
	switch s {
	case "killed":
		return "SIGKILL"
	case "terminated":
		return "SIGTERM"
	case "interrupt":
		return "SIGINT"
	}
	return s
}
