package cli

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/devenv"
)

// Dev servers of bob dev (SPEC §02.7.7 step 5, R-DEV-002): each runs in its own process group
// inside the namespace with the dev shell's environment; its output goes to
// .bob/dev/<name>.log (and, with --servers-only, to stdout with a "[name] " prefix).

// readyTimeout bounds how long bob waits for a server's readiness line.
const readyTimeout = 2 * time.Minute

// stopGrace is the time between SIGTERM and SIGKILL when a server stops (§02.7.7 step 8).
const stopGrace = 5 * time.Second

type devServer struct {
	spec devenv.Server
	root string
	cmd  *exec.Cmd
	log  *os.File
	done chan struct{} // closed when the process has exited
	ws   syscall.WaitStatus

	mu       sync.Mutex
	stopping bool
}

// startServer starts one dev server. fg receives its prefixed output (nil: log only); ready is
// called once when it is ready.
func startServer(root string, env []string, s devenv.Server, fg io.Writer, ready func(*devServer)) (*devServer, error) {
	if len(s.Cmd) == 0 || !validServerName(s.Name) {
		return nil, diag.New(9004, "label", s.Label, "reason", fmt.Sprintf("dev server %q has no valid name or command", s.Name))
	}
	dir := filepath.Join(root, ".bob", "dev")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, diag.New(7002, "reason", ".bob/dev cannot be written")
	}
	log, err := os.Create(filepath.Join(dir, s.Name+".log"))
	if err != nil {
		return nil, diag.New(7002, "reason", ".bob/dev cannot be written")
	}
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	prog := s.Cmd[0]
	if !strings.Contains(prog, "/") {
		if p, err := lookTool(&devenv.File{}, prog, env, identity); err == nil {
			prog = p
		}
	}
	cmd := exec.Command(prog, s.Cmd[1:]...)
	cmd.Args[0] = s.Cmd[0]
	cmd.Env = env
	cmd.Dir = filepath.Join(root, filepath.FromSlash(s.Cwd))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw, err := os.Pipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	d := &devServer{spec: s, root: root, cmd: cmd, log: log, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		log.Close()
		return nil, diag.New(2012, "name", s.Name, "status", "127 (it could not be started: "+err.Error()+")", "log", d.logRel())
	}
	pw.Close()
	var pattern *regexp.Regexp
	if s.Ready == "log" && s.Pattern != "" {
		pattern, _ = regexp.Compile(s.Pattern)
	}
	var once sync.Once
	signalReady := func() { once.Do(func() { ready(d) }) }
	copied := make(chan struct{})
	go func() { // output: log (+ prefixed foreground), readiness pattern
		defer close(copied)
		defer func() { _ = recover() }()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(log, line)
			if fg != nil {
				fmt.Fprintf(fg, "[%s] %s\n", s.Name, line)
			}
			if pattern != nil && pattern.MatchString(line) {
				signalReady()
			}
		}
		pr.Close()
	}()
	go func() {
		defer func() { _ = recover() }()
		_ = cmd.Wait()
		<-copied
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			d.ws = ws
		}
		log.Close()
		close(d.done)
	}()
	if s.Ready == "http" && s.Port > 0 {
		go func() {
			defer func() { _ = recover() }()
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port))
			for deadline := time.Now().Add(readyTimeout); time.Now().Before(deadline); {
				select {
				case <-d.done:
					return
				default:
				}
				if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
					c.Close()
					signalReady()
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
		}()
	} else if pattern == nil {
		signalReady() // nothing to wait for
	}
	return d, nil
}

func validServerName(n string) bool {
	return n != "" && len(n) <= 64 && strings.Trim(n, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

func (d *devServer) logRel() string { return ".bob/dev/" + d.spec.Name + ".log" }

// url is where the server listens (the readiness line).
func (d *devServer) url() string {
	host := d.spec.Host
	if host == "" {
		host = "localhost"
	}
	if d.spec.Port > 0 {
		return "http://" + net.JoinHostPort(host, strconv.Itoa(d.spec.Port))
	}
	return ""
}

// exited reports whether the server ended without being stopped by bob, and its status.
func (d *devServer) exited() (bool, string) {
	select {
	case <-d.done:
	default:
		return false, ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return false, ""
	}
	if d.ws.Signaled() {
		return true, "signal " + d.ws.Signal().String()
	}
	return true, strconv.Itoa(d.ws.ExitStatus())
}

// stop ends the server's process group: SIGTERM, then SIGKILL after stopGrace.
func (d *devServer) stop() {
	d.mu.Lock()
	d.stopping = true
	d.mu.Unlock()
	pgid := d.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-d.done
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL) // stragglers of the group
}
