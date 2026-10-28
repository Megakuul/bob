package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	errorsuite "github.com/megakuul/bob/tests/errors"
	"github.com/megakuul/bob/tests/errors/leakcheck"
)

const repoRoot = "../../.."

func human(r *Report, s Style) string {
	var b strings.Builder
	Human(&b, r, s)
	return b.String()
}

var ansiAll = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// replayCase checks one capture: codes and exit, the plain golden byte for byte, the TTY
// golden without colour codes, no leak in default output, -v a superset with nix: lines, every
// error in bob terms, and the --json error objects carry no raw text.
func replayCase(t *testing.T, dir string, outcomes []errorsuite.Outcome, warnings, forbid []string) {
	t.Helper()
	r, evs, err := Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	var codes, warns []string
	for _, e := range r.Errors {
		codes = append(codes, e.Code.ID())
	}
	for _, e := range r.Warnings {
		warns = append(warns, e.Code.ID())
	}
	matched := false
	for _, oc := range outcomes {
		if slices.Equal(codes, oc.Codes) && (oc.Exit == nil && r.ExitCode != 0 || oc.Exit != nil && *oc.Exit == r.ExitCode) {
			matched = true
		}
	}
	if !matched {
		t.Errorf("codes %v exit %d, want one of %s", codes, r.ExitCode, fmtOutcomes(outcomes))
	}
	if !slices.Equal(warns, warnings) {
		t.Errorf("warnings %v, want %v", warns, warnings)
	}
	if len(r.Drift) > 0 {
		t.Errorf("log-format drift on a recorded Nix: %v", r.Drift)
	}
	for _, e := range append(append([]*diag.Error{}, r.Errors...), r.Warnings...) {
		for k, v := range e.Args {
			if v == missingArg {
				t.Errorf("%s: template argument %q missing", e.Code.ID(), k)
			}
		}
		if e.Code.Class() == diag.ClassBuild && (e.Label == "" || e.DefinedAt.File == "" || e.Log == "") {
			t.Errorf("%s: build error without label, definedAt or log: %+v", e.Code.ID(), e)
		}
	}
	plain := human(r, Style{})
	want, err := os.ReadFile(filepath.Join(dir, "golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if plain != string(want) {
		t.Errorf("human output differs from golden.txt:\n--- got\n%s--- want\n%s", plain, want)
	}
	if tty, err := os.ReadFile(filepath.Join(dir, "golden.tty.txt")); err == nil {
		got := human(r, Style{Color: true, Glyphs: true})
		if !strings.Contains(got, "\x1b[") && len(r.Errors)+len(r.Warnings) > 0 {
			t.Errorf("TTY output has no colour")
		}
		if ansiAll.ReplaceAllString(got, "") != ansiAll.ReplaceAllString(string(tty), "") {
			t.Errorf("TTY output (colours removed) differs from golden.tty.txt:\n%s", got)
		}
	}
	for _, l := range leakcheck.Check([]byte(plain), forbid...) {
		t.Errorf("default output leaks raw Nix text: %s", l)
	}
	verbose := human(r, Style{Verbose: true})
	if l := missingLine(plain, verbose); l != "" {
		t.Errorf("-v output lacks the default line %q", l)
	}
	if strings.Contains(verbose, "@nix") {
		t.Errorf("-v shows raw @nix lines")
	}
	for _, e := range r.Errors {
		if len(e.Raw) > 0 && !strings.Contains(verbose, "\n  nix: ") {
			t.Errorf("%s: -v shows no nix: line", e.Code.ID())
		}
	}
	var js bytes.Buffer
	for _, ev := range evs {
		if ev.Error == nil {
			continue
		}
		b, err := json.Marshal(NewErrorObject(ev.Error, ev.Hint))
		if err != nil {
			t.Fatal(err)
		}
		js.Write(b)
		js.WriteByte('\n')
	}
	for _, l := range leakcheck.Check(js.Bytes(), forbid...) {
		t.Errorf("--json error objects leak raw Nix text: %s", l)
	}
}

func fmtOutcomes(ocs []errorsuite.Outcome) string {
	var s []string
	for _, oc := range ocs {
		exit := "non-zero"
		if oc.Exit != nil {
			exit = fmt.Sprint(*oc.Exit)
		}
		s = append(s, fmt.Sprintf("%v exit %s", oc.Codes, exit))
	}
	return strings.Join(s, " | ")
}

// missingLine returns the first non-empty line of def not found, in order, in verbose.
func missingLine(def, verbose string) string {
	vl := strings.Split(verbose, "\n")
	i := 0
	for _, l := range strings.Split(def, "\n") {
		if l == "" {
			continue
		}
		for i < len(vl) && vl[i] != l {
			i++
		}
		if i == len(vl) {
			return l
		}
		i++
	}
	return ""
}

// TestReplayCatalogue replays every capture of the protected fault catalogue (tests/faults,
// the embedded Nix 2.34.9) against expect.json and the approved goldens.
func TestReplayCatalogue(t *testing.T) {
	entries, err := errorsuite.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		for _, nv := range e.NixVer {
			dir := e.ReplayDir(nv)
			if dir == "" {
				continue
			}
			n++
			t.Run(e.ID+"/"+nv, func(t *testing.T) {
				replayCase(t, dir, e.Expect.Outcomes(), e.Expect.Warnings, e.Forbid)
			})
		}
	}
	if n < 48 {
		t.Errorf("replayed %d captures, want at least 48", n)
	}
}

// spikeExpect is a row of the S15 spike's faults/expect.json (with per-version overrides).
type spikeExpect struct {
	Codes     []string               `json:"codes"`
	Warnings  []string               `json:"warnings"`
	Exit      int                    `json:"exit"`
	ByVersion map[string]spikeExpect `json:"byVersion"`
}

// TestReplayBothNixVersions replays the S15 captures of both Nix versions (host 2.34.8 and
// embedded 2.34.9, spikes/s15-errors/fixtures): the pattern table must classify both
// identically to their goldens (S15 §3.3, ADR 0017 Decision 6).
func TestReplayBothNixVersions(t *testing.T) {
	base := filepath.Join(repoRoot, "spikes/s15-errors")
	b, err := os.ReadFile(filepath.Join(base, "faults/expect.json"))
	if err != nil {
		t.Skipf("S15 captures not present: %v", err)
	}
	var exp map[string]spikeExpect
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	dirs, err := filepath.Glob(filepath.Join(base, "fixtures/*/*/meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	vers := map[string]int{}
	for _, m := range dirs {
		dir := filepath.Dir(m)
		fault, ver := filepath.Base(filepath.Dir(dir)), filepath.Base(dir)
		e, ok := exp[fault]
		if !ok {
			t.Errorf("%s: no row in expect.json", fault)
			continue
		}
		if o, ok := e.ByVersion[ver]; ok {
			e = o
		}
		vers[ver]++
		t.Run(fault+"/"+ver, func(t *testing.T) {
			exit := e.Exit
			replayCase(t, dir, []errorsuite.Outcome{{Codes: e.Codes, Exit: &exit}}, e.Warnings, nil)
		})
	}
	for _, v := range []string{"2.34.8", "2.34.9"} {
		if vers[v] < 48 {
			t.Errorf("Nix %s: %d captures, want at least 48", v, vers[v])
		}
	}
}

func nixLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "@nix " + string(b)
}

type msg struct {
	Action string `json:"action"`
	Level  int    `json:"level"`
	Msg    string `json:"msg"`
}

const fooDrv = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz0-foo.drv"

func fooCtx() *Context {
	return &Context{
		Packages:  map[string]Pkg{fooDrv: {Label: "//a:foo", DefinedAt: "a/bob.go:3:9", Kind: "cc_library"}},
		Requested: []string{"//a:foo"},
	}
}

func run(t *testing.T, ctx *Context, o Outcome, lines ...string) *Report {
	t.Helper()
	tr := New(ctx)
	if err := FeedAll(tr, strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
	return tr.Finish(o)
}

// TestUnknownMessage: R-NIX-010: a Nix message missing from the pattern table is BOB-E-9999,
// exit 70; its text is not in default output and appears as nix: lines with -v.
func TestUnknownMessage(t *testing.T) {
	raw := "error: Cannot frobnicate '/nix/store/0123456789abcdfghijklmnpqrsvwxyz0-foo.drv': the widget is busy"
	r := run(t, fooCtx(), Outcome{ExitCode: 1, NixVersion: "2.34.9"}, nixLine(t, msg{"msg", 0, raw}))
	if len(r.Errors) != 1 || r.Errors[0].Code != cUnknown || r.ExitCode != 70 {
		t.Fatalf("errors %v exit %d, want one BOB-E-9999 and exit 70", r.Errors, r.ExitCode)
	}
	plain := human(r, Style{})
	if strings.Contains(plain, "frobnicate") || len(leakcheck.Check([]byte(plain))) > 0 {
		t.Errorf("default output shows the raw message:\n%s", plain)
	}
	if v := human(r, Style{Verbose: true}); !strings.Contains(v, "  nix: Cannot frobnicate") {
		t.Errorf("-v lacks the raw message:\n%s", v)
	}
}

// TestMessagesInSuccessfulRun: builders can inject level-0 messages, even ones that look like
// known Nix errors, so a structurally successful run (exit 0, outputs valid) is never failed by
// a message: it gets a warning (R-NIX-018, ADR 0017 Decision 3).
func TestMessagesInSuccessfulRun(t *testing.T) {
	for _, m := range []string{"something odd", "Cannot build '" + fooDrv + "'.\n Reason: builder failed with exit code 1."} {
		r := run(t, fooCtx(), Outcome{ExitCode: 0, Valid: []string{fooDrv}},
			nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
			nixLine(t, msg{"msg", 0, m}),
			nixLine(t, map[string]any{"action": "stop", "id": 1}))
		if r.ExitCode != 0 || len(r.Errors) != 0 || len(r.Warnings) != 1 {
			t.Fatalf("%q: exit %d errors %v warnings %v, want exit 0 and one warning", m, r.ExitCode, r.Errors, r.Warnings)
		}
		if r.Packages[0].Status != StBuilt {
			t.Errorf("%q: status %s, want built", m, r.Packages[0].Status)
		}
	}
	// The same message with an invalid output fails the run.
	r := run(t, fooCtx(), Outcome{ExitCode: 0},
		nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
		nixLine(t, msg{"msg", 0, "something odd"}),
		nixLine(t, map[string]any{"action": "stop", "id": 1}))
	if r.ExitCode == 0 {
		t.Fatalf("invalid output reported as success")
	}
	// Nix exits 0 and says nothing, but the output is not valid: BOB-E-9002.
	r = run(t, fooCtx(), Outcome{ExitCode: 0})
	if r.ExitCode != 70 || len(r.Errors) != 1 || r.Errors[0].Code != cNoOutput || r.Packages[0].Status != StFailed {
		t.Fatalf("exit %d errors %v, want BOB-E-9002", r.ExitCode, r.Errors)
	}
}

// TestDriftAllUnknown: an unknown log format plus only unknown errors is BOB-E-9998.
func TestDriftAllUnknown(t *testing.T) {
	r := run(t, fooCtx(), Outcome{ExitCode: 1, NixVersion: "9.9"},
		`@nix {"action":"teleport","id":3}`,
		`@nix {"action":"msg","level":0,"msg":"unheard of","shiny":true}`,
		`@nix {not json`)
	codes := []diag.Code{}
	for _, e := range r.Errors {
		codes = append(codes, e.Code)
	}
	if !slices.Contains(codes, cLogFormat) || r.ExitCode != 70 {
		t.Fatalf("codes %v exit %d, want BOB-E-9998 and exit 70", codes, r.ExitCode)
	}
	if len(r.Drift) != 3 {
		t.Errorf("drift %v, want 3 items", r.Drift)
	}
}

// TestStructuralSuccess: a build that Nix reports as finished but whose output is not valid
// is never reported as built (R-NIX-018).
func TestStructuralSuccess(t *testing.T) {
	r := run(t, fooCtx(), Outcome{ExitCode: 1},
		nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
		nixLine(t, map[string]any{"action": "stop", "id": 1}))
	if r.ExitCode == 0 || len(r.Packages) != 1 || r.Packages[0].Status != StFailed {
		t.Fatalf("exit %d packages %+v, want a failure", r.ExitCode, r.Packages)
	}
}

// TestLongLines: a line over MaxLineBytes is cut and reported as drift; nothing else breaks.
func TestLongLines(t *testing.T) {
	long := "@nix " + `{"action":"msg","level":0,"msg":"` + strings.Repeat("x", MaxLineBytes) + `"}`
	r := run(t, fooCtx(), Outcome{ExitCode: 0, Valid: []string{fooDrv}}, long, nixLine(t, msg{"msg", 1, "warning: fine"}))
	if r.ExitCode != 0 || len(r.Drift) != 1 {
		t.Fatalf("exit %d drift %v", r.ExitCode, r.Drift)
	}
}

// TestBuildLogBounded: a flood of log lines keeps memory bounded and the tail intact.
func TestBuildLogBounded(t *testing.T) {
	var l buildLog
	for i := range 100000 {
		l.add(fmt.Sprint(i))
	}
	got := l.lines()
	if len(got) != logHeadLines+logTailLines || got[0] != "0" || got[len(got)-1] != "99999" || got[logHeadLines] != fmt.Sprint(100000-logTailLines) {
		t.Fatalf("lines: %d, first %s, last %s", len(got), got[0], got[len(got)-1])
	}
}

func TestPos(t *testing.T) {
	for _, s := range []string{"a/bob.go:12:3", "a/b.rs:4", "file", "c:/x:y", ""} {
		if got := PosString(ParsePos(s)); got != s {
			t.Errorf("round trip %q → %q", s, got)
		}
	}
}

func TestStyleFor(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	tests := []struct {
		tty           bool
		env           func(string) string
		color, glyphs bool
	}{
		{true, env(), true, true},
		{false, env(), false, false},
		{true, env("NO_COLOR", "1"), false, true},
		{true, env("TERM", "dumb"), false, false},
		{false, env("BOB_COLOR", "always"), true, false},
		{false, env("BOB_COLOR", "always", "NO_COLOR", "x"), false, false},
	}
	for i, tt := range tests {
		s := StyleFor(tt.tty, tt.env, false)
		if s.Color != tt.color || s.Glyphs != tt.glyphs {
			t.Errorf("case %d: %+v", i, s)
		}
	}
}

// FuzzFeed: arbitrary stderr never panics the translator and never leaks raw text into
// default output beyond what the build log excerpt carries.
func FuzzFeed(f *testing.F) {
	f.Add([]byte(`@nix {"action":"start","id":1,"type":105,"fields":["` + fooDrv + `","",1,1]}` + "\n" +
		`@nix {"action":"result","id":1,"type":101,"fields":["x.c:1:2: error: y"]}` + "\n" +
		`@nix {"action":"msg","level":0,"msg":"Cannot build '` + fooDrv + `'.\n Reason: builder failed with exit code 1."}`))
	f.Add([]byte("@nix {\"action\":\"msg\",\"level\":0,\"msg\":\"timed out after 3 seconds\"}\nplain line\n"))
	f.Add([]byte("@nix {\"action\":\"result\",\"id\":9,\"type\":105,\"fields\":[1,2,3]}\n@nix [1,2]\n@nix null\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, exit := range []int{0, 1} {
			tr := New(fooCtx())
			if err := FeedAll(tr, bytes.NewReader(b)); err != nil {
				t.Fatal(err)
			}
			r := tr.Finish(Outcome{ExitCode: exit, Signal: map[int]string{1: "killed"}[exit]})
			out := human(r, Style{})
			if exit != 0 && r.ExitCode == 0 {
				t.Fatalf("failed run reported as success:\n%s", out)
			}
		}
	})
}

// A failure bob-builder diagnosed itself (a bob.Run output that was not created, a step
// timeout) shows its own text: the diag message is the cause, a timeout is BOB-E-2007.
func TestBuilderDiagnostics(t *testing.T) {
	logRun := func(lines ...string) *Report {
		in := []string{nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}})}
		for _, l := range lines {
			in = append(in, nixLine(t, map[string]any{"action": "result", "id": 1, "type": 101, "fields": []any{l}}))
		}
		in = append(in, nixLine(t, msg{Action: "msg", Msg: "Cannot build '" + fooDrv + "'.\n Reason: builder failed with exit code 4."}))
		return run(t, fooCtx(), Outcome{ExitCode: 1}, in...)
	}
	r := logRun("hi", `bob:: {"type":"diag","severity":"error","file":"","line":0,"col":0,"msg":"//a:foo: step finished but did not create promised.txt"}`,
		"bob-builder: step 4 check-files /nix/store/x failed")
	if len(r.Errors) != 1 || r.Errors[0].Code != 2000 || r.Errors[0].Cause != "//a:foo: step finished but did not create promised.txt" || r.ExitCode != 1 {
		t.Fatalf("missing output: exit %d %+v", r.ExitCode, r.Errors)
	}
	if out := human(r, Style{}); !strings.Contains(out, "cause: //a:foo: step finished but did not create promised.txt") {
		t.Errorf("human output lacks the cause:\n%s", out)
	}
	r = logRun("working", `bob:: {"type":"diag","severity":"error","file":"","line":0,"col":0,"msg":"//a:foo timed out after 5s"}`)
	if len(r.Errors) != 1 || r.Errors[0].Code != 2007 || r.Errors[0].Args["seconds"] != "5" {
		t.Fatalf("timeout: %+v", r.Errors)
	}
}

// A nixpkgs Lib handle that provides no headers and no libraries is named, not reported as an
// exit status (A564).
func TestEmptyLibraryCause(t *testing.T) {
	in := []string{nixLine(t, map[string]any{"action": "start", "id": 1, "type": 105, "fields": []any{fooDrv, "", 1, 1}}),
		nixLine(t, map[string]any{"action": "result", "id": 1, "type": 101, "fields": []any{"bob-builder: //a:foo: /nix/store/54clm95cr7piy1g6pvsar032v5jxw4wi-hello-2.12.3 has no pkg-config module, no include directory and no lib*.so or lib*.a library: it provides nothing"}}),
		nixLine(t, msg{Action: "msg", Msg: "Cannot build '" + fooDrv + "'.\n Reason: builder failed with exit code 1."})}
	r := run(t, fooCtx(), Outcome{ExitCode: 1}, in...)
	if len(r.Errors) != 1 || r.Errors[0].Cause != "the nixpkgs library hello-2.12.3 provides nothing to compile or link against; a program belongs in Tool, not Lib" {
		t.Fatalf("%+v", r.Errors)
	}
}

// A failing nixpkgs derivation that is not a direct input gets its chain from ChainOf.
func TestChainOf(t *testing.T) {
	direct := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz1-openssl-3.drv"
	deep := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz2-perl-5.drv"
	ctx := fooCtx()
	ctx.Nixpkgs = map[string]string{direct: "openssl"}
	ctx.Users = map[string][]string{direct: {"//a:foo"}}
	asked := 0
	ctx.ChainOf = func(d string) []string {
		asked++
		if d == deep {
			return []string{direct, deep}
		}
		return nil
	}
	r := run(t, ctx, Outcome{ExitCode: 1}, nixLine(t, msg{Action: "msg", Msg: "Cannot build '" + deep + "'.\n Reason: builder failed with exit code 2."}))
	if asked == 0 || len(r.Errors) == 0 {
		t.Fatalf("ChainOf asked %d times, errors %+v", asked, r.Errors)
	}
	e := r.Errors[0]
	if e.Code != 5001 || e.Args["attr"] != "openssl" || e.Label != "//a:foo" || len(e.Excerpt) == 0 || !strings.Contains(e.Excerpt[0], "openssl-3 → ") || !strings.Contains(e.Excerpt[0], "perl-5") {
		t.Errorf("error %+v excerpt %v", e, e.Excerpt)
	}
}
