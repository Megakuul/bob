package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/remote"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// projectSlug is the syntax of a server project id (decision D-IB12-1: 1-40 characters a-z,
// 0-9 and '-', a letter first, no '-' last).
var projectSlug = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`) })

// project is the server project of the repository at root: BOB_SERVER_PROJECT, else the slug
// of the root directory's name (decision D-IC1-5). A server creates the project on its first
// upload build when it does not exist yet (decision D-IB13-1).
func (e *env) project(root string) (string, error) {
	if p := e.getenv("BOB_SERVER_PROJECT"); p != "" {
		if !projectSlug().MatchString(p) {
			return "", diag.New(4, "flag", "BOB_SERVER_PROJECT", "what", "a project id (lower-case letters, digits and -, a letter first)")
		}
		return p, nil
	}
	p := slug(filepath.Base(root))
	if !projectSlug().MatchString(p) {
		return "", diag.New(4, "flag", "BOB_SERVER_PROJECT", "what", "a project id (the repository directory name is not one)")
	}
	return p, nil
}

// slug maps a directory name to a project id: lower case, runs of other characters become one
// '-', leading characters before the first letter dropped, at most 40 characters.
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', b.Len() > 0 && r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	p := b.String()
	if len(p) > 40 {
		p = p[:40]
	}
	return strings.TrimRight(p, "-")
}

// remoteRun is the state of one `bob build/test --remote`.
type remoteRun struct {
	e        *env
	st       *store.Store
	server   string
	test     bool
	counts   translate.Counts
	buildID  string
	finished map[string]bool // labels reported as finished
}

// runRemote is `bob build --remote` / `bob test --remote` (SPEC §02.6, R-DEV-005): snapshot,
// upload, CreateBuild, follow the build, substitute the results.
func runRemote(e *env, test bool) int {
	server, err := e.serverURL("")
	if err != nil {
		return e.out.fail(err)
	}
	if server == "" {
		return e.out.fail(noServer())
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	project, err := e.project(root)
	if err != nil {
		return e.out.fail(err)
	}
	patterns := e.patterns(root, e.a.pos)
	version := ""
	if e.a.has("version") { // release version (R-OUT-022): checked here, built with on the server
		version = e.a.str("version")
		if err := build.CheckVersion(version); err != nil {
			return e.out.fail(err)
		}
	}
	st, _, warns, exit, done := e.openStore(true)
	if done {
		return exit
	}
	e.out.begin(patterns)
	for _, w := range warns {
		if w.Args["server"] != remote.Host(server) { // this server's own failure follows as an error
			e.out.diagnose(w, "")
		}
	}
	api, err := e.session(e.ctx, server)
	if err != nil {
		return e.out.fail(err)
	}
	r := &remoteRun{e: e, st: st, server: server, test: test, finished: map[string]bool{}}
	x := &remote.Executor{Server: api, Project: project, Fetch: r.fetch}
	if e.a.bool("no-download") {
		x.Fetch = nil
	}
	req := engine.RemoteRequest{Root: root, Patterns: patterns, Variants: variants(e.a.str("variant")), Test: test, Version: version}
	req.Platforms = []bob.Platform{bob.Platform(e.a.str("platform"))}

	// Ctrl-C detaches (the build keeps running); a second one within 2 s cancels it.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT)
	defer signal.Stop(sigs)

	events, err := x.Build(e.ctx, req)
	if err != nil {
		if e.ctx.Err() != nil { // Ctrl-C before the build existed: nothing runs on the server
			return e.out.fail(diag.New(8001))
		}
		method := remote.CreateBuildMethod
		if ae, ok := remote.AsError(err); ok && ae.Method != remote.UploadMethod {
			method = ae.Method
		}
		d := apiDiag(server, method, project, err)
		if d.Code == 4012 { // no build exists, so its default hint (bob log --build) does not apply
			e.out.diagnose(d, "the server refused the request; its log has the details")
			return e.out.finish(-1, translate.Counts{}, nil)
		}
		return e.out.fail(d)
	}
	var fin remote.Done
	for ev := range events {
		if ev.Name == remote.EvDone {
			_ = json.Unmarshal(ev.Data, &fin)
			continue
		}
		r.event(ev)
	}
	if fin.Detached {
		return r.detach(api, sigs)
	}
	return r.finish(fin)
}

func variants(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, "+")
}

// event renders one executor event (--json lines or human progress).
func (r *remoteRun) event(ev engine.Event) {
	o := r.e.out
	switch ev.Name {
	case remote.EvSubmitted:
		var s remote.Submitted
		if json.Unmarshal(ev.Data, &s) != nil {
			return
		}
		r.buildID = s.BuildID
		o.event("remote.submitted", s)
		o.printf("remote build %s queued on %s\n  %s\n", s.BuildID, s.Server, s.URL)
	case remote.EvState:
		var s remote.State
		if json.Unmarshal(ev.Data, &s) == nil {
			o.event("remote.state", s)
		}
	case remote.EvPackage:
		var p remote.Package
		if json.Unmarshal(ev.Data, &p) != nil || p.Label == "" {
			return
		}
		r.pkg(p)
	}
}

// pkg reports a package state change: package.start when it starts building, package.finish
// (and a human line) when it is done.
func (r *remoteRun) pkg(p remote.Package) {
	o := r.e.out
	action := "build"
	if r.test {
		action = "test"
	}
	status, word := "", ""
	switch p.State {
	case "building":
		o.event("package.start", struct {
			Label  string `json:"label"`
			Action string `json:"action"`
			Cache  string `json:"cache"`
		}{p.Label, action, ""})
		return
	case "succeeded":
		status, word = "built", "built  "
		r.counts.Built++
	case "cached":
		status, word = "cached", "cached "
		r.counts.Cached++
	case "failed":
		status, word = "failed", "FAILED "
		r.counts.Failed++
	case "skipped":
		status, word = "skipped", "skipped"
		r.counts.Skipped++
	case "cancelled":
		status, word = "cancelled", "stopped"
		r.counts.Cancelled++
	default:
		return
	}
	key := p.Label + "@" + p.Platform
	if r.finished[key] {
		return
	}
	r.finished[key] = true
	var paths []string
	if r.e.a.bool("print-paths") && (status == "built" || status == "cached") {
		for _, sp := range p.Outputs {
			paths = append(paths, filepath.Join(r.st.Home(), sp))
		}
	}
	o.event("package.finish", struct {
		Label  string   `json:"label"`
		Status string   `json:"status"`
		Paths  []string `json:"paths,omitempty"`
	}{p.Label, status, paths})
	o.printf("%s %s\n", word, p.Label)
	for _, hp := range paths {
		o.printf("%s %s\n", p.Label, hp)
	}
}

// fetch substitutes the build's outputs from the server cache into the local store, with a
// negative narinfo TTL of 0: the paths were promoted moments ago (SPEC §6.6.5).
func (r *remoteRun) fetch(ctx context.Context, paths []string) error {
	r.e.out.printf("downloading %d output%s\n", len(paths), plural(len(paths)))
	var se bytes.Buffer
	args := append([]string{"--realise", "--option", "narinfo-cache-negative-ttl", "0"}, paths...)
	c := r.st.Command(ctx, "nix-store", args...)
	c.Stdout, c.Stderr = io.Discard, &se
	if err := c.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(se.String()))
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// finish reports the end of a followed build and returns the exit status.
func (r *remoteRun) finish(fin remote.Done) int {
	o := r.e.out
	id := fin.BuildID
	var errs []*diag.Error
	fail := func(reason string, raw ...string) *diag.Error {
		d := diag.New(4012, "id", id, "reason", reason)
		d.Raw = raw
		errs = append(errs, d)
		return d
	}
	switch {
	case fin.Failure != nil && fin.Failure.Step == "download":
		fail("its outputs could not be downloaded from the server's cache", fin.Failure.Message)
	case fin.Failure != nil && fin.Failure.Unreachable:
		d := diag.New(4009, "variant", "unreachable", "server", remote.Host(r.server), "reason", fin.Failure.Message)
		errs = append(errs, d)
	case fin.Failure != nil && fin.Failure.Code == "unauthenticated":
		errs = append(errs, diag.New(4008, "server", r.server))
	case fin.Failure != nil:
		fail("bob lost track of it ("+fin.Failure.Message+")", fin.Failure.Message)
	case fin.State == "succeeded":
	case fin.State == "failed":
		reason := "the build failed"
		if len(fin.Errors) > 0 {
			reason = strings.TrimSpace(fin.Errors[0].Code + " " + fin.Errors[0].Message)
		}
		d := fail(reason)
		for _, w := range fin.Errors {
			line := strings.TrimSpace(w.Code + ": " + w.Message)
			if w.Hint != "" {
				line += " (hint: " + w.Hint + ")"
			}
			d.Excerpt = append(d.Excerpt, line)
		}
	default:
		fail("it ended " + fin.State)
	}
	// The worker's own errors count for the exit status (SPEC §02.3: 4012 plus them).
	exitErrs := append([]*diag.Error{}, errs...)
	for _, w := range fin.Errors {
		if n, err := strconv.Atoi(strings.TrimPrefix(w.Code, "BOB-E-")); err == nil && strings.HasPrefix(w.Code, "BOB-E-") {
			if _, ok := diag.Lookup(diag.Code(n)); ok {
				exitErrs = append(exitErrs, &diag.Error{Code: diag.Code(n), Severity: diag.SeverityError})
			}
		}
	}
	exit := diag.ExitCode(exitErrs)
	if o.json {
		for _, w := range fin.Errors {
			o.event("error", struct {
				Error translate.ErrorObject `json:"error"`
			}{workerError(w)})
		}
		for _, d := range errs {
			o.diagnose(d, "")
		}
		return o.finish(exit, r.counts, nil)
	}
	rep := &translate.Report{Errors: errs, ExitCode: exit, Counts: r.counts, BuiltPackages: r.counts.Built}
	translate.HumanTo(o.w, o.errw, rep, o.style)
	o.errs = append(o.errs, errs...)
	return o.finish(exit, r.counts, nil)
}

// workerError is the --json error object of an error the worker reported.
func workerError(w remote.ErrorSummary) translate.ErrorObject {
	class := ""
	if n, err := strconv.Atoi(strings.TrimPrefix(w.Code, "BOB-E-")); err == nil {
		class = diag.Code(n).Class().String()
	}
	return translate.ErrorObject{Code: w.Code, Severity: string(diag.SeverityError), Class: class, Message: w.Message,
		Label: w.Label(), Excerpt: []string{}, Hint: w.Hint, Args: map[string]string{}}
}

// detach ends the command after Ctrl-C: the server build keeps running and bob says how to
// stop it; a second Ctrl-C within 2 s cancels it (SPEC §02.6 step 6). One Ctrl-C can arrive
// twice (from the terminal and forwarded by the outer bob), so signals within 250 ms of the
// first count once.
func (r *remoteRun) detach(api *remote.Server, sigs <-chan os.Signal) int {
	o := r.e.out
	if r.buildID == "" {
		return o.fail(diag.New(8001))
	}
	fmt.Fprintf(o.errw, "detached: remote build %s keeps running on %s\n  stop it with: bob cancel %s\n", r.buildID, remote.Host(r.server), r.buildID)
	first := time.Now()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	cancel := false
wait:
	for {
		select {
		case <-sigs:
			if time.Since(first) > 250*time.Millisecond {
				cancel = true
				break wait
			}
		case <-deadline.C:
			break wait
		}
	}
	if cancel {
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := api.Call(ctx, remote.CancelBuildMethod, map[string]string{"id": r.buildID}, nil); err != nil {
			return o.fail(apiDiag(r.server, remote.CancelBuildMethod, "", err))
		}
		fmt.Fprintf(o.errw, "cancelled remote build %s\n", r.buildID)
	}
	return o.finish(130, r.counts, nil)
}

// runCancel is `bob cancel <build-id>` (R-DEV-005).
func runCancel(e *env) int {
	if len(e.a.pos) != 1 || e.a.pos[0] == "" {
		return e.out.fail(diag.New(4, "flag", "bob cancel", "what", "one build id"))
	}
	id := e.a.pos[0]
	api, err := e.session(e.ctx, "")
	if err != nil {
		return e.out.fail(err)
	}
	var resp struct {
		Build remote.Build `json:"build"`
	}
	if err := api.Call(e.ctx, remote.CancelBuildMethod, map[string]string{"id": id}, &resp); err != nil {
		if ae, ok := remote.AsError(err); ok && ae.Code == "failed_precondition" {
			e.out.result(map[string]string{"buildId": id, "state": "finished"})
			e.out.printf("remote build %s is already finished\n", id)
			return e.out.finish(0, translate.Counts{}, nil)
		}
		return e.out.fail(apiDiag(api.URL, remote.CancelBuildMethod, "", err))
	}
	state := remote.StateName(resp.Build.State)
	e.out.result(map[string]string{"buildId": id, "state": state})
	e.out.printf("cancelled remote build %s (%s)\n", id, state)
	return e.out.finish(0, translate.Counts{}, nil)
}

// logLine is bob.log.v1.LogLine.
type logLine struct {
	Package string       `json:"package"`
	Seq     remote.Int64 `json:"seq"`
	Attempt int32        `json:"attempt"`
	Text    string       `json:"text"`
}

// runServerLog is `bob log --build <id> [<label>] [-f] [--attempt N]` (R-CLI-008): the log of a
// server build from LogService (lake), or followed live.
func runServerLog(e *env) int {
	id := e.a.str("build")
	if id == "" {
		return e.out.fail(diag.New(4, "flag", "--build", "what", "a build id"))
	}
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob log", "what", "at most one label"))
	}
	label := ""
	if len(e.a.pos) == 1 {
		label = e.a.pos[0]
	}
	attempt := 0
	if s := e.a.str("attempt"); s != "" {
		attempt, _ = strconv.Atoi(s)
	}
	api, err := e.session(e.ctx, "")
	if err != nil {
		return e.out.fail(err)
	}
	if label != "" {
		e.out.begin([]string{label})
	} else {
		e.out.begin(nil)
	}
	print := func(l logLine) {
		if attempt > 0 && int(l.Attempt) != attempt {
			return
		}
		if e.out.json {
			e.out.event("log", struct {
				Label string `json:"label"`
				Line  string `json:"line"`
			}{l.Package, l.Text})
			return
		}
		if label == "" {
			fmt.Fprintf(e.out.w, "%s: %s\n", l.Package, l.Text)
			return
		}
		fmt.Fprintln(e.out.w, l.Text)
	}
	if e.a.bool("f") {
		err = followLog(e.ctx, api, id, label, print, func(msg string) { fmt.Fprintln(e.out.errw, msg) })
	} else {
		err = getLog(e.ctx, api, id, label, print)
	}
	if err != nil {
		method := "bob.log.v1.LogService/GetLog"
		if e.a.bool("f") {
			method = "bob.log.v1.LogService/FollowLog"
		}
		return e.out.fail(apiDiag(api.URL, method, "", err))
	}
	return e.out.finish(0, translate.Counts{}, nil)
}

// getLog pages through LogService/GetLog.
func getLog(ctx context.Context, api *remote.Server, id, label string, print func(logLine)) error {
	after := map[string]int64{} // per package: the last seq printed
	for {
		var resp struct {
			Lines     []logLine `json:"lines"`
			Truncated bool      `json:"truncated"`
		}
		req := map[string]any{"buildId": id, "package": label, "limit": 10000}
		if label != "" && after[label] > 0 {
			req["afterSeq"] = strconv.FormatInt(after[label], 10)
		}
		if err := api.Call(ctx, "bob.log.v1.LogService/GetLog", req, &resp); err != nil {
			return err
		}
		progressed := false
		for _, l := range resp.Lines {
			if int64(l.Seq) <= after[l.Package] {
				continue
			}
			after[l.Package] = int64(l.Seq)
			progressed = true
			print(l)
		}
		// Paging by after_seq works for one package; a whole-build log is one page.
		if !resp.Truncated || label == "" || !progressed {
			return nil
		}
	}
}

// followLog streams LogService/FollowLog until the build's log is complete. Lines are printed
// once: a line whose (package, attempt, seq) was printed already is skipped; a restart (a new
// attempt of a package) is announced on stderr.
func followLog(ctx context.Context, api *remote.Server, id, label string, print func(logLine), note func(string)) error {
	type key struct {
		pkg     string
		attempt int32
	}
	seen := map[key]int64{}
	cursor := map[string]map[string]any{}
	for tries := 0; ; tries++ {
		finished := false
		req := map[string]any{"buildId": id, "package": label, "cursor": cursor}
		err := api.Stream(ctx, "bob.log.v1.LogService/FollowLog", req, func(m json.RawMessage) error {
			var resp struct {
				Lines    []logLine `json:"lines"`
				Restarts []struct {
					Package string       `json:"package"`
					Attempt int32        `json:"attempt"`
					FromSeq remote.Int64 `json:"fromSeq"`
				} `json:"restarts"`
				Done bool `json:"done"`
			}
			if err := json.Unmarshal(m, &resp); err != nil {
				return &remote.Error{Method: "bob.log.v1.LogService/FollowLog", Code: "internal", Message: "invalid message: " + err.Error()}
			}
			for _, rs := range resp.Restarts {
				note(fmt.Sprintf("bob: %s restarted (attempt %d) from line %d", rs.Package, rs.Attempt, rs.FromSeq))
			}
			for _, l := range resp.Lines {
				k := key{l.Package, l.Attempt}
				if int64(l.Seq) <= seen[k] {
					continue
				}
				seen[k] = int64(l.Seq)
				cursor[l.Package] = map[string]any{"seq": strconv.FormatInt(int64(l.Seq), 10), "attempt": l.Attempt}
				print(l)
			}
			if resp.Done {
				finished = true
			}
			return nil
		})
		if finished || (err == nil && ctx.Err() == nil) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ae, ok := remote.AsError(err)
		if !ok || !ae.Unreachable() || tries >= 30 {
			return err
		}
		select { // an orchestrator went away: resume from the cursor on another one
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
