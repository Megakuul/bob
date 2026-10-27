package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/store"
)

// runDev is `bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]` (SPEC
// §02.7.7, R-DEV-001, R-DEV-002).
func runDev(e *env) int {
	// --servers-only --no-servers is the session without shell and servers: the tree is
	// prepared and kept up to date until bob is stopped (editors and tools outside the shell).
	if e.a.bool("servers-only") && e.a.dash {
		return e.out.fail(diag.New(4, "flag", "--servers-only", "what", "no command"))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	start := time.Now()
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	// Codegen is brought up to date from the start, while the environment is prepared (§03.6.6:
	// on entry and on every change of a codegen input).
	s := &session{e: e, root: root}
	stopGen := s.watchCodegen(st)
	defer stopGen()
	shell := e.a.str("shell")
	f, _, err := e.loadDev(st, root, shell)
	if err != nil {
		return e.out.fail(err)
	}
	if err := ensureClosure(e, root, f, identity); err != nil {
		return e.out.fail(err)
	}
	if !e.out.quiet {
		fmt.Fprintf(e.out.errw, "bob: dev shell %s%s · %.1fs\n", f.Shell, summary(f), time.Since(start).Seconds())
	}
	env := append(execEnv(os.Environ(), f, root, ""), devenv.VarDev+"=1", devenv.VarProject+"="+root, "BOB_DEV_SHELL="+f.Shell)
	// Editors started in the shell find a gopls that sees bob.go (A565); a failure is not fatal.
	if dir, err := writeToolWrappers(f, root); err != nil {
		fmt.Fprintf(e.out.errw, "bob: dev tool wrappers: %v\n", err)
	} else if dir != "" {
		env = withPathFirst(env, dir)
	}

	// Hooks run in order before anything else; a failing one stops bob dev with exit 1.
	for _, h := range f.Hooks {
		c := exec.CommandContext(e.ctx, "/bin/sh", "-c", h)
		c.Dir, c.Env = root, env
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := startDev(c)
		if err == nil {
			err = c.Wait()
		}
		if err != nil {
			status := "failed"
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				status = fmt.Sprintf("exited with status %d", ee.ExitCode())
			}
			fmt.Fprintf(e.out.errw, "bob: dev shell hook %q %s\n", h, status)
			return 1
		}
	}

	s.env, s.f = env, f
	defer s.stopAll()
	if !e.a.bool("no-servers") {
		var fg io.Writer
		if e.a.bool("servers-only") {
			fg = &lockedWriter{w: os.Stdout}
		}
		if err := s.startServers(fg); err != nil {
			return e.out.fail(err)
		}
	}
	stopWatch := s.watch(st, shell)
	defer stopWatch()
	if e.a.bool("servers-only") {
		return s.serve()
	}
	argv := e.a.pass
	if len(argv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh}
	}
	return s.run(argv)
}

// loadDev returns the env file of the requested shell: as it is when nothing it depends on
// changed since it was written (the hook's stamps), else computed again.
func (e *env) loadDev(st *store.Store, root, shell string) (*devenv.File, []byte, error) {
	p := devenv.Path(root, shell)
	if f, b, err := devenv.Read(p); err == nil && f.Root == root &&
		devenv.ReadStamps(devenv.StampPath(st.Home(), root, stampShell(shell))).Fresh(bobVersion(), devenv.Hash(b)) {
		return f, b, nil
	}
	f, b, _, err := e.devEnv(st, root, shell, true)
	return f, b, err
}

// stampShell is the stamp key of a requested shell ("" and "default" share env.json).
func stampShell(shell string) string {
	if shell == "" {
		return "default"
	}
	return shell
}

// summary is " (go 1.26.8, gopls 0.22.0)" for the first line of bob dev.
func summary(f *devenv.File) string {
	var parts []string
	for _, p := range f.Packages {
		if p.Version != "" {
			parts = append(parts, p.Name+" "+p.Version)
		} else {
			parts = append(parts, p.Name)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// session is one bob dev run: its servers and the watcher.
type session struct {
	e    *env
	root string
	env  []string
	f    *devenv.File

	mu      sync.Mutex
	servers []*devServer
	changed bool // the "dev environment changed" line was printed
	fg      io.Writer
}

func (s *session) startServers(fg io.Writer) error {
	s.fg = fg
	for _, sv := range s.f.Servers {
		if err := s.startServer(sv); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) startServer(sv devenv.Server) error {
	d, err := startServer(s.root, s.env, sv, s.fg, func(d *devServer) {
		if u := d.url(); u != "" {
			fmt.Fprintf(s.e.out.errw, "bob: %s ready at %s · log %s\n", d.spec.Name, u, d.logRel())
		} else {
			fmt.Fprintf(s.e.out.errw, "bob: %s ready · log %s\n", d.spec.Name, d.logRel())
		}
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.servers = append(s.servers, d)
	s.mu.Unlock()
	return nil
}

// stopAll stops every server (SIGTERM, then SIGKILL after 5 s) and reports it.
func (s *session) stopAll() {
	s.mu.Lock()
	servers := s.servers
	s.servers = nil
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, d := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			d.stop()
		}()
	}
	wg.Wait()
	for _, d := range servers {
		if !s.e.out.quiet {
			fmt.Fprintf(s.e.out.errw, "bob: stopped %s\n", d.spec.Name)
		}
	}
}

// reportExits warns once about every server that ended on its own (BOB-E-2012); it is not
// restarted.
func (s *session) reportExits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keep []*devServer
	for _, d := range s.servers {
		if gone, status := d.exited(); gone {
			s.e.out.diagnose(diag.Warn(2012, "name", d.spec.Name, "status", status, "log", d.logRel()), "")
			continue
		}
		keep = append(keep, d)
	}
	s.servers = keep
	return len(keep)
}

// run runs the shell or command in the foreground and returns its exit status. ^C goes to it
// from the terminal; SIGTERM and SIGHUP sent to bob are passed on.
func (s *session) run(argv []string) int {
	// The program owns the terminal now (a shell, nvim in it): the watcher's re-evaluations
	// must not draw a status line over it.
	if s.e.out.liveStop != nil {
		s.e.out.liveStop()
	}
	c := devCommand(s.f, argv, s.env)
	c.Env = s.env
	c.Dir = s.e.cwd
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := startDev(c); err != nil {
		return s.e.out.fail(devStartError(argv[0], err))
	}
	s.e.out.finished = true // bob dev's status is the command's
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case sig := <-sigs:
			_ = c.Process.Signal(sig)
		case <-tick.C:
			s.reportExits()
		case err := <-waited:
			return exitStatus(err)
		}
	}
}

// devCommand is the command for argv in the dev environment: the program is one of the
// shell's tools or found on the dev environment's PATH, else on bob's own PATH. exec.Command
// is not used: it searches only bob's PATH and its lookup error stays in Cmd.Err, which made
// Start fail for a toolchain command (`bob dev -- go version`: BOB-E-0005, A454).
func devCommand(f *devenv.File, argv, env []string) *exec.Cmd {
	c := &exec.Cmd{Path: argv[0], Args: argv}
	if p, err := lookTool(f, argv[0], env, identity); err == nil {
		c.Path = p
	} else if p, err := exec.LookPath(argv[0]); err == nil {
		c.Path = p
	}
	return c
}

// serve is --servers-only: the servers in the foreground until a signal or until all ended.
// With --no-servers it only keeps the tree up to date until a signal.
func (s *session) serve() int {
	if s.e.a.bool("no-servers") {
		<-s.e.ctx.Done()
		return 130
	}
	if len(s.f.Servers) == 0 {
		fmt.Fprintf(s.e.out.errw, "bob: dev shell %s has no dev servers\n", s.f.Shell)
		return 0
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.e.ctx.Done():
			return 130
		case <-tick.C:
			if s.reportExits() == 0 {
				return 1
			}
		}
	}
}

// watch re-checks the dev environment while the session runs (§02.7.7 step 7): when bob.go,
// bob.lock or another eval-key input changed, the environment is computed again; servers
// whose definition changed are restarted, and a changed inside environment is reported once.
func (s *session) watch(st *store.Store, shell string) (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() { _ = recover() }()
		key := devenv.StampPath(st.Home(), s.root, stampShell(shell))
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-s.e.ctx.Done():
				return
			case <-t.C:
			}
			_, b, err := devenv.Read(devenv.Path(s.root, shell))
			if err == nil && devenv.ReadStamps(key).Fresh(bobVersion(), devenv.Hash(b)) {
				continue
			}
			// Quietly: a broken bob.go while editing is normal; the next save tries again.
			quiet := *s.e
			o := *s.e.out
			o.errw, o.json = io.Discard, false
			quiet.out = &o
			nf, _, _, err := quiet.devEnv(st, s.root, shell, true)
			if err != nil {
				continue
			}
			s.apply(nf)
		}
	}()
	return func() { close(quit); <-finished }
}

// codegenPoll is how often bob dev stats the codegen inputs it knows (§03.6.6: on every change
// of a codegen input; the budget from a .proto save to the tree is 2.5 s, §03.8); codegenFull is
// how often it evaluates again anyway (new files, bob.go changes).
const (
	codegenPoll = 250 * time.Millisecond
	codegenFull = 2 * time.Second
)

// watchCodegen regenerates codegen whose inputs changed while the session runs (§03.6.6, R-FW-067):
// the owned directories that bob status would show as stale or missing are generated again
// with bob gen's protocol. Conflicts and failures are printed and watching continues; the same
// inputs are not tried twice, so a broken .proto is reported once per save.
func (s *session) watchCodegen(st *store.Store) (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() { _ = recover() }()
		tried := map[string]string{} // label → inputs digest last generated (or attempted)
		var watch []string
		snap, last := "", time.Time{}
		t := time.NewTicker(codegenPoll)
		defer t.Stop()
		for {
			if snap == "" || statSnapshot(s.root, watch) != snap || time.Since(last) >= codegenFull {
				last = time.Now()
				if cs, err := build.StaleCodegen(s.e.ctx, build.Options{Root: s.root, Store: st}); err == nil {
					watch = cs.Watch
					snap = statSnapshot(s.root, watch)
					var labels []string
					for l, d := range cs.Stale {
						if tried[l] != d {
							tried[l] = d
							labels = append(labels, l)
						}
					}
					if len(labels) > 0 {
						s.regenerate(st, labels)
					}
				}
			}
			select {
			case <-quit:
				return
			case <-s.e.ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(quit); <-finished }
}

// statSnapshot is the size and mtime of each repo-relative file ("-" when missing), so that a
// save shows up without reading or hashing anything.
func statSnapshot(root string, files []string) string {
	var b strings.Builder
	b.WriteString("v1")
	for _, f := range files {
		if fi, err := os.Stat(filepath.Join(root, f)); err == nil {
			fmt.Fprintf(&b, "\x00%s %d %d", f, fi.Size(), fi.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "\x00%s -", f)
		}
	}
	return b.String()
}

// regenerate runs bob gen for labels inside the session.
func (s *session) regenerate(st *store.Store, labels []string) {
	sort.Strings(labels)
	// Only the working-tree outputs: native files do not depend on codegen inputs (bob.go
	// changes reach them through the dev environment watch).
	res, err := build.Run(s.e.ctx, build.Options{Command: build.CmdGen, Root: s.root, Store: st, Patterns: labels})
	errs := diagErrors(err)
	if err == nil && res.ExitCode != 0 {
		errs = res.Report.Errors
	}
	if len(errs) > 0 {
		if s.e.ctx.Err() != nil {
			return
		}
		for _, d := range errs {
			w := *d
			w.Severity = diag.SeverityWarning
			s.e.out.diagnose(&w, "")
		}
		return
	}
	if !s.e.out.quiet {
		fmt.Fprintf(s.e.out.errw, "bob: generated %s\n", strings.Join(labels, ", "))
	}
}

// apply takes a recomputed environment: changed servers restart, a changed inside
// environment is reported once.
func (s *session) apply(nf *devenv.File) {
	s.mu.Lock()
	old := s.f
	s.f = nf
	report := !s.changed && !maps.Equal(old.Inside, nf.Inside)
	if report {
		s.changed = true
	}
	running := map[string]*devServer{}
	for _, d := range s.servers {
		running[d.spec.Name] = d
	}
	s.mu.Unlock()
	if report {
		fmt.Fprintln(s.e.out.errw, "bob: dev environment changed; exit and run bob dev again to use it")
	}
	if s.e.a.bool("no-servers") {
		return
	}
	for _, sv := range nf.Servers {
		d, ok := running[sv.Name]
		if ok && sameServer(d.spec, sv) {
			continue
		}
		if ok {
			d.stop()
			s.mu.Lock()
			for i, x := range s.servers {
				if x == d {
					s.servers = append(s.servers[:i], s.servers[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
		}
		if err := s.startServer(sv); err != nil {
			for _, d := range diagErrors(err) {
				s.e.out.diagnose(diag.Warn(d.Code, flatArgs(d.Args)...), "")
			}
		}
	}
}

func sameServer(a, b devenv.Server) bool {
	return a.Name == b.Name && strings.Join(a.Cmd, "\x00") == strings.Join(b.Cmd, "\x00") && a.Cwd == b.Cwd &&
		a.Port == b.Port && maps.Equal(a.Env, b.Env)
}

func flatArgs(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}

// exitStatus is the exit status of a finished command (128 + signal when it was killed).
func exitStatus(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	if err != nil {
		return 127
	}
	return 0
}

// lockedWriter serialises the prefixed lines of several servers.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
