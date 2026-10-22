package devenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/errors/leakcheck"
	"github.com/megakuul/bob/tests/parity"
)

// hookScripts are the "exact hook output" blocks of SPEC §02.7.1 (<bob> is filled in).
var hookScripts = map[string]string{
	"bash": `# bob shell hook (bash)
_bob_hook() {
  local s=$?
  eval "$("<bob>" env --hook=bash)"
  return $s
}
if [[ ";${PROMPT_COMMAND[*]:-};" != *";_bob_hook;"* ]]; then
  if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
    PROMPT_COMMAND=(_bob_hook "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_bob_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi`,
	"zsh": `# bob shell hook (zsh)
_bob_hook() { eval "$("<bob>" env --hook=zsh)" }
typeset -ag precmd_functions chpwd_functions
(( ${precmd_functions[(I)_bob_hook]} )) || precmd_functions=(_bob_hook $precmd_functions)
(( ${chpwd_functions[(I)_bob_hook]} )) || chpwd_functions=(_bob_hook $chpwd_functions)`,
	"fish": `# bob shell hook (fish)
function __bob_hook --on-event fish_prompt --on-variable PWD
    "<bob>" env --hook=fish | source
end`,
}

// TestRDEV003HookScripts: bob hook <shell> prints exactly the SPEC text with the absolute path of
// the running bob.
func TestRDEV003HookScripts(t *testing.T) {
	parity.Covers(t, "R-DEV-003")
	w := newWorld(t)
	bob := bobBin(t)
	for shell, want := range hookScripts {
		t.Run(shell, func(t *testing.T) {
			r := w.bob(w.scratch(), "hook", shell)
			if redIf(t, r) || !exitIs(t, r, 0) {
				return
			}
			want := strings.ReplaceAll(want, "<bob>", bob)
			if got := strings.TrimRight(r.Stdout, "\n"); got != want {
				t.Errorf("bob hook %s differs from SPEC §02.7.1\n got:\n%s\nwant:\n%s", shell, got, want)
			}
			if r.Stderr != "" {
				t.Errorf("bob hook %s wrote to stderr: %s", shell, r.Stderr)
			}
		})
	}
	if r := w.bob(w.scratch(), "hook", "tcsh"); !redIf(t, r) && r.Exit == 0 {
		t.Errorf("bob hook tcsh: exit 0, an unknown shell must fail")
	}
}

// ignoredEnv are variables a shell changes on its own when it changes directory.
var ignoredEnv = map[string]bool{"PWD": true, "OLDPWD": true, "_": true, "SHLVL": true}

// sameEnv reports the differences between two environments, minus the shell's own variables.
func sameEnv(a, b map[string]string) []string {
	var diffs []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if ignoredEnv[k] {
			continue
		}
		av, aok := a[k]
		bv, bok := b[k]
		if aok != bok || av != bv {
			diffs = append(diffs, k)
		}
	}
	return diffs
}

// TestRDEV003AutomaticActivation: for bash, zsh and fish through a pty: cd into an allowed
// project puts its shims first on PATH and sets the dev shell's outside variables; cd out restores
// the environment byte for byte (including unset variables); moving inside a project is silent;
// nested projects switch.
func TestRDEV003AutomaticActivation(t *testing.T) {
	parity.Covers(t, "R-DEV-003")
	for _, preset := range []bool{false, true} {
		name := "unset"
		if preset {
			name = "preset"
		}
		for _, sp := range shellSpecs("<bob>") {
			t.Run(name+"/"+sp.name, func(t *testing.T) {
				hookScenario(t, sp.name, preset)
			})
		}
	}
}

func hookScenario(t *testing.T, shell string, preset bool) {
	w := newWorld(t)
	bob := bobBin(t)
	a := w.fixture("acme")
	b := w.projectB(a, "sub")
	for _, d := range []string{a, b} {
		r := w.bob(d, "allow")
		if redIf(t, r) || !exitIs(t, r, 0) {
			return
		}
	}
	if r := w.bob(a, "hook", shell); redIf(t, r) {
		return
	}
	var sp shellSpec
	for _, s := range shellSpecs(bob) {
		if s.name == shell {
			sp = s
		}
	}
	var extra []string
	if preset {
		extra = []string{"CGO_ENABLED=7"}
	}
	base := filepath.Dir(a)
	s := startShell(t, w, sp, base, extra...)
	s.prompt()
	s.run(sp.hook)
	s.run("true")
	before := s.environ()
	if _, set := before["BOB_PROJECT"]; set {
		t.Fatalf("BOB_PROJECT is set outside a project")
	}

	// cd acme: shims first, outside variables set.
	hookOut := s.cd(a)
	if l := leakcheck.Check([]byte(hookOut)); len(l) > 0 {
		t.Errorf("raw Nix text in the hook output: %v", l)
	}
	if got, want := s.capture("command -v gopls"), filepath.Join(a, ".bob", "bin", "gopls"); got != want {
		t.Errorf("command -v gopls = %q, want %q (hook output: %q)", got, want, hookOut)
	}
	inA := s.environ()
	if inA["CGO_ENABLED"] != "0" {
		t.Errorf("CGO_ENABLED = %q in acme, want 0", inA["CGO_ENABLED"])
	}
	if inA["ACME_ENV"] != "dev" {
		t.Errorf("ACME_ENV = %q in acme, want dev (dev shell variable)", inA["ACME_ENV"])
	}
	if inA["BOB_PROJECT"] != a {
		t.Errorf("BOB_PROJECT = %q, want %q", inA["BOB_PROJECT"], a)
	}
	if !strings.HasPrefix(inA["PATH"], filepath.Join(a, ".bob", "bin")+":") {
		t.Errorf("PATH does not start with the shim directory: %q", inA["PATH"])
	}
	hash := inA["BOB_ENV_HASH"]
	if len(hash) != 16 {
		t.Errorf("BOB_ENV_HASH = %q, want 16 hex digits", hash)
	}

	// cd acme/libs: no output, same hash.
	if out := s.cd(filepath.Join(a, "libs")); out != "" {
		t.Errorf("cd acme/libs: the hook printed %q, want nothing", out)
	}
	if got := s.environ()["BOB_ENV_HASH"]; got != hash {
		t.Errorf("BOB_ENV_HASH changed inside the project: %q → %q", hash, got)
	}
	if out := s.run("true"); out.During != "" || out.After != "" {
		t.Errorf("an idle prompt printed %+v", out)
	}

	// cd /: the environment is restored exactly.
	s.cd("/")
	after := s.environ()
	if d := sameEnv(before, after); len(d) > 0 {
		t.Errorf("cd / did not restore the environment: differing variables %v (PATH before %q after %q; CGO_ENABLED before %q/%v after %q/%v)",
			d, before["PATH"], after["PATH"], before["CGO_ENABLED"], has(before, "CGO_ENABLED"), after["CGO_ENABLED"], has(after, "CGO_ENABLED"))
	}
	if after["PATH"] != before["PATH"] {
		t.Errorf("PATH after leaving: %q, before: %q", after["PATH"], before["PATH"])
	}

	// Nested project: B, back to A.
	s.cd(a)
	s.cd(b)
	inB := s.environ()
	if inB["BOB_PROJECT"] != b || inB["B_ENV"] != "b" {
		t.Errorf("in nested project: BOB_PROJECT=%q B_ENV=%q, want %q and b", inB["BOB_PROJECT"], inB["B_ENV"], b)
	}
	if _, ok := inB["ACME_ENV"]; ok {
		t.Errorf("ACME_ENV leaked into the nested project")
	}
	if p := inB["PATH"]; !strings.HasPrefix(p, filepath.Join(b, ".bob", "bin")+":") || strings.Contains(p, filepath.Join(a, ".bob", "bin")) {
		t.Errorf("PATH in the nested project: %q", p)
	}
	s.cd(a)
	back := s.environ()
	if back["BOB_PROJECT"] != a || back["ACME_ENV"] != "dev" {
		t.Errorf("back in acme: BOB_PROJECT=%q ACME_ENV=%q", back["BOB_PROJECT"], back["ACME_ENV"])
	}
	if _, ok := back["B_ENV"]; ok {
		t.Errorf("B_ENV leaked back into acme")
	}
	if p := back["PATH"]; !strings.HasPrefix(p, filepath.Join(a, ".bob", "bin")+":") || strings.Contains(p, filepath.Join(b, ".bob", "bin")) {
		t.Errorf("PATH back in acme: %q", p)
	}
	s.cd("/")
	if d := sameEnv(before, s.environ()); len(d) > 0 {
		t.Errorf("leaving the last project did not restore the environment: %v", d)
	}
}

func has(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// TestRDEV007HookCost: the fast path of bob env --hook=bash, 100 runs, p95 within budget B-079,
// and (strace) it opens no files besides bob.lock lookups, .bob/env.json and the eval-key index,
// spawns nothing and enters no namespace.
func TestRDEV007HookCost(t *testing.T) {
	parity.Covers(t, "R-DEV-007")
	w := newWorld(t)
	bob := bobBin(t)
	a := w.fixture("acme")
	if r := w.bob(a, "allow"); redIf(t, r) || !exitIs(t, r, 0) {
		return
	}
	// First run: enter. Its exports are the state of a shell in the project.
	r := w.bob(a, "env", "--hook=bash")
	if redIf(t, r) || !exitIs(t, r, 0) {
		return
	}
	state := exports(r.Stdout)
	if state["BOB_PROJECT"] != a || state["BOB_ENV_HASH"] == "" {
		t.Fatalf("the enter script does not set BOB_PROJECT/BOB_ENV_HASH:\n%s", r.Stdout)
	}
	var env []string
	for k, v := range state {
		env = append(env, k+"="+v)
	}
	fast := func(argv ...string) harnessResult {
		return w.run(runOpts{dir: a, env: env}, append(argv, bob, "env", "--hook=bash")...)
	}
	if f := fast(); f.Stdout != "" || f.Exit != 0 {
		t.Fatalf("the fast path printed %q (exit %d, stderr %q), want nothing", f.Stdout, f.Exit, f.Stderr)
	}
	const runs = 100
	ds := make([]time.Duration, 0, runs)
	for range runs {
		t0 := time.Now()
		f := fast()
		ds = append(ds, time.Since(t0))
		if f.Exit != 0 || f.Stdout != "" {
			t.Fatalf("fast path run failed: exit %d stdout %q stderr %q", f.Exit, f.Stdout, f.Stderr)
		}
	}
	limit := time.Duration(profile(10, 20) * float64(time.Millisecond))
	if got := p95(ds); got > limit {
		t.Errorf("hook fast path p95 = %s, budget (B-079) %s", got, limit)
	}

	// strace: no evaluator, no namespace, only the allowed files.
	strace := pinned(t, "strace", "strace", "strace")
	log := filepath.Join(w.scratch(), "strace.log")
	if err := os.WriteFile(log, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	w.acc.chownTree(t, filepath.Dir(log))
	f := fast(strace, "-f", "-o", log, "-e", "trace=openat,open,execve,execveat,unshare,setns,mount,pivot_root,clone,clone3", "--")
	if f.Exit != 0 || f.Stdout != "" {
		t.Fatalf("strace run: exit %d stdout %q stderr %q", f.Exit, f.Stdout, f.Stderr)
	}
	checkHookTrace(t, readFile(t, log), a, w.home)
}

// checkHookTrace verifies an strace log of the fast path.
func checkHookTrace(t *testing.T, trace, root, home string) {
	t.Helper()
	execs := 0
	for _, l := range strings.Split(trace, "\n") {
		if l == "" || strings.Contains(l, "ENOENT") {
			continue
		}
		switch {
		case strings.Contains(l, "execve("):
			execs++
		case strings.Contains(l, "unshare(") || strings.Contains(l, "setns(") || strings.Contains(l, "mount(") ||
			strings.Contains(l, "pivot_root(") || strings.Contains(l, "CLONE_NEWUSER") || strings.Contains(l, "CLONE_NEWNS"):
			t.Errorf("the fast path entered a namespace: %s", l)
		case strings.Contains(l, "open"):
			i := strings.Index(l, `"`)
			j := strings.Index(l[i+1:], `"`)
			if i < 0 || j < 0 {
				continue
			}
			p := l[i+1 : i+1+j]
			if fastPathMayOpen(p, root, home) {
				continue
			}
			t.Errorf("the fast path opened %s (allowed: bob.lock lookups, .bob/env.json, the eval-key index): %s", p, l)
		}
	}
	if execs != 1 {
		t.Errorf("the fast path ran %d programs, want 1 (bob itself)", execs)
	}
}

// fastPathMayOpen allows the loader and runtime files of a static Go binary and the files named
// in SPEC R-DEV-007.
func fastPathMayOpen(p, root, home string) bool {
	switch {
	case strings.HasPrefix(p, "/proc/"), strings.HasPrefix(p, "/sys/"), strings.HasPrefix(p, "/dev/"):
		return true // Go runtime start-up (cpu count, hugepage size, urandom)
	case filepath.Base(p) == "bob.lock":
		return true
	case p == filepath.Join(root, ".bob", "env.json"), p == filepath.Join(root, ".bob"):
		return true
	case strings.HasPrefix(p, filepath.Join(home, ".local", "share", "bob")+"/") && strings.Contains(p, "eval"):
		return true // the §01 eval-key index lives in bob's state
	case strings.HasPrefix(p, filepath.Join(home, ".cache", "bob")+"/") && strings.Contains(p, "eval"):
		return true
	}
	return false
}

// TestRDEV009Trust: without bob allow the hook prints the "not allowed" line once per shell, runs
// no evaluator, sets no BOB_PROJECT; bob allow activates at the next prompt; bob deny deactivates;
// allow.json has the documented shape and mode.
func TestRDEV009Trust(t *testing.T) {
	parity.Covers(t, "R-DEV-009")
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) { trustScenario(t, shell) })
	}
	t.Run("no-spawn", func(t *testing.T) {
		w := newWorld(t)
		a := w.fixture("acme")
		strace := pinned(t, "strace", "strace", "strace")
		logDir := w.scratch()
		w.acc.chownTree(t, logDir)
		log := filepath.Join(logDir, "strace.log")
		r := w.run(runOpts{dir: a}, strace, "-f", "-o", log, "-e", "trace=execve,execveat", "--", bobBin(t), "env", "--hook=bash")
		if redIf(t, r) {
			return
		}
		want := "bob: " + a + " is not allowed; run 'bob allow' to load its environment"
		if !strings.Contains(r.Stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.Stderr)
		}
		if strings.Contains(r.Stdout, "BOB_PROJECT") {
			t.Errorf("a denied root exports BOB_PROJECT:\n%s", r.Stdout)
		}
		execs := 0
		for _, l := range strings.Split(readFile(t, log), "\n") {
			if strings.Contains(l, "execve(") && !strings.Contains(l, "ENOENT") {
				execs++
			}
		}
		if execs != 1 {
			t.Errorf("a denied root spawned %d programs, want only the hook's bob", execs)
		}
		if exists(filepath.Join(a, ".bob", "env.json")) {
			t.Errorf(".bob/env.json was written for a root that is not allowed (the evaluator ran)")
		}
	})
}

func trustScenario(t *testing.T, shell string) {
	w := newWorld(t)
	bob := bobBin(t)
	a := w.fixture("acme")
	if r := w.bob(a, "hook", shell); redIf(t, r) {
		return
	}
	var sp shellSpec
	for _, s := range shellSpecs(bob) {
		if s.name == shell {
			sp = s
		}
	}
	s := startShell(t, w, sp, filepath.Dir(a))
	s.prompt()
	s.run(sp.hook)
	s.run("true")

	notAllowed := "bob: " + a + " is not allowed; run 'bob allow' to load its environment"
	out := s.cd(a)
	if strings.Count(out, notAllowed) != 1 {
		t.Errorf("cd into a root that is not allowed printed %q, want the line %q exactly once", out, notAllowed)
	}
	if again := s.run("true"); strings.Contains(again.During+again.After, "not allowed") {
		t.Errorf("the not-allowed line is printed again at the next prompt: %+v", again)
	}
	if out := s.cd(filepath.Join(a, "libs")); strings.Contains(out, "not allowed") {
		t.Errorf("the not-allowed line is printed again inside the same root: %q", out)
	}
	if e := s.environ(); has(e, "BOB_PROJECT") || strings.Contains(e["PATH"], ".bob/bin") {
		t.Errorf("a root that is not allowed was activated: BOB_PROJECT=%q PATH=%q", e["BOB_PROJECT"], e["PATH"])
	}
	if exists(filepath.Join(a, ".bob", "env.json")) {
		t.Errorf(".bob/env.json exists: the hook evaluated bob.go of a root that is not allowed")
	}

	// bob allow: the next prompt activates.
	s.run(quote(bob) + " allow")
	if e := s.environ(); e["BOB_PROJECT"] != a {
		t.Errorf("after bob allow: BOB_PROJECT = %q, want %q", e["BOB_PROJECT"], a)
	}
	state := filepath.Join(w.home, ".local", "state", "bob", "allow.json")
	st, err := os.Stat(state)
	if err != nil {
		t.Fatalf("allow.json: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("allow.json mode %v, want 0600", st.Mode().Perm())
	}
	var al struct {
		V     int      `json:"v"`
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal([]byte(readFile(t, state)), &al); err != nil {
		t.Fatalf("allow.json is not JSON: %v", err)
	}
	if al.V != 1 || !reflect.DeepEqual(al.Roots, []string{a}) {
		t.Errorf("allow.json = %+v, want {v:1 roots:[%s]}", al, a)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(readFile(t, state)), &raw)
	if len(raw) != 2 {
		t.Errorf("allow.json has keys beyond v and roots: %v", raw)
	}

	// bob deny: deactivated at the next prompt.
	s.run(quote(bob) + " deny")
	if e := s.environ(); has(e, "BOB_PROJECT") || strings.Contains(e["PATH"], ".bob/bin") {
		t.Errorf("after bob deny the project is still active: BOB_PROJECT=%q PATH=%q", e["BOB_PROJECT"], e["PATH"])
	}
}
