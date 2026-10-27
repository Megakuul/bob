package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// command is one entry of the command table (SPEC §02.1).
type command struct {
	name     string
	synopsis []string
	summary  string
	flags    []flag
	run      func(*env) int
}

// commands is the user command set of SPEC §02.1. It has no server command (R-ARCH-004).
// The table is built by a function so the run fields may refer back to it (help, completion).
func commands() []command {
	return []command{
		{"build", []string{"bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]"}, "build packages",
			[]flag{{name: "remote"}, {name: "platform", kind: fStr}, {name: "variant", kind: fStr}, {name: "version", kind: fStr}, {name: "print-paths"}, {name: "keep-going"}, {name: "lock"}, {name: "no-download"}, {name: "dry-run"}}, runBuildCmd},
		{"test", []string{"bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]"}, "run tests",
			[]flag{{name: "remote"}, {name: "rerun"}, {name: "lock"}, {name: "require-all-tests"}, {name: "platform", kind: fStr}, {name: "variant", kind: fStr}, {name: "keep-going"}}, runTest},
		{"run", []string{"bob run <label> [--platform native] [-- args]"}, "build and run a binary",
			[]flag{{name: "platform", kind: fStr}, {name: "bin", kind: fStr}}, runRun},
		{"dev", []string{"bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]"}, "enter the developer environment",
			[]flag{{name: "shell", kind: fStr}, {name: "no-servers"}, {name: "servers-only"}}, runDev},
		{"env", []string{"bob env [--shell <name>] [--format sh|fish|json] [--hook=bash|zsh|fish]"}, "print the developer environment",
			[]flag{{name: "shell", kind: fStr}, {name: "format", kind: fStr}, {name: "hook", kind: fStr}}, runEnv},
		{"hook", []string{"bob hook bash|zsh|fish"}, "print the shell hook", nil, runHook},
		{"allow", []string{"bob allow [dir]"}, "allow the shell hook in a directory", nil, runAllow},
		{"deny", []string{"bob deny [dir]"}, "deny the shell hook in a directory", nil, runDeny},
		{"exec", []string{"bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]"}, "run a tool of the developer environment",
			[]flag{{name: "lsp"}, {name: "project", kind: fStr}, {name: "shell", kind: fStr}, {name: "installed", kind: fStr}}, runExecCmd},
		{"editor", []string{"bob editor vscode|nvim|helix|zed|jetbrains [--write]"}, "set up an editor",
			[]flag{{name: "write"}}, runEditor},
		{"check", []string{"bob check [--fix]"}, "check bob.go files and bob.lock",
			[]flag{{name: "fix"}, {name: "determinism"}}, runCheck},
		{"gen", []string{"bob gen [patterns…] [--check] [--force]"}, "write native tooling files and generated code",
			[]flag{{name: "check"}, {name: "force"}}, runGen},
		{"status", []string{"bob status [--json]"}, "show the state of generated files and the lock", nil, runStatus},
		{"init", []string{"bob init [dir] [--lang go|cc|rust|npm|ocaml]"}, "start a new project",
			[]flag{{name: "lang", kind: fStr}}, runInit},
		{"lock", []string{"bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]"}, "update bob.lock",
			[]flag{{name: "update"}, {name: "nixpkgs", kind: fStr}, {name: "rebuild"}}, runLock},
		{"graph", []string{"bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text|dot|json]"}, "show the package graph",
			[]flag{{name: "why", kind: fTwo}, {name: "all"}, {name: "rdeps"}, {name: "depth", kind: fInt}, {name: "format", kind: fStr}}, runGraph},
		{"log", []string{"bob log <label> [--build <id>] [-f] [--attempt N]"}, "show a build log",
			[]flag{{name: "build", kind: fStr}, {name: "f", short: true}, {name: "attempt", kind: fInt}}, runLog},
		{"cancel", []string{"bob cancel <build-id>"}, "cancel a remote build", nil, runCancel},
		{"pipeline", []string{"bob pipeline run <name> [--remote]"}, "run a pipeline",
			[]flag{{name: "remote"}}, runPipelineCmd},
		{"login", []string{"bob login [<url>] [--device] [--token-stdin]"}, "log in to a bob server",
			[]flag{{name: "device"}, {name: "token-stdin"}}, runLogin},
		{"logout", []string{"bob logout [<url>]"}, "log out of a bob server", nil, runLogout},
		{"whoami", []string{"bob whoami [<url>]"}, "show the login of a bob server", nil, runWhoAmI},
		{"install", []string{"bob install <ref> [--as <name>] [--bin-dir <dir>]"}, "install a release",
			[]flag{{name: "as", kind: fStr}, {name: "bin-dir", kind: fStr}}, runInstall},
		{"uninstall", []string{"bob uninstall <ref>"}, "remove an installed release", nil, runUninstall},
		{"upgrade", []string{"bob upgrade"}, "upgrade installed releases", []flag{{name: "minor-only"}}, runUpgrade},
		{"rollback", []string{"bob rollback"}, "go back to the previous install generation", []flag{{name: "to", kind: fInt}}, runRollback},
		{"list", []string{"bob list"}, "list installed releases", []flag{{name: "available"}}, runList},
		{"registry", []string{"bob registry add <name> <url> | remove <name> | list | default <name>"}, "manage release registries",
			[]flag{{name: "key", kind: fStr}, {name: "refresh-keys"}}, runRegistry},
		{"completion", []string{"bob completion bash|zsh|fish"}, "print shell completion", nil, runCompletion},
		{"gc", []string{"bob gc [--dry-run]"}, "collect garbage in bob's store", []flag{{name: "dry-run"}}, notYet},
		{"doctor", []string{"bob doctor [--repair]"}, "check and repair bob's setup",
			[]flag{{name: "repair"}, {name: "print-nix-env"}}, runDoctor},
		{"version", []string{"bob version [--json]"}, "print version information", nil, runVersion},
	}
}

// env is everything a command needs.
type env struct {
	ctx    context.Context
	out    *out
	a      *args
	cmd    command
	cwd    string // the working directory after -C
	getenv func(string) string
}

// Main runs the bob command line with args (without the program name) and returns the exit
// status.
func Main(args []string, stdout, stderr io.Writer) int {
	args = commandFirst(args)
	if len(args) == 0 || isHelp(args[0]) {
		printHelp(stdout)
		return 0
	}
	name := args[0]
	if name == "help" {
		if len(args) == 1 {
			printHelp(stdout)
			return 0
		}
		name = args[1]
		args = []string{name, "--help"}
	}
	if name == "__complete" { // hidden: the target of the completion scripts (R-CLI-004)
		if cwd, err := os.Getwd(); err == nil {
			runComplete(args[1:], cwd, func(s string) { _, _ = io.WriteString(stdout, s) })
		}
		return 0
	}
	cmd, ok := lookup(name)
	o := newOut(stdout, stderr, args)
	if !ok {
		hint := "run bob help"
		if s := suggest(name); s != "" {
			hint = "did you mean " + s + "?"
		}
		o.diagnose(diag.New(1, "kind", "command", "name", name), hint)
		return o.finish(2, translate.Counts{}, nil)
	}
	o.command = cmd.name
	a, err := parse(args[1:], cmd.flags)
	if err != nil {
		return o.fail(err)
	}
	if a.bool("help") {
		for _, s := range cmd.synopsis {
			fmt.Fprintf(stdout, "usage: %s\n", s)
		}
		return 0
	}
	o.configure(a)
	if m, ok := hostArchitecture(); !ok {
		// R-PLAT-001: bob runs on linux-amd64 and linux-arm64 only (uname -m, so that a 32-bit
		// personality is refused too).
		return o.fail(diag.New(8, "arch", m))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	e := &env{ctx: ctx, out: o, a: a, cmd: cmd, getenv: os.Getenv}
	cwd, err := os.Getwd()
	if err != nil {
		return o.fail(diag.New(7002, "reason", "the working directory cannot be read"))
	}
	if dir := a.str("C"); dir != "" {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return o.fail(diag.New(4, "flag", "-C", "what", "an existing directory"))
		}
		cwd = dir
	}
	e.cwd = cwd
	exit := cmd.run(e)
	if ctx.Err() != nil && exit != 0 && !o.finished {
		o.diagnose(diag.New(8001), "")
		return o.finish(130, translate.Counts{}, nil)
	}
	return exit
}

// newOut returns the output of a run before its flags are known (usage errors).
func newOut(stdout, stderr io.Writer, args []string) *out {
	o := &out{w: stdout, errw: stderr, start: time.Now(), now: time.Now, command: "", args: []string{}}
	if len(args) > 0 {
		o.command = args[0]
		o.args = append([]string{}, args[1:]...)
	}
	// --json anywhere before "--" decides the mode, even for a usage error.
	for _, w := range args {
		if w == "--" {
			break
		}
		if w == "--json" || w == "--json=true" {
			o.json = true
		}
	}
	o.style = translate.StyleFor(isTerminal(stdout), os.Getenv, false)
	return o
}

// configure applies the output flags.
func (o *out) configure(a *args) {
	o.json = a.bool("json")
	o.jsonLog = o.json && a.bool("json-log")
	o.quiet = a.bool("q")
	switch {
	case a.bool("vv"):
		o.verbose = 2
	case a.bool("v"):
		o.verbose = 1
	}
	o.isTTY = isTerminal(o.w)
	getenv := os.Getenv
	switch a.str("color") {
	case "always":
		getenv = func(k string) string {
			if k == "BOB_COLOR" {
				return "always"
			}
			return os.Getenv(k)
		}
	case "never":
		getenv = func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return os.Getenv(k)
		}
	}
	o.style = translate.StyleFor(o.isTTY, getenv, o.verbose > 0)
}

// notYet is the answer of a command no task has implemented yet (bob gc: plan/execution-plan.md
// has no task for it; raised by I-B47).
func notYet(e *env) int {
	return e.out.fail(diag.NotImplemented("bob " + e.cmd.name))
}

func lookup(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func isHelp(a string) bool { return a == "--help" }

// commandFirst moves the command word to the front: global flags may come before it
// (`bob -C dir build`, `bob --color=always build`).
func commandFirst(args []string) []string {
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "--" {
			return args
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			if i == 0 {
				return args
			}
			out := append([]string{w}, args[:i]...)
			return append(out, args[i+1:]...)
		}
		switch w {
		case "-C", "-j", "--color", "--server":
			i++ // the flag's value
		}
	}
	return args
}

func printHelp(w io.Writer) {
	var b strings.Builder
	b.WriteString("usage: bob [global flags] <command> [subcommand] [flags] [args] [-- passthrough args]\n\ncommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	b.WriteString("\nrun 'bob <command> --help' for a command's flags\n")
	_, _ = io.WriteString(w, b.String())
}

// suggest returns the command closest to name within edit distance 2 (SPEC R-CLI-001), or "".
func suggest(name string) string {
	best, bestD := "", 3
	for _, c := range commands() {
		if d := editDistance(name, c.name); d < bestD {
			best, bestD = c.name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance of a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// diagErrors turns any error into the diagnostics it carries. An error that is not a
// diagnostic is a bob bug (internal class); its text is kept for -v.
func diagErrors(err error) []*diag.Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return []*diag.Error{diag.New(8001)}
	}
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		var out []*diag.Error
		for _, e := range multi.Unwrap() {
			var d *diag.Error
			if errors.As(e, &d) {
				out = append(out, d)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var d *diag.Error
	if errors.As(err, &d) {
		return []*diag.Error{d}
	}
	reason := err.Error()
	raw := []string{reason}
	if strings.Contains(reason, "/nix/store/") || strings.Contains(reason, "\n") {
		reason = "an internal step failed (run with -v for details)"
	}
	d = diag.New(9005, "reason", reason)
	d.Raw = raw
	return []*diag.Error{d}
}

// hostArchitecture returns uname -m and whether bob supports it (x86_64, aarch64).
func hostArchitecture() (string, bool) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", true // cannot tell; the store setup fails later if it matters
	}
	b := make([]byte, 0, len(u.Machine))
	for _, c := range u.Machine {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	m := string(b)
	return m, m == "x86_64" || m == "aarch64"
}
