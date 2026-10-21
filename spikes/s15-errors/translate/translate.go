// Package translate turns Nix's internal-json stream of a B-style build into bob errors and
// progress events. It is fed incrementally (live output) and finished with the process outcome.
package translate

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"bobspike/s15/nixlog"
)

// Status of a package in this run.
type Status string

const (
	StPending     Status = "pending"
	StBuilding    Status = "building"
	StBuilt       Status = "built"
	StSubstituted Status = "cached" // downloaded from a binary cache
	StFailed      Status = "failed"
	StSkipped     Status = "skipped" // a dependency failed
	StCancelled   Status = "cancelled"
)

// PkgState is the per-package view for output.
type PkgState struct {
	Label     string `json:"label"`
	DefinedAt string `json:"definedAt,omitempty"`
	Status    Status `json:"status"`
	Phase     string `json:"phase,omitempty"`
	StartSeq  int    `json:"-"`
	StopSeq   int    `json:"-"`
	drv       string
}

// Report is the result of a run.
type Report struct {
	Errors   []*Error    `json:"errors"`
	Packages []*PkgState `json:"packages"`
	Drift    []string    `json:"drift,omitempty"` // log-format surprises (unknown types, unparsable lines)
	Warnings []*Error    `json:"warnings,omitempty"`
	ExitCode int         `json:"exitCode"`
	Counts   Counts      `json:"counts"`
}

// Counts summarises the run.
type Counts struct {
	Built      int   `json:"built"`
	Cached     int   `json:"cached"`
	Failed     int   `json:"failed"`
	Skipped    int   `json:"skipped"`
	Cancelled  int   `json:"cancelled"`
	Upstream   int   `json:"upstream"`   // nixpkgs packages built from source
	Downloaded int64 `json:"downloaded"` // bytes
}

type activity struct {
	id, parent uint64
	typ        nixlog.ActivityType
	text       string
	drv        string // build / post-build-hook
	path, from string // substitute / copy-path
	url        string // file-transfer
	log        []string
	stopped    bool
	stopSeq    int
	done       int64 // progress
}

type warning struct {
	seq  int
	text string
}

// Translator holds the state of one run.
type Translator struct {
	ctx      *Context
	acts     map[uint64]*activity
	builds   map[string]*activity // drv -> last build activity
	logs     map[string][]string  // drv -> build log lines (ANSI stripped)
	hookLogs map[string][]string  // drv -> post-build-hook lines
	pkgs     map[string]*PkgState // label -> state
	order    []string
	failed   map[string]bool // drvs that failed with their own error
	skipped  map[string]bool // drvs that failed because a dependency failed
	warns    []warning
	errs     []*Error
	fatal    []string // drv-less fatal messages (timeouts, interrupts)
	drift    []string
	seq      int
	upstream map[string]bool
	Sink     func(Event) // live events (optional)
}

// New creates a translator for one Nix run.
func New(ctx *Context) *Translator {
	t := &Translator{ctx: ctx, acts: map[uint64]*activity{}, builds: map[string]*activity{}, logs: map[string][]string{},
		hookLogs: map[string][]string{}, pkgs: map[string]*PkgState{}, failed: map[string]bool{}, skipped: map[string]bool{},
		upstream: map[string]bool{}}
	for _, d := range sortedKeys(ctx.Packages) {
		p := ctx.Packages[d]
		if p.Role != "" {
			continue
		}
		if _, ok := t.pkgs[p.Label]; !ok {
			t.pkgs[p.Label] = &PkgState{Label: p.Label, DefinedAt: p.DefinedAt, Status: StPending, drv: d}
			t.order = append(t.order, p.Label)
		}
	}
	return t
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`)

// StripANSI removes terminal escapes.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func (t *Translator) emit(e Event) {
	if t.Sink != nil {
		t.Sink(e)
	}
}

// Feed processes one parsed event.
func (t *Translator) Feed(ev nixlog.Event) {
	t.seq++
	switch {
	case ev.Plain:
		if s := strings.TrimSpace(StripANSI(ev.Raw)); s != "" {
			// Nix prints some fatal errors before the JSON logger exists (e.g. bad flags).
			t.fatal = append(t.fatal, s)
		}
		return
	case ev.Bad != "":
		t.drift = append(t.drift, "unparsable @nix line: "+ev.Bad)
		return
	}
	if len(ev.Unknown) > 0 {
		t.driftOnce("unknown JSON keys " + strings.Join(ev.Unknown, ","))
	}
	switch ev.Action {
	case nixlog.ActStart:
		t.start(ev)
	case nixlog.ActStop:
		if a := t.acts[ev.ID]; a != nil {
			a.stopped, a.stopSeq = true, t.seq
			if a.typ == nixlog.ActBuild {
				t.buildStopped(a)
			}
		}
	case nixlog.ActResult:
		t.result(ev)
	case nixlog.ActMsg:
		t.msg(ev)
	default:
		t.driftOnce(fmt.Sprintf("unknown action %q", ev.Action))
	}
}

func (t *Translator) driftOnce(s string) {
	for _, d := range t.drift {
		if d == s {
			return
		}
	}
	t.drift = append(t.drift, s)
}

func (t *Translator) start(ev nixlog.Event) {
	at := ev.Activity()
	if !at.Known() {
		t.driftOnce(fmt.Sprintf("unknown activity type %d (%q)", ev.Type, ev.Text))
	}
	a := &activity{id: ev.ID, parent: ev.Parent, typ: at, text: StripANSI(ev.Text)}
	t.acts[ev.ID] = a
	switch at {
	case nixlog.ActBuild:
		a.drv = ev.Str(0)
		t.builds[a.drv] = a
		if p, ok := t.ctx.Packages[a.drv]; ok {
			if st := t.pkgs[p.Label]; st != nil {
				st.Status, st.StartSeq = StBuilding, t.seq
			}
			t.emit(Event{Event: "package.start", Label: p.Label, Action: "build"})
		} else {
			t.upstream[a.drv] = true
			t.emit(Event{Event: "upstream.start", Name: drvName(a.drv)})
		}
	case nixlog.ActSubstitute:
		a.path, a.from = ev.Str(0), ev.Str(1)
		if d, ok := t.ctx.Outputs[a.path]; ok {
			p := t.ctx.Packages[d]
			if st := t.pkgs[p.Label]; st != nil {
				st.Status = StSubstituted
			}
			t.emit(Event{Event: "package.start", Label: p.Label, Action: "download", Cache: t.cacheName(a.from)})
		}
	case nixlog.ActCopyPath:
		a.path, a.from = ev.Str(0), ev.Str(1)
	case nixlog.ActFileTransfer:
		a.url = ev.Str(0)
	case nixlog.ActPostBuildHook:
		a.drv = ev.Str(0)
	case nixlog.ActBuildWaiting:
		// "waiting for lock on '<out>'" (another process builds the same path)
		if m := quotedPath.FindStringSubmatch(a.text); m != nil {
			if d, ok := t.ctx.Outputs[m[1]]; ok {
				t.emit(Event{Event: "package.wait", Label: t.ctx.Packages[d].Label, Reason: "another bob process is building it"})
			}
		}
	}
}

var quotedPath = regexp.MustCompile(`'(/nix/store/[^']+)'`)

func drvName(d string) string {
	b := strings.TrimSuffix(d[strings.LastIndex(d, "/")+1:], ".drv")
	if len(b) > 33 {
		return b[33:]
	}
	return b
}

func (t *Translator) buildStopped(a *activity) {
	p, ok := t.ctx.Packages[a.drv]
	if !ok {
		return
	}
	if st := t.pkgs[p.Label]; st != nil {
		st.StopSeq = t.seq
	}
}

func (t *Translator) result(ev nixlog.Event) {
	rt := ev.Result()
	if !rt.Known() {
		t.driftOnce(fmt.Sprintf("unknown result type %d", ev.Type))
		return
	}
	a := t.acts[ev.ID]
	switch rt {
	case nixlog.ResBuildLogLine:
		line := StripANSI(ev.Str(0))
		if a != nil && a.drv != "" {
			t.logs[a.drv] = append(t.logs[a.drv], line)
			if p, ok := t.ctx.Packages[a.drv]; ok {
				t.emit(Event{Event: "log", Label: p.Label, Line: t.mapPaths(line, p)})
			}
		} else if a != nil {
			a.log = append(a.log, line)
		}
	case nixlog.ResPostBuildLogLine:
		if a != nil {
			t.hookLogs[a.drv] = append(t.hookLogs[a.drv], StripANSI(ev.Str(0)))
		}
	case nixlog.ResSetPhase:
		if a != nil && a.drv != "" {
			if p, ok := t.ctx.Packages[a.drv]; ok {
				if st := t.pkgs[p.Label]; st != nil {
					st.Phase = ev.Str(0)
				}
			}
		}
	case nixlog.ResProgress:
		if a != nil && a.typ == nixlog.ActFileTransfer {
			a.done = ev.Int(0)
		}
	}
}

// msg handles log messages. Errors (level 0) and warnings (level 1) matter; the rest is noise.
func (t *Translator) msg(ev nixlog.Event) {
	text := ev.RawMsg
	if text == "" {
		text = ev.Msg
	}
	text = strings.TrimPrefix(StripANSI(text), "error: ")
	switch {
	case ev.Level == nixlog.LvlWarn:
		t.warns = append(t.warns, warning{t.seq, strings.TrimPrefix(text, "warning: ")})
	case ev.Level == nixlog.LvlError:
		t.nixError(text)
	}
}

// Message patterns of Nix 2.34 (verified on 2.34.8 and 2.34.9, see fixtures/).
var (
	reCannotBuild   = regexp.MustCompile(`(?s)^Cannot build '(/nix/store/[^']+\.drv)'\.\s*\n\s*Reason: (.*?)(\n\s*Output paths:.*)?$`)
	reExit          = regexp.MustCompile(`^builder failed with exit code (\d+)\.?$`)
	reSignal        = regexp.MustCompile(`^builder failed due to signal (\d+) \(([^)]*)\)\.?$`)
	reDepsFailed    = regexp.MustCompile(`^(\d+) dependenc(y|ies) failed\.?$`)
	rePlatform      = regexp.MustCompile(`(?s)^platform mismatch\s*\n\s*Required system: '([^']*)'.*Current system: '([^']*)'`)
	reNoLocal       = regexp.MustCompile(`^local builds are disabled`)
	reHashFOD       = regexp.MustCompile(`(?s)^hash mismatch in fixed-output derivation '(/nix/store/[^']+\.drv)':\s*specified:\s*(\S+)\s*got:\s*(\S+)`)
	reTimedOut      = regexp.MustCompile(`^timed out after (\d+) seconds`)
	reInterrupted   = regexp.MustCompile(`^interrupted by the user`)
	reNoChroot      = regexp.MustCompile(`^derivation '(/nix/store/[^']+\.drv)' has '__noChroot' set`)
	reNoOutput      = regexp.MustCompile(`^builder for '(/nix/store/[^']+\.drv)' failed to produce output path for output '([^']+)'`)
	reDisallowed    = regexp.MustCompile(`(?s)^output '(/nix/store/[^']+)' is not allowed to refer to the following paths:\s*(.*)$`)
	reLogLimit      = regexp.MustCompile(`^building derivation '(/nix/store/[^']+\.drv)' killed after writing more than (\d+) bytes of log output`)
	reHookFailed    = regexp.MustCompile(`^program "([^"]+)" failed with exit code (\d+)`)
	reLockPerm      = regexp.MustCompile(`^opening lock file "([^"]+)": (Permission denied|Read-only file system)`)
	reMissingPath   = regexp.MustCompile(`^getting (attributes|status) of (path )?["']?(/[^"']+)["']?: No such file or directory`)
	reDiskFull      = regexp.MustCompile(`(No space left on device|database or disk is full)`)
	reHashImport    = regexp.MustCompile(`(?s)^hash mismatch importing path '(/nix/store/[^']+)';\s*specified:\s*(\S+)\s*got:\s*(\S+)`)
	reNoSubstituter = regexp.MustCompile(`^path '(/nix/store/[^']+)' is required, but there is no substituter that can build it`)
	reSomeSubst     = regexp.MustCompile(`^some substitutes for the outputs of derivation '(/nix/store/[^']+\.drv)' failed`)
	reBuildOf       = regexp.MustCompile(`^build of '.*' failed$`)
	reIgnored       = regexp.MustCompile(`^error \(ignored\): `)
	reSQLiteBusy    = regexp.MustCompile(`SQLite database '([^']+)' is busy`)
)

func (t *Translator) nixError(text string) {
	if m := reCannotBuild.FindStringSubmatch(text); m != nil {
		t.cannotBuild(m[1], strings.TrimSpace(m[2]), text)
		return
	}
	switch {
	case reHashFOD.MatchString(text):
		m := reHashFOD.FindStringSubmatch(text)
		t.failed[m[1]] = true
		p := t.ctx.Packages[m[1]]
		e := t.pkgErr("BOB-E-3001", m[1], map[string]string{})
		e.Excerpt = []string{"url:        " + p.URL, "bob.lock:   " + m[2], "downloaded: " + m[3]}
		e.Cause = "the downloaded file has a different sha256 than bob.lock pins"
		t.add(e, text)
	case reTimedOut.MatchString(text), reInterrupted.MatchString(text):
		t.fatal = append(t.fatal, text)
	case reNoChroot.MatchString(text):
		m := reNoChroot.FindStringSubmatch(text)
		t.failed[m[1]] = true
		t.add(t.pkgErr("BOB-E-9004", m[1], map[string]string{"reason": "it asks to run outside the sandbox"}), text)
	case reNoOutput.MatchString(text):
		m := reNoOutput.FindStringSubmatch(text)
		t.failed[m[1]] = true
		e := t.pkgErr("BOB-E-9002", m[1], nil)
		e.Cause = "the build step finished successfully but did not create its output"
		e.Excerpt = t.tail(m[1], 5)
		t.add(e, text)
	case reDisallowed.MatchString(text):
		m := reDisallowed.FindStringSubmatch(text)
		d := t.ctx.Outputs[m[1]]
		t.failed[d] = true
		var refs []string
		for _, r := range strings.Fields(m[2]) {
			refs = append(refs, t.describePath(r))
		}
		t.add(t.pkgErr("BOB-E-9003", d, map[string]string{"ref": strings.Join(refs, ", ")}), text)
	case reLogLimit.MatchString(text):
		m := reLogLimit.FindStringSubmatch(text)
		t.failed[m[1]] = true
		e := t.pkgErr("BOB-E-2011", m[1], map[string]string{"bytes": m[2]})
		e.Excerpt = t.tail(m[1], 5)
		t.add(e, text)
	case reHookFailed.MatchString(text):
		t.hookFailed(text)
	case reLockPerm.MatchString(text):
		m := reLockPerm.FindStringSubmatch(text)
		e := newErr("BOB-E-7002", map[string]string{"reason": strings.ToLower(m[2]) + " (" + m[1] + ")"})
		t.add(e, text)
	case reMissingPath.MatchString(text):
		m := reMissingPath.FindStringSubmatch(text)
		e := newErr("BOB-E-7003", map[string]string{"what": t.describePath(storeSuffix(m[3])) + " is registered but missing on disk"})
		t.add(e, text)
	case reHashImport.MatchString(text):
		m := reHashImport.FindStringSubmatch(text)
		from := t.copyFrom(m[1])
		e := newErr("BOB-E-4005", map[string]string{"cache": t.cacheName(from), "what": t.describePath(m[1])})
		e.Excerpt = []string{"expected: " + m[2], "received: " + m[3]}
		t.add(e, text)
	case reNoSubstituter.MatchString(text), reSomeSubst.MatchString(text), reBuildOf.MatchString(text):
		// follow-on messages of an error that was already reported
		if m := reSomeSubst.FindStringSubmatch(text); m != nil {
			t.failed[m[1]] = true
		}
	case reIgnored.MatchString(text):
		if reDiskFull.MatchString(text) {
			t.diskFull(text)
		}
	case reDiskFull.MatchString(text):
		t.diskFull(text)
	case reSQLiteBusy.MatchString(text):
		t.add(newErr("BOB-E-7004", nil), text)
	default:
		e := newErr("BOB-E-9999", nil)
		e.Cause = firstLine(text)
		t.add(e, text)
	}
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
		if e.Code == "BOB-E-6001" {
			e.Raw = append(e.Raw, text)
			return
		}
	}
	what := "a package"
	for _, l := range t.order { // the build that was running
		if st := t.pkgs[l]; st.Status == StBuilding {
			what = l
		}
	}
	e := newErr("BOB-E-6001", map[string]string{"what": what})
	if what != "a package" {
		e.Label, e.DefinedAt = what, t.pkgs[what].DefinedAt
	}
	e.Cause = "the disk holding bob's store is full"
	t.add(e, text)
}

func (t *Translator) add(e *Error, raw string) {
	if raw != "" {
		e.Raw = append(e.Raw, raw)
	}
	t.errs = append(t.errs, e)
	if e.Code != "BOB-E-9999" { // unknown messages are decided at Finish (a successful run demotes them)
		t.emit(Event{Event: "error", Error: e})
	}
}

func (t *Translator) pkgErr(c Code, drv string, args map[string]string) *Error {
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
		e.DefinedAt = p.DefinedAt
		e.LogRef = "bob log " + p.Label
	}
	return e
}

// describePath names a store path in bob terms.
func (t *Translator) describePath(p string) string {
	if d, ok := t.ctx.Outputs[p]; ok {
		return t.ctx.Packages[d].Label
	}
	if _, ok := t.ctx.Packages[p]; ok {
		return t.ctx.Packages[p].Label
	}
	name := p[strings.LastIndex(p, "/")+1:]
	if len(name) > 33 {
		name = name[33:]
	}
	return "nixpkgs " + name
}

func (t *Translator) copyFrom(path string) string {
	for _, a := range t.acts {
		if (a.typ == nixlog.ActCopyPath || a.typ == nixlog.ActSubstitute) && a.path == path {
			return a.from
		}
	}
	return ""
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
	case reDepsFailed.MatchString(reason):
		t.skipped[drv] = true
		return
	case rePlatform.MatchString(reason):
		m := rePlatform.FindStringSubmatch(reason)
		t.failed[drv] = true
		e := t.pkgErr("BOB-E-1010", drv, map[string]string{"system": m[1]})
		e.Class, e.LogRef = ClassDefinition, ""
		e.Cause = "this machine is " + m[2]
		t.add(e, raw)
		return
	case reNoLocal.MatchString(reason):
		t.failed[drv] = true
		t.add(t.cacheOnlyMiss(drv), raw)
		return
	}
	t.failed[drv] = true
	if !mine {
		t.upstreamFailed(drv, reason, raw)
		return
	}
	var e *Error
	if m := reSignal.FindStringSubmatch(reason); m != nil {
		if m[1] == "9" {
			e = t.pkgErr("BOB-E-6002", drv, map[string]string{"what": p.Label})
			e.Cause = "the build step was killed with SIGKILL; on Linux this is almost always the out-of-memory killer"
		} else {
			e = t.pkgErr("BOB-E-6003", drv, map[string]string{"what": p.Label, "signal": m[2]})
		}
		e.Excerpt = t.tail(drv, 5)
	} else if m := reExit.FindStringSubmatch(reason); m != nil {
		e = t.classifyLog(drv, p, m[1])
	} else {
		e = t.pkgErr("BOB-E-9999", drv, nil)
		e.Cause = reason
	}
	t.add(e, raw)
}

func (t *Translator) upstreamFailed(drv, reason, raw string) {
	chain := t.ctx.Chains[drv]
	attr, users := drvName(drv), []string{}
	if len(chain) > 0 {
		if a, ok := t.ctx.Nixpkgs[chain[0]]; ok {
			attr = a
		}
		users = t.ctx.Users[chain[0]]
	}
	if len(users) == 0 {
		users = []string{"(unknown)"}
	}
	e := newErr("BOB-E-5001", map[string]string{"attr": attr, "users": strings.Join(users, ", ")})
	e.Cause = "nixpkgs " + drvName(drv) + ": " + strings.TrimSuffix(reason, ".")
	if len(chain) > 1 {
		var names []string
		for _, c := range chain {
			names = append(names, drvName(c))
		}
		e.Excerpt = append(e.Excerpt, "via "+strings.Join(names, " → "))
	}
	e.Excerpt = append(e.Excerpt, t.tail(drv, 8)...)
	if len(users) > 0 && users[0] != "(unknown)" {
		e.Label = users[0]
		for d, p := range t.ctx.Packages {
			if p.Label == users[0] && p.Role == "" {
				e.DefinedAt = p.DefinedAt
				_ = d
			}
		}
	}
	e.LogRef = "bob log --nixpkgs " + attr
	t.add(e, raw)
}

// cacheOnlyMiss explains why a package could not be downloaded when local builds are off.
func (t *Translator) cacheOnlyMiss(drv string) *Error {
	for _, w := range t.warns {
		if e := t.cacheWarning(w.text); e != nil {
			if p, ok := t.ctx.Packages[drv]; ok {
				e.Label, e.DefinedAt = p.Label, p.DefinedAt
			}
			return e
		}
	}
	e := t.pkgErr("BOB-E-4006", drv, nil)
	return e
}

var (
	reUnableDL   = regexp.MustCompile(`(?s)^unable to download '([^']+)': (.*?)(; retrying in \d+ ms \(attempt (\d+)/(\d+)\))?$`)
	reNotInCache = regexp.MustCompile(`^file '([^']+)' does not exist in binary cache '([^']+)'`)
	reUntrusted  = regexp.MustCompile(`^ignoring substitute for '([^']+)' from '([^']+)', as it's not signed`)
	reHTTP       = regexp.MustCompile(`HTTP error (\d+)`)
)

// cacheWarning turns a substituter warning into an error (nil if it is not about a cache).
func (t *Translator) cacheWarning(w string) *Error {
	if m := reUnableDL.FindStringSubmatch(w); m != nil {
		base := cacheBase(m[1])
		if !t.isSubstituter(base) {
			return nil
		}
		cache := t.cacheName(base)
		if h := reHTTP.FindStringSubmatch(m[2]); h != nil && (h[1] == "401" || h[1] == "403") {
			e := newErr("BOB-E-4001", map[string]string{"cache": cache})
			e.Cause = "the cache answered HTTP " + h[1] + " to " + m[1]
			e.Raw = []string{w}
			return e
		}
		e := newErr("BOB-E-4003", map[string]string{"cache": cache, "reason": netReason(m[2])})
		e.Raw = []string{w}
		return e
	}
	if m := reNotInCache.FindStringSubmatch(w); m != nil {
		e := newErr("BOB-E-4002", map[string]string{"cache": t.cacheName(m[2]), "what": "a package", "status": "404"})
		e.Cause = m[1] + " is missing"
		e.Raw = []string{w}
		return e
	}
	if m := reUntrusted.FindStringSubmatch(w); m != nil {
		e := newErr("BOB-E-4004", map[string]string{"cache": t.cacheName(m[2]), "what": t.describePath(m[1])})
		e.Raw = []string{w}
		return e
	}
	return nil
}

func cacheBase(url string) string {
	for _, suf := range []string{"/nix-cache-info", ".narinfo"} {
		if strings.HasSuffix(url, suf) {
			if suf == ".narinfo" {
				return url[:strings.LastIndex(url, "/")]
			}
			return strings.TrimSuffix(url, suf)
		}
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

// netReason maps curl's wording to plain words.
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
	if h := reHTTP.FindStringSubmatch(s); h != nil {
		return "server error (HTTP " + h[1] + ")"
	}
	return firstLine(s)
}

func (t *Translator) hookFailed(text string) {
	var drv string
	for _, a := range t.acts {
		if a.typ == nixlog.ActPostBuildHook {
			drv = a.drv
		}
	}
	e := t.pkgErr("BOB-E-4007", drv, nil)
	for _, l := range t.hookLogs[drv] {
		e.Excerpt = append(e.Excerpt, t.mapPaths(l, t.ctx.Packages[drv]))
	}
	e.LogRef = ""
	t.add(e, text)
}

// tail returns the last n log lines of drv (paths mapped).
func (t *Translator) tail(drv string, n int) []string {
	l := t.logs[drv]
	p := t.ctx.Packages[drv]
	var out []string
	for _, s := range l[max(0, len(l)-n):] {
		out = append(out, t.mapPaths(s, p))
	}
	return out
}

// mapPaths replaces store paths of imported sources with repo paths and shortens other store paths.
func (t *Translator) mapPaths(s string, p Pkg) string {
	if !strings.Contains(s, "/nix/store/") {
		return s
	}
	for sp, repo := range t.ctx.Sources {
		s = strings.ReplaceAll(s, sp, repo)
	}
	return storePathRe.ReplaceAllStringFunc(s, func(m string) string {
		return "…/" + m[len("/nix/store/")+33:]
	})
}

var storePathRe = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^/\s'"]+`)

// Finish ends the run and returns the report.
func (t *Translator) Finish(o Outcome) *Report {
	valid := map[string]bool{}
	for _, d := range o.Valid {
		valid[d] = true
	}
	// drv-less fatal messages: attribute to builds that stopped without output and without an error
	for _, f := range t.fatal {
		t.fatalErr(f, o, valid)
	}
	if o.Signal != "" && !hasClass(t.errs, ClassInterrupt) {
		e := newErr("BOB-E-6003", map[string]string{"what": "bob's build engine", "signal": sigName(o.Signal)})
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
		e.Hint = "run the build again; if it repeats, reduce parallel jobs (bob build -j)"
		t.add(e, "")
	}
	// A run that Nix reports as successful (exit 0, all outputs valid) never fails because of a
	// message bob does not understand: builders can print "@nix" lines that Nix forwards as
	// level-0 messages (measured), so such messages are kept as notes, not errors.
	var notes []*Error
	if o.ExitCode == 0 && o.Signal == "" {
		var keep []*Error
		for _, e := range t.errs {
			if e.Code == "BOB-E-9999" {
				e.Severity = "warning"
				notes = append(notes, e)
			} else {
				keep = append(keep, e)
			}
		}
		t.errs = keep
	}
	for _, e := range t.errs {
		if e.Code == "BOB-E-9999" {
			t.emit(Event{Event: "error", Error: e})
		}
	}
	if o.ExitCode != 0 && len(t.errs) == 0 && o.Signal == "" {
		e := newErr("BOB-E-9999", nil)
		e.Cause = fmt.Sprintf("Nix exited with status %d without an error bob understands", o.ExitCode)
		t.add(e, "")
	}
	// substituter problems Nix worked around (it built locally instead) become warnings:
	// the run succeeds, but the user must learn that the cache is not working.
	used := map[string]bool{}
	for _, e := range t.errs {
		for _, r := range e.Raw {
			used[r] = true
		}
	}
	seenW := map[Code]bool{}
	for _, w := range t.warns {
		if used[w.text] {
			continue
		}
		if e := t.cacheWarning(w.text); e != nil && !seenW[e.Code] && !hasCode(t.errs, e.Code) {
			seenW[e.Code] = true
			e.Severity = "warning"
			e.Hint = warnHint[e.Code]
			notes = append(notes, e)
		}
	}
	for _, n := range notes {
		t.emit(Event{Event: "warning", Error: n})
	}
	sort.SliceStable(t.errs, func(i, j int) bool {
		a, b := t.errs[i].Label, t.errs[j].Label
		if (a == "") != (b == "") {
			return a != ""
		}
		return a < b
	})
	r := &Report{Drift: t.drift, Warnings: notes}
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
		case valid[d] && st.Status == StSubstituted:
		case st.Status == StBuilding:
			if hasClass(t.errs, ClassInterrupt) {
				st.Status = StCancelled
			} else {
				st.Status = StFailed
			}
		case !valid[d] && len(o.Valid) > 0 && t.requested(l):
			st.Status = StSkipped
		}
		if st.Status != StPending {
			r.Packages = append(r.Packages, st)
		}
		switch st.Status {
		case StBuilt:
			r.Counts.Built++
		case StSubstituted:
			r.Counts.Cached++
		case StFailed:
			r.Counts.Failed++
		case StSkipped:
			r.Counts.Skipped++
		case StCancelled:
			r.Counts.Cancelled++
		}
	}
	rank := map[Status]int{StFailed: 0, StCancelled: 1, StSkipped: 2, StBuilt: 3, StSubstituted: 4}
	sort.SliceStable(r.Packages, func(i, j int) bool {
		a, b := r.Packages[i], r.Packages[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.Label < b.Label
	})
	r.Counts.Upstream = len(t.upstream)
	for _, a := range t.acts {
		if a.typ == nixlog.ActFileTransfer {
			r.Counts.Downloaded += a.done
		}
	}
	r.Errors = t.errs
	r.ExitCode = ExitCode(t.errs)
	if len(t.drift) > 0 && r.ExitCode != 0 && allUnknown(t.errs) {
		e := newErr("BOB-E-9998", map[string]string{"nixVersion": o.NixVersion})
		e.Excerpt = t.drift
		r.Errors = append(r.Errors, e)
	}
	return r
}

func allUnknown(errs []*Error) bool {
	for _, e := range errs {
		if e.Code != "BOB-E-9999" {
			return false
		}
	}
	return true
}

func (t *Translator) requested(label string) bool {
	for _, r := range t.ctx.Requested {
		if r == label {
			return true
		}
	}
	return false
}

func (t *Translator) failedHelper(label string) bool {
	for d, p := range t.ctx.Packages {
		if p.Label == label && p.Role != "" && t.failed[d] {
			return true
		}
	}
	return false
}

func hasClass(errs []*Error, c Class) bool {
	for _, e := range errs {
		if e.Class == c {
			return true
		}
	}
	return false
}

func (t *Translator) fatalErr(f string, o Outcome, valid map[string]bool) {
	// candidates: bob builds that stopped, have no output and no error of their own
	var cands []string
	for _, d := range sortedKeys(t.builds) {
		a := t.builds[d]
		if a.stopped && !valid[d] && !t.failed[d] && !t.skipped[d] {
			cands = append(cands, d)
		}
	}
	switch {
	case reTimedOut.MatchString(f):
		secs, _ := strconv.Atoi(reTimedOut.FindStringSubmatch(f)[1])
		code := Code("BOB-E-2007")
		if secs == t.ctx.Limits.MaxSilentS && secs != t.ctx.Limits.TimeoutS {
			code = "BOB-E-2008"
		}
		if len(cands) == 0 {
			if hasCode(t.errs, code) {
				return // the same timeout is reported twice by Nix
			}
			t.add(newErr(code, map[string]string{"label": "a package", "seconds": strconv.Itoa(secs)}), f)
			return
		}
		for _, d := range cands {
			if t.failed[d] {
				continue // second copy of the message
			}
			t.failed[d] = true
			e := t.pkgErr(code, d, map[string]string{"seconds": strconv.Itoa(secs)})
			e.Excerpt = t.tail(d, 5)
			t.add(e, f)
		}
	case reInterrupted.MatchString(f):
		e := newErr("BOB-E-8001", nil)
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
		e := newErr("BOB-E-9005", map[string]string{"reason": firstLine(f)})
		t.add(e, f)
	}
}

func hasCode(errs []*Error, c Code) bool {
	for _, e := range errs {
		if e.Code == c {
			return true
		}
	}
	return false
}

// warnHint replaces the error hint when Nix worked around the problem.
var warnHint = map[Code]string{
	"BOB-E-4001": "bob built locally instead, which is slower; run bob login",
	"BOB-E-4002": "bob built locally instead",
	"BOB-E-4003": "bob built locally instead; check the network or the cache URL",
	"BOB-E-4004": "bob built locally instead; the cache's signing key is not trusted",
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
