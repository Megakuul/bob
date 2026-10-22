package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
	"github.com/megakuul/bob/pkg/engine/store"
)

// runDev is `bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]` (SPEC
// §02.7.7, R-DEV-001, R-DEV-002).
func runDev(e *env) int {
	if e.a.bool("servers-only") && (e.a.bool("no-servers") || e.a.dash) {
		return e.out.fail(diag.New(4, "flag", "--servers-only", "what", "no --no-servers and no command"))
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

	// Hooks run in order before anything else; a failing one stops bob dev with exit 1.
	for _, h := range f.Hooks {
		c := exec.CommandContext(e.ctx, "/bin/sh", "-c", h)
		c.Dir, c.Env = root, env
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			status := "failed"
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				status = fmt.Sprintf("exited with status %d", ee.ExitCode())
			}
			fmt.Fprintf(e.out.errw, "bob: dev shell hook %q %s\n", h, status)
			return 1
		}
	}

	s := &session{e: e, root: root, env: env, f: f}
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
	c := exec.Command(argv[0], argv[1:]...)
	if p, err := lookTool(s.f, argv[0], s.env, identity); err == nil {
		c.Path = p
	}
	c.Env = s.env
	c.Dir = s.e.cwd
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := c.Start(); err != nil {
		d := diag.New(5, "label", argv[0])
		d.Raw = []string{err.Error()}
		return s.e.out.fail(d)
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

// serve is --servers-only: the servers in the foreground until a signal or until all ended.
func (s *session) serve() int {
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
