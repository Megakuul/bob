//go:build live

package forge

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// ciStates returns the states of the statuses with the given context in write order.
func ciStates(e *regenv.Env, sha, context string) []string {
	var out []string
	for _, s := range e.Statuses("demo", sha) {
		if s.Context == context {
			out = append(out, s.State)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); !ok(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, timeout)
		}
	}
}

// brokenTest makes the fixture's Go test fail, so pipeline ci fails with BOB-E-2005.
const brokenTest = "\nfunc TestBroken(t *testing.T) { t.Fatal(\"broken on purpose\") }\n"

// TestPIPE003PipelinesRunAndReport: with pipelines ci (push main) and release (tag v*), a push to
// main shows only bob/ci (pending -> running -> success) and its target URL opens the build page; a
// failing test gives `failure` with BOB-E-2005. The forged evaluation result of the acceptance text
// needs a worker Attach client and is not written (see the report).
func TestPIPE003PipelinesRunAndReport(t *testing.T) {
	parity.Covers(t, "R-PIPE-003")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	dir := harness.Fixture(t, "acme")
	sha := e.Push(dir, "demo", "main")
	pushed := time.Now()
	e.WaitBuilds("acme", 1, 500*time.Millisecond) // webhook -> BuildRecord <= 500 ms (HARNESS 8.2)
	t.Logf("webhook to BuildRecord: %v", time.Since(pushed))
	b := e.WaitBuilds("acme", 1, time.Second)[0]
	b = e.WaitBuild(b.ID, 20*time.Minute, regenv.Terminal)
	if b.State != "BUILD_STATE_SUCCEEDED" {
		t.Fatalf("build of the push to main: %s %+v", b.State, b.Errors)
	}
	if len(b.Pipelines) != 1 || b.Pipelines[0].Name != "ci" {
		t.Errorf("pipelines run for a push to main: %+v, want only ci", b.Pipelines)
	}
	var contexts []string
	var target string
	for _, s := range e.Statuses("demo", sha) {
		if s.Context != "bob/evaluate" {
			contexts = append(contexts, s.Context)
		}
		if s.Context == "bob/ci" {
			target = s.TargetURL
		}
	}
	for _, c := range contexts {
		if c != "bob/ci" {
			t.Errorf("status context %q appeared; only bob/ci may (release is a tag pipeline)", c)
		}
	}
	states := ciStates(e, sha, "bob/ci")
	if len(states) < 2 || states[0] != "pending" || states[len(states)-1] != "success" {
		t.Errorf("bob/ci states %v, want pending ... success (running is reported as pending by Forgejo)", states)
	}
	for _, s := range states {
		if s == "failure" || s == "error" {
			t.Errorf("bob/ci reported %s for a passing build: %v", s, states)
		}
	}
	if code, _, _ := e.Get(target, e.Admin, nil); code != http.StatusOK {
		t.Errorf("target URL %q: HTTP %d, want 200 (the build page)", target, code)
	}

	// a failing test: failure with BOB-E-2005
	bad := e.CommitAppend(dir, "cmd/api/main_test.go", brokenTest)
	e.Push(dir, "demo", "main")
	waitFor(t, "failure status for the broken commit", 20*time.Minute, func() bool {
		s := ciStates(e, bad, "bob/ci")
		return len(s) > 0 && (s[len(s)-1] == "failure" || s[len(s)-1] == "success")
	})
	var last struct{ State, Description string }
	for _, s := range e.Statuses("demo", bad) {
		if s.Context == "bob/ci" {
			last.State, last.Description = s.State, s.Description
		}
	}
	if last.State != "failure" || !strings.Contains(last.Description, "BOB-E-2005") || !regexp.MustCompile(`^#\d+ `).MatchString(last.Description) {
		t.Errorf("bob/ci for a failing test: %+v, want failure with \"#<n> <state>: BOB-E-2005 ...\"", last)
	}
	e.AssertNoList()
}

// TestPIPE004Triggers: CreateBuild{git sha, pipeline} has trigger API; `bob build --remote` uploads
// the source, has trigger REMOTE and afterwards the CLI substitutes the result (the cache answers
// 200 for the narinfo).
func TestPIPE004Triggers(t *testing.T) {
	parity.Covers(t, "R-PIPE-004")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	dir := harness.Fixture(t, "acme")
	sha := e.Push(dir, "demo", "main")

	var created struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/CreateBuild", map[string]any{
		"projectId": "acme", "source": map[string]any{"git": map[string]any{"ref": "refs/heads/main", "sha": sha}}, "pipeline": "ci"}, &created)
	if created.Build.Trigger.Kind != "TRIGGER_KIND_API" {
		t.Errorf("CreateBuild{git, pipeline}: trigger %q, want TRIGGER_KIND_API", created.Build.Trigger.Kind)
	}
	if b := e.WaitBuild(created.Build.ID, 20*time.Minute, regenv.Terminal); b.State != "BUILD_STATE_SUCCEEDED" {
		t.Errorf("API build: %s %+v", b.State, b.Errors)
	}

	home := t.TempDir()
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_STATE_HOME=" + home + "/.state"}
	login := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Stdin: e.Admin + "\n"}, "login", "--token-stdin", e.API)
	if login.Exit != 0 {
		t.Fatalf("bob login: exit %d\n%s", login.Exit, login.Combined())
	}
	r := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Timeout: 20 * time.Minute}, "build", "--remote", "--print-paths", "//cmd/api:api")
	if r.Exit != 0 {
		t.Fatalf("bob build --remote: exit %d\n%s", r.Exit, r.Combined())
	}
	var remote regenv.Build
	for _, b := range e.Builds("acme") {
		if b.Trigger.Kind == "TRIGGER_KIND_REMOTE" {
			remote = b
		}
	}
	if remote.ID == "" {
		t.Fatalf("bob build --remote created no build with trigger REMOTE")
	}
	paths := regexp.MustCompile(`/nix/store/([0-9a-z]{32})-[^\s"]+`).FindAllStringSubmatch(r.Stdout, -1)
	if len(paths) == 0 {
		t.Fatalf("bob build --remote --print-paths printed no store path:\n%s", r.Stdout)
	}
	if code, _, _ := e.Get(e.API+"/nix/"+paths[0][1]+".narinfo", e.Admin, nil); code != http.StatusOK {
		t.Errorf("narinfo of the remote build result: HTTP %d, want 200", code)
	}
	e.AssertNoList()
}

// TestPIPE005CancelRetryRerunConcurrency covers the four cases of the acceptance text.
func TestPIPE005CancelRetryRerunConcurrency(t *testing.T) {
	parity.Covers(t, "R-PIPE-005")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	dir := harness.Fixture(t, "acme")
	e.Push(dir, "demo", "main")
	api := func(sha string) regenv.Build {
		var c struct{ Build regenv.Build }
		e.MustCall(e.Admin, "bob.build.v1.BuildService/CreateBuild", map[string]any{"projectId": "acme", "pipeline": "ci",
			"source": map[string]any{"git": map[string]any{"ref": "refs/heads/feature", "sha": sha}}}, &c)
		return c.Build
	}

	// (1) two builds of a feature branch 1 s apart: the first ends SUPERSEDED, its status says so
	sha1 := e.CommitAppend(dir, "cmd/api/main.go", "\n// one\n")
	e.Push(dir, "demo", "feature")
	first := api(sha1)
	time.Sleep(time.Second)
	sha2 := e.CommitAppend(dir, "cmd/api/main.go", "\n// two\n")
	e.Push(dir, "demo", "feature")
	second := api(sha2)
	got := e.WaitBuild(first.ID, 10*time.Minute, regenv.Terminal)
	if got.State != "BUILD_STATE_SUPERSEDED" || got.Trigger.SupersededBy != second.ID {
		t.Errorf("(1) first build: state %s superseded_by %q, want SUPERSEDED by %s", got.State, got.Trigger.SupersededBy, second.ID)
	}
	waitFor(t, "cancelled status of the superseded build", 2*time.Minute, func() bool {
		for _, s := range e.Statuses("demo", sha1) {
			if s.State == "error" && strings.Contains(s.Description, fmt.Sprintf("superseded by #%v", second.Number)) {
				return true
			}
		}
		return false
	})

	// (2) two tag pushes with a Queue release pipeline run one after the other
	e.Tag(dir, "demo", "v1.0.0")
	e.Tag(dir, "demo", "v1.0.1")
	waitFor(t, "two tag builds", time.Minute, func() bool { return countTrigger(e, "TRIGGER_KIND_TAG") == 2 })
	var tags []regenv.Build
	for _, b := range e.Builds("acme") {
		if b.Trigger.Kind == "TRIGGER_KIND_TAG" {
			tags = append(tags, b)
		}
	}
	older, newer := tags[len(tags)-1], tags[0] // newest first
	for deadline := time.Now().Add(30 * time.Minute); ; time.Sleep(300 * time.Millisecond) {
		o, n := e.WaitBuild(older.ID, time.Minute, func(regenv.Build) bool { return true }), e.WaitBuild(newer.ID, time.Minute, func(regenv.Build) bool { return true })
		if o.State == "BUILD_STATE_RUNNING" && n.State == "BUILD_STATE_RUNNING" {
			t.Fatalf("(2) both release builds run at once (queue concurrency violated)")
		}
		if !regenv.Terminal(o) && n.State != "BUILD_STATE_PENDING" && n.State != "" {
			t.Fatalf("(2) the newer release build is %s while the older one is %s, want PENDING", n.State, o.State)
		}
		if regenv.Terminal(o) && regenv.Terminal(n) {
			if o.State != "BUILD_STATE_SUCCEEDED" || n.State != "BUILD_STATE_SUCCEEDED" {
				t.Errorf("(2) release builds ended %s and %s", o.State, n.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("(2) release builds did not finish")
		}
	}

	// (3) RetryBuild of a failed build rebuilds only the failed package and its dependents
	e.CommitAppend(dir, "cmd/api/main_test.go", brokenTest)
	e.Push(dir, "demo", "main")
	var failedBuild regenv.Build
	waitFor(t, "failed build of the broken commit", 20*time.Minute, func() bool {
		for _, b := range e.Builds("acme") {
			if b.State == "BUILD_STATE_FAILED" {
				failedBuild = b
				return true
			}
		}
		return false
	})
	var retry struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/RetryBuild", map[string]any{"id": failedBuild.ID}, &retry)
	if retry.Build.Trigger.Kind != "TRIGGER_KIND_RETRY" || retry.Build.Trigger.RetryOf != failedBuild.ID || fmt.Sprint(retry.Build.Attempt) != "2" {
		t.Errorf("(3) retry build trigger %+v attempt %v, want RETRY of %s, attempt 2", retry.Build.Trigger, retry.Build.Attempt, failedBuild.ID)
	}
	rb := e.WaitBuild(retry.Build.ID, 20*time.Minute, regenv.Terminal)
	if rb.Counts.Cached < failedBuild.Counts.Built+failedBuild.Counts.Cached || rb.Counts.Built != 0 {
		t.Errorf("(3) retry counts %+v after %+v: the succeeded packages must be cache hits and nothing but the failing ones rebuilt", rb.Counts, failedBuild.Counts)
	}
	var rerun struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/RerunBuild", map[string]any{"id": failedBuild.ID}, &rerun)
	if rerun.Build.Trigger.Kind != "TRIGGER_KIND_RERUN" || rerun.Build.Trigger.RerunOf != failedBuild.ID {
		t.Errorf("RerunBuild trigger %+v, want RERUN of %s", rerun.Build.Trigger, failedBuild.ID)
	}

	// (4) CancelBuild on a SUCCEEDED build: FailedPrecondition
	var done regenv.Build
	for _, b := range e.Builds("acme") {
		if b.State == "BUILD_STATE_SUCCEEDED" {
			done = b
			break
		}
	}
	if done.ID == "" {
		t.Fatalf("(4) no succeeded build to cancel")
	}
	err := e.Call(e.Admin, "bob.build.v1.BuildService/CancelBuild", map[string]any{"id": done.ID}, nil)
	if err == nil || err.Code != "failed_precondition" || !strings.Contains(err.Message, "build is already finished") {
		t.Errorf("(4) CancelBuild on a SUCCEEDED build: %v, want FailedPrecondition \"build is already finished\"", err)
	}
	e.AssertNoList()
}

func countTrigger(e *regenv.Env, kind string) int {
	n := 0
	for _, b := range e.Builds("acme") {
		if b.Trigger.Kind == kind {
			n++
		}
	}
	return n
}
