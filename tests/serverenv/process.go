package serverenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Process is a harness-started server process in its own process group. It
// gets SIGKILL when the test binary dies, so nothing is left running.
type Process struct {
	Cmd  *exec.Cmd
	Log  string // combined stdout/stderr
	done chan struct{}
	err  error
	logf *os.File
}

// StartProcess starts cmd with stdout and stderr appended to logPath. cmd
// should not come from exec.CommandContext: servers outlive the start-up
// context and end with Stop.
func StartProcess(cmd *exec.Cmd, logPath string) (*Process, error) {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("serverenv: %w", err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, fmt.Errorf("serverenv: start %s: %w: %w", cmd.Path, err, ErrHarness)
	}
	p := &Process{Cmd: cmd, Log: logPath, done: make(chan struct{}), logf: logf}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Pid is the process id (and process group id).
func (p *Process) Pid() int { return p.Cmd.Process.Pid }

// Exited is closed when the process has exited.
func (p *Process) Exited() <-chan struct{} { return p.done }

// Err is the wait error; valid after Exited is closed.
func (p *Process) Err() error { return p.err }

// Stop sends SIGTERM to the process group, SIGKILL after grace, and waits.
// It is safe to call more than once.
func (p *Process) Stop(grace time.Duration) error {
	select {
	case <-p.done:
	default:
		// ESRCH means the group is already gone.
		_ = syscall.Kill(-p.Pid(), syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(grace):
			_ = syscall.Kill(-p.Pid(), syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				return fmt.Errorf("serverenv: pid %d did not exit after SIGKILL", p.Pid())
			}
		}
	}
	// Children that left the group leader behind die with the group too.
	_ = syscall.Kill(-p.Pid(), syscall.SIGKILL)
	if err := p.logf.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("serverenv: %w", err)
	}
	return nil
}
