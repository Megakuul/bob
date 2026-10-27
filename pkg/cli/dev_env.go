package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// The developer environment outside the namespace (SPEC §02.7.1-§02.7.3): bob env, the shell
// hook, bob allow/deny, and the env.json writer every dev command shares.

// shellsIndex lists the dev shell names of the last evaluation (completion of --shell).
const shellsIndex = ".bob/shells"

// bobExe is the absolute path of the running bob (os.Executable), which hooks and shims embed.
func bobExe() string {
	p, err := os.Executable()
	if err != nil {
		return "bob"
	}
	return p
}

// devEnv computes the dev environment of root's shell, realises its packages and records it:
// the env file, the shims (default shell), the hook's stamps and the shell index. inside says
// whether this process runs in bob's namespace (store paths are readable as they are).
func (e *env) devEnv(st *store.Store, root, shell string, inside bool) (*devenv.File, []byte, *build.DevResult, error) {
	dropShims(root, e.getenv(devenv.VarProject))
	res, err := build.Dev(e.ctx, build.Options{Root: root, Store: st, Offline: e.a.bool("offline")}, shell)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(res.Generated) > 0 {
		_ = updateExclude(root, res.Generated, false) // best effort, as bob build
	}
	if err := realiseDev(e, st, root, res.Shell, res.Packages); err != nil {
		return nil, nil, nil, err
	}
	// Outputs the ecosystems' working-tree files name (a no-op when they are valid).
	if err := realiseDrvs(e, st, root, res.Shell+"-prep", res.Realise); err != nil {
		return nil, nil, nil, err
	}
	read := func(p string) string { return p }
	if !inside {
		read = func(p string) string { return build.HostPath(st, p) }
	}
	f := devenv.Compose(devenv.Spec{Root: root, Shell: res.Shell, EvalKey: res.EvalKey, Packages: res.Packages,
		Env: res.Env, InsideEnv: res.InsideEnv, ToolEnv: res.ToolEnv, Tools: res.Tools, Hooks: res.Hooks,
		Servers: res.Servers}, read)
	b, err := f.Encode()
	if err != nil {
		return nil, nil, nil, err
	}
	// The requested shell names the file: "" (the default selection) is env.json, which the
	// hook and the shims use.
	if _, err := devenv.WriteFile(devenv.Path(root, shell), b, 0o644); err != nil {
		return nil, nil, nil, diag.New(7002, "reason", ".bob/env.json cannot be written")
	}
	if shell == "" || shell == "default" {
		if err := devenv.WriteShims(root, bobExe(), f); err != nil {
			return nil, nil, nil, diag.New(7002, "reason", ".bob/bin cannot be written")
		}
	}
	if s, err := devenv.CollectStamps(root, devenv.AllowPath(e.getenv), bobVersion(), devenv.Hash(b)); err == nil {
		_ = devenv.WriteStamps(devenv.StampPath(st.Home(), root, stampShell(shell)), s)
	}
	_, _ = devenv.WriteFile(filepath.Join(root, filepath.FromSlash(shellsIndex)), []byte(strings.Join(res.Shells, "\n")+"\n"), 0o644)
	return f, b, res, nil
}

// ensureShims creates the dev shims (.bob/bin) of root when it has none, so an editor started
// right after bob init, bob lock or bob gen finds the language servers without a prior bob dev
// (A550). Best effort: a failure (offline, a package not yet buildable) is a note, never an
// error of the command. It must run inside the namespace (after enterStore).
func (e *env) ensureShims(st *store.Store, root string) {
	if _, err := os.Stat(devenv.ShimDir(root)); err == nil {
		return
	}
	quiet := *e
	if _, _, _, err := quiet.devEnv(st, root, "", true); err != nil && !e.out.json {
		e.out.printf("note: the editor shims in .bob/bin were not created (%v); run bob dev\n", err)
	}
}

// dropShims takes the shim directories of root and of the active project off this process'
// PATH: the evaluator must not find a shim as its go (outside the namespace the store paths it
// reports do not exist).
func dropShims(roots ...string) {
	drop := map[string]bool{}
	for _, r := range roots {
		if r != "" {
			drop[devenv.ShimDir(r)] = true
		}
	}
	var keep []string
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if !drop[p] {
			keep = append(keep, p)
		}
	}
	_ = os.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
}

// realiseDev makes the dev shell's packages valid (substitute or build) and keeps them alive
// as GC roots below .bob/dev/gcroots/<shell>. Nix's own output stays hidden (-v shows it).
func realiseDev(e *env, st *store.Store, root, shell string, pkgs []engine.DevPackage) error {
	var paths []string
	for _, p := range pkgs {
		paths = append(paths, p.Path)
	}
	invalid, err := st.Invalid(e.ctx, paths)
	if err != nil || len(invalid) == 0 {
		return err
	}
	missing := map[string]bool{}
	for _, p := range invalid {
		missing[p] = true
	}
	var drvs []string
	seen := map[string]bool{}
	for _, p := range pkgs {
		if missing[p.Path] && p.Drv != "" && !seen[p.Drv] {
			seen[p.Drv] = true
			drvs = append(drvs, p.Drv)
		}
	}
	return realiseDrvs(e, st, root, shell, drvs)
}

// realiseDrvs runs nix-store --realise on drvs with indirect GC roots in the project.
func realiseDrvs(e *env, st *store.Store, root, shell string, drvs []string) error {
	if len(drvs) == 0 {
		return nil
	}
	gc := filepath.Join(root, ".bob", "dev", "gcroots", shell)
	if err := os.MkdirAll(gc, 0o755); err != nil {
		return diag.New(7002, "reason", ".bob/dev cannot be written")
	}
	args := []string{"--realise", "--add-root", filepath.Join(gc, "root")}
	if e.a.bool("offline") {
		args = append(args, "--option", "substituters", "")
	}
	var out bytes.Buffer
	c := st.Command(e.ctx, "nix-store", append(args, drvs...)...)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		if e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		d := diag.New(9005, "reason", "the dev shell's tools could not be downloaded or built (run with -v for details)")
		d.Raw = strings.Split(strings.TrimSpace(out.String()), "\n")
		return d
	}
	return nil
}

// runEnv is `bob env [--shell <name>] [--format sh|fish|json] [--hook=bash|zsh|fish]`.
func runEnv(e *env) int {
	if h := e.a.str("hook"); h != "" || e.a.has("hook") {
		return runEnvHook(e, h)
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	format := e.a.str("format")
	switch format {
	case "", "sh", "fish", "json":
	default:
		return e.out.fail(diag.New(4, "flag", "--format", "what", "sh, fish or json"))
	}
	st, err := store.Open(e.ctx, store.Config{})
	if err != nil {
		return e.out.fail(err)
	}
	f, b, _, err := e.devEnv(st, root, e.a.str("shell"), false)
	if err != nil {
		return e.out.fail(err)
	}
	if e.out.json {
		e.out.result(f.Result())
		return e.out.finish(0, translate.Counts{}, nil)
	}
	if format == "json" { // the result event's data (SPEC §02.4), with outside.prepend.PATH
		j, err := json.Marshal(f.Result())
		if err != nil {
			return e.out.fail(err)
		}
		e.out.printf("%s\n", j)
		return 0
	}
	shell := "sh"
	if format == "fish" {
		shell = "fish"
	}
	s := devenv.NewScript(shell, os.Environ())
	if p, _ := s.Get(devenv.VarProject); p != "" {
		s.Leave()
	}
	s.Enter(root, f, devenv.Hash(b))
	e.out.printf("%s", s.String())
	return 0
}

// runEnvHook is `bob env --hook=<shell>`, run by the shell hook at every prompt (SPEC §02.7.1).
// Its fast path stats a few files and prints nothing; it never enters the namespace itself.
func runEnvHook(e *env, shell string) int {
	switch shell {
	case "bash", "zsh", "fish":
	default:
		return e.out.fail(diag.New(1, "kind", "shell", "name", shell, "suggest", "bash, zsh or fish"))
	}
	if e.getenv(devenv.VarDev) == "1" {
		return 0
	}
	active := e.getenv(devenv.VarProject)
	root, err := findRoot(e.cwd, false)
	if err != nil {
		root = ""
	}
	s := devenv.NewScript(shell, os.Environ())
	if root == "" {
		if active != "" {
			s.Leave()
		}
		e.out.printf("%s", s.String())
		return 0
	}
	envPath := devenv.Path(root, "default")
	home, herr := store.HomeDir("")
	// Fast path (R-DEV-007): the active project, its env.json unchanged, nothing the eval key
	// depends on (nor the trust list) changed since env.json was written.
	if root == active && herr == nil {
		if b, err := os.ReadFile(envPath); err == nil {
			h := devenv.Hash(b)
			if h == e.getenv(devenv.VarEnvHash) && devenv.ReadStamps(devenv.StampPath(home, root, "default")).Fresh(bobVersion(), h) {
				return 0
			}
		}
	}
	if !devenv.IsAllowed(devenv.AllowPath(e.getenv), root) {
		if active != "" {
			s.Leave()
		}
		seen := devenv.DeniedSeen(e.getenv(devenv.VarDeniedSeen))
		if !contains(seen, root) {
			fmt.Fprintf(e.out.errw, "bob: %s is not allowed; run 'bob allow' to load its environment\n", root)
			s.Set(devenv.VarDeniedSeen, devenv.EncodeDeniedSeen(append(seen, root)))
		}
		e.out.printf("%s", s.String())
		return 0
	}
	f, b, err := hookEnv(e, root, envPath, active)
	if err != nil {
		// The previous environment stays; the error goes to stderr (the hook evals stdout).
		e.out.json = false
		for _, d := range diagErrors(err) {
			e.out.diagnose(d, d.Hint())
		}
		return 0
	}
	h := devenv.Hash(b)
	if root == active && h == e.getenv(devenv.VarEnvHash) {
		return 0
	}
	if active != "" {
		s.Leave()
	}
	s.Enter(root, f, h)
	e.out.printf("%s", s.String())
	return 0
}

// hookEnv returns root's env.json for the hook: re-evaluated when its stamps are stale or it
// is missing, else as it is.
func hookEnv(e *env, root, envPath, active string) (*devenv.File, []byte, error) {
	home, err := store.HomeDir("")
	if err != nil {
		return nil, nil, err
	}
	f, b, rerr := devenv.Read(envPath)
	allow := devenv.AllowPath(e.getenv)
	if rerr == nil && devenv.ReadStamps(devenv.StampPath(home, root, "default")).FreshIgnoring(bobVersion(), devenv.Hash(b), allow) {
		// Only the trust list changed: refresh the stamps.
		if s, err := devenv.CollectStamps(root, allow, bobVersion(), devenv.Hash(b)); err == nil {
			_ = devenv.WriteStamps(devenv.StampPath(home, root, "default"), s)
		}
		return f, b, nil
	}
	if rerr != nil || root != active {
		fmt.Fprintf(e.out.errw, "bob: loading %s (dev shell default)…\n", root)
	}
	st, err := store.Open(e.ctx, store.Config{})
	if err != nil {
		return nil, nil, err
	}
	f, b, _, err = e.devEnv(st, root, "", false)
	return f, b, err
}

// runHook is `bob hook bash|zsh|fish`.
func runHook(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob hook", "what", "a shell (bash, zsh or fish)"))
	}
	s, err := devenv.Hook(e.a.pos[0], bobExe())
	if err != nil {
		return e.out.fail(err)
	}
	if e.out.json {
		e.out.result(struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}{e.a.pos[0], s})
		return e.out.finish(0, translate.Counts{}, nil)
	}
	e.out.printf("%s", s)
	return 0
}

// runAllow is `bob allow [dir]`; runDeny is `bob deny [dir]` (R-DEV-009).
func runAllow(e *env) int { return setAllowed(e, true) }
func runDeny(e *env) int  { return setAllowed(e, false) }

func setAllowed(e *env, allow bool) int {
	if len(e.a.pos) > 1 {
		return e.out.fail(diag.New(4, "flag", "bob "+e.cmd.name, "what", "at most one directory"))
	}
	dir := e.cwd
	if len(e.a.pos) == 1 {
		dir = e.a.pos[0]
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.cwd, dir)
		}
	}
	root, err := findRoot(filepath.Clean(dir), false)
	if err != nil {
		if allow {
			return e.out.fail(err)
		}
		root = filepath.Clean(dir) // deny works for a root that is gone
	}
	p := devenv.AllowPath(e.getenv)
	if err := devenv.SetAllowed(p, root, allow); err != nil {
		reason := "the trust list " + p + " cannot be written"
		if errors.Is(err, fs.ErrPermission) {
			reason += " (permission denied)"
		}
		return e.out.fail(diag.New(7002, "reason", reason))
	}
	if e.out.json {
		e.out.result(struct {
			Root    string `json:"root"`
			Allowed bool   `json:"allowed"`
		}{root, allow})
		return e.out.finish(0, translate.Counts{}, nil)
	}
	if !e.out.quiet {
		verb := "allowed"
		if !allow {
			verb = "denied"
		}
		fmt.Fprintf(e.out.errw, "bob: %s %s\n", verb, root)
	}
	if allow {
		// Load the environment now, while the user waits for an explicit command, so the next
		// prompt only activates it. A broken bob.go is reported; the root stays allowed.
		if _, _, err := hookEnv(e, root, devenv.Path(root, "default"), ""); err != nil {
			for _, d := range diagErrors(err) {
				e.out.diagnose(d, "")
			}
		}
	}
	return 0
}

// readShells returns the dev shell names recorded for root (completion).
func readShells(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(shellsIndex)))
	if err != nil || len(b) > 1<<20 {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
