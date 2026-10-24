package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/pkg/engine/store"
)

// runExec is `bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]` (SPEC
// §02.7.3, R-DEV-008): the tool runs inside bob's namespace with the dev shell's inside
// variables; its streams, working directory, exit status and signals are its own. Shims call it.
func runExec(e *env) int {
	if !e.a.dash || len(e.a.pass) == 0 {
		return e.out.fail(diag.New(4, "flag", "bob exec", "what", "-- <tool> [args]"))
	}
	if e.a.bool("lsp") {
		e.out.json = false // rule 7: in --lsp mode stdout belongs to the protocol
	}
	tool := e.a.pass[0]
	root := e.a.str("project")
	if root == "" {
		r, err := e.root(false)
		if err != nil {
			return e.out.fail(err)
		}
		root = r
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(e.cwd, root)
	}
	shell := e.a.str("shell")
	envPath := devenv.Path(root, shell)
	noEnv := diag.New(7006, "shim", filepath.Join(devenv.ShimDir(root), filepath.Base(tool)), "root", root)
	if _, err := os.Stat(envPath); err != nil {
		return e.out.fail(noEnv)
	}
	home, err := store.HomeDir("")
	if err != nil {
		return e.out.fail(err)
	}
	f, _, err := devenv.Read(envPath)
	if err != nil {
		return e.out.fail(noEnv)
	}
	// Store paths as this process sees them: itself inside bob's namespace (bob dev), below
	// the state home outside.
	inside := store.Inside(home)
	read := func(p string) string {
		if !inside && strings.HasPrefix(p, "/nix/store/") {
			return filepath.Join(home, p)
		}
		return p
	}
	if err := ensureClosure(e, root, f, read); err != nil {
		return e.out.fail(err)
	}
	env := execEnv(os.Environ(), f, root, tool)
	path, err := lookTool(f, tool, env, read)
	if err != nil {
		return e.out.fail(err)
	}
	argv := append([]string{path}, e.a.pass[1:]...) // argv[0] = the program, as the S1 shim exec did
	if e.a.bool("lsp") {
		e.out.finished = true // the exit status is the server's, also after a signal
		return runLSP(e.ctx, home, path, argv, env, filepath.Join(home, "nix", "store"))
	}
	if inside { // the tool replaces bob
		err := syscall.Exec(path, argv, env)
		d := diag.New(5, "label", tool)
		d.Raw = []string{err.Error()}
		return e.out.fail(d)
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	return runIn(e, home, cmd, tool)
}

// runIn runs cmd in bob's namespace in the foreground: signals sent to bob are passed on, and
// bob exits with the program's status.
func runIn(e *env, home string, cmd *exec.Cmd, tool string) int {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := store.StartIn(home, cmd); err != nil {
		var d *diag.Error
		if errors.As(err, &d) {
			return e.out.fail(err)
		}
		d = diag.New(5, "label", tool)
		d.Raw = []string{err.Error()}
		return e.out.fail(d)
	}
	e.out.finished = true // the exit status is the program's, also after a signal
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case err := <-waited:
			return exitStatus(err)
		}
	}
}

// ensureClosure realises the dev shell's packages when a path is missing (a garbage-collected
// or never realised store): the shim works on the first use after a fresh clone, also with a
// store that does not hold the derivations (their outputs are then substituted).
func ensureClosure(e *env, root string, f *devenv.File, read func(string) string) error {
	var drvs []string
	for _, p := range f.Closure {
		if _, err := os.Lstat(read(p)); err == nil {
			continue
		}
		d, ok := f.Drvs[p]
		if !ok {
			return diag.New(7006, "shim", devenv.ShimDir(root), "root", root)
		}
		if _, err := os.Lstat(read(d)); err != nil {
			// Another store than the one bob env wrote the derivations to (a new bob home):
			// realising the output path substitutes it from the binary caches.
			d = p
		}
		drvs = append(drvs, d)
	}
	if len(drvs) == 0 {
		return nil
	}
	st, err := store.Open(e.ctx, store.Config{})
	if err != nil {
		return err
	}
	sort.Strings(drvs)
	return realiseDrvs(e, st, root, f.Shell, drvs)
}

// execEnv is the environment of a tool inside: the caller's variables, the dev shell's inside
// variables (PATH = the dev shell's bin directories, then the caller's PATH without the shims),
// then the tool's own.
func execEnv(base []string, f *devenv.File, root, tool string) []string {
	m := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = v
	}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}
	shims := devenv.ShimDir(root)
	var keep []string
	for _, p := range strings.Split(m["PATH"], ":") {
		if p != "" && p != shims {
			keep = append(keep, p)
		}
	}
	for k, v := range f.Inside {
		set(k, v)
	}
	path := f.Inside["PATH"]
	if len(keep) > 0 {
		if path != "" {
			path += ":"
		}
		path += strings.Join(keep, ":")
	}
	set("PATH", path)
	for k, v := range f.Tools[filepath.Base(tool)].Env {
		set(k, v)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+m[k])
	}
	return out
}

// lookTool finds the program of tool: a path as given, a tool of the dev shell, or a program
// on the inside PATH.
func lookTool(f *devenv.File, tool string, env []string, read func(string) string) (string, error) {
	if strings.Contains(tool, "/") {
		return tool, nil
	}
	if t, ok := f.Tools[tool]; ok {
		return t.Path, nil
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, tool)
		if fi, err := os.Stat(read(p)); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	names := make([]string, 0, len(f.Tools))
	for n := range f.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	best := ""
	for _, n := range names {
		if editDistance(tool, n) <= 2 {
			best = n
			break
		}
	}
	if best != "" {
		return "", diag.New(1, "kind", "tool", "name", tool, "suggest", best)
	}
	return "", diag.New(1, "kind", "tool", "name", tool)
}

// runLSP starts a language server as this process' child and pumps both directions through
// the path-rewriting proxy (SPEC §02.7.4): store paths become host paths for the editor and back.
// stderr passes through. Signals are forwarded; the exit status is the server's.
func runLSP(ctx context.Context, home, path string, argv, env []string, hostStore string) int {
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, SysProcAttr: &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bob: %v\n", err)
		return 70
	}
	// The server's stdout is read to its end after it exits, so no answer is lost.
	out, outW, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bob: %v\n", err)
		return 70
	}
	defer out.Close()
	cmd.Stdout = outW
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	err = store.StartIn(home, cmd)
	outW.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bob: %s cannot be started: %v\n", argv[0], err)
		return 127
	}
	pctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	pump := func(r io.Reader, w io.Writer, from, to string, done func()) {
		defer done()
		defer func() { _ = recover() }() // a proxy bug ends the session, never crashes silently
		if err := devenv.Pump(pctx, r, w, from, to); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "bob: lsp proxy: %v\n", err)
		}
	}
	answered := make(chan struct{})
	go pump(os.Stdin, in, hostStore, "/nix/store", func() { _ = in.Close() })
	go pump(out, os.Stdout, "/nix/store", hostStore, func() { close(answered) })
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var werr error
	for done := false; !done; {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case werr = <-waited:
			done = true
		}
	}
	select { // the rest of its answers (a child that keeps the pipe open is not waited for)
	case <-answered:
	case <-time.After(5 * time.Second):
	}
	cancel()
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 0
}

// identity reads store paths as they are (inside bob's namespace).
func identity(p string) string { return p }
