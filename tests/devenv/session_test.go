package devenv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// session is an interactive shell on a pseudo-terminal, driven like a person would: commands go
// in as one sourced file per command, and the output is split at markers into what the command
// printed and what the prompt-time hook printed.
//
// The shell prints "@@P@@" as its prompt. A command file ends with an "@@E<n>@@" marker, so the
// text between the marker and the next prompt is what the precmd hook wrote (bash PROMPT_COMMAND,
// zsh precmd, fish fish_prompt event); text before the marker includes zsh's chpwd hook.
type session struct {
	t      testing.TB
	w      *world
	shell  string
	dir    string // scratch directory for command files
	master *os.File
	cmd    *exec.Cmd

	mu   sync.Mutex
	buf  bytes.Buffer
	pos  int // consumed prefix of buf
	done chan struct{}
	n    int
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[=>()][0-9A-B]?`)

// clean removes terminal control sequences and carriage returns.
func clean(s string) string {
	s = ansi.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

func openPTY(t testing.TB) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("harness error: open /dev/ptmx: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlock))); e != 0 { // TIOCSPTLCK
		t.Fatalf("harness error: unlock pty: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&n))); e != 0 { // TIOCGPTN
		t.Fatalf("harness error: pty number: %v", e)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("harness error: open pty slave: %v", err)
	}
	if os.Geteuid() == 0 { // the account must be able to open its own terminal
		_ = os.Chmod(slave.Name(), 0o666)
	}
	return master, slave
}

// shellSpec describes how to start and prepare one shell.
type shellSpec struct {
	name  string
	attr  string   // nixpkgs attribute of the pin
	args  []string // interactive, no rc files
	setup []string // lines typed before the hook is installed: prompt, no greeting
	hook  string   // the command that installs the hook (the SPEC rc line)
}

// shellSpecs are the three shells of R-DEV-003 (SPEC: bash 5, zsh 5.9, fish 3.7).
func shellSpecs(bob string) []shellSpec {
	return []shellSpec{
		{"bash", "bashInteractive", []string{"--norc", "--noprofile", "--noediting", "-i"},
			[]string{`PS1='@@P@@'`}, `eval "$("` + bob + `" hook bash)"`},
		{"zsh", "zsh", []string{"-f", "-i", "+Z"},
			[]string{`unsetopt prompt_sp`, `PROMPT='@@P@@'`, `RPROMPT=''`}, `eval "$("` + bob + `" hook zsh)"`},
		{"fish", "fish", []string{"--no-config", "-i"},
			[]string{`set -g fish_greeting`, `function fish_prompt; printf '@@P@@'; end`, `function fish_right_prompt; end`},
			`"` + bob + `" hook fish | source`},
	}
}

// startShell starts the shell as the account in dir and waits for its first prompt. extraEnv is
// added to the world's variables. The hook is not installed yet.
func startShell(t testing.TB, w *world, sp shellSpec, dir string, extraEnv ...string) *session {
	t.Helper()
	exe := pinned(t, sp.name, sp.attr, sp.name)
	scratch, err := os.MkdirTemp("", "bob-devenv-sh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if err := os.Chmod(scratch, 0o777); err != nil {
		t.Fatal(err)
	}
	master, slave := openPTY(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	cmd := exec.CommandContext(ctx, exe, sp.args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, w.env...), extraEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	w.acc.cred(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: start %s: %v", sp.name, err)
	}
	slave.Close()
	s := &session{t: t, w: w, shell: sp.name, dir: scratch, master: master, cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		b := make([]byte, 4096)
		for {
			n, err := master.Read(b)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(b[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		_ = cmd.Wait()
		master.Close()
		<-s.done
	})
	for _, l := range sp.setup {
		s.type_(l)
	}
	return s
}

func (s *session) type_(line string) {
	if _, err := io.WriteString(s.master, line+"\n"); err != nil {
		s.t.Fatalf("harness error: write to %s: %v", s.shell, err)
	}
}

// waitFor consumes output up to and including the first match of marker and returns the text
// before it.
func (s *session) waitFor(marker string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		rest := s.buf.Bytes()[s.pos:]
		if i := bytes.Index(rest, []byte(marker)); i >= 0 {
			before := string(rest[:i])
			s.pos += i + len(marker)
			s.mu.Unlock()
			return before, nil
		}
		tail := string(rest)
		s.mu.Unlock()
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s: no %q within %s; output so far: %q", s.shell, marker, timeout, clean(tail))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// prompt waits for the next prompt and returns what was printed before it (the hook's output
// after the previous command).
func (s *session) prompt() string {
	s.t.Helper()
	out, err := s.waitFor("@@P@@", 2*time.Minute)
	if err != nil {
		s.t.Fatal(err)
	}
	return clean(out)
}

// result of one command.
type cmdOut struct {
	During string // between the typed line and the end marker: the command's output (and chpwd hooks)
	After  string // between the end marker and the next prompt: precmd hooks
}

// run sources script (lines in the shell's own syntax) and returns its output and the hook output
// that followed. The echoed command line is removed from the output.
func (s *session) run(script string) cmdOut {
	s.t.Helper()
	s.n++
	file := filepath.Join(s.dir, fmt.Sprintf("c%d", s.n))
	end := fmt.Sprintf("printf '@@E%%s@@\\n' %d\n", s.n)
	if err := os.WriteFile(file, []byte(script+"\n"+end), 0o644); err != nil {
		s.t.Fatal(err)
	}
	typed := ". " + file
	s.type_(typed)
	during, err := s.waitFor(fmt.Sprintf("@@E%d@@", s.n), 2*time.Minute)
	if err != nil {
		s.t.Fatal(err)
	}
	after := s.prompt()
	return cmdOut{During: s.strip(during, typed), After: s.strip(after, typed)}
}

// strip removes the echo of the typed line and surrounding blanks.
func (s *session) strip(out, typed string) string {
	out = clean(out)
	// fish repaints the prompt and the half-typed line while it echoes the input, and marks
	// output without a final newline with a return symbol.
	for _, junk := range []string{typed, "@@P@@", "\u23ce"} {
		out = strings.ReplaceAll(out, junk, "")
	}
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "." {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// cd changes directory and returns all hook output of that prompt cycle (chpwd and precmd).
func (s *session) cd(dir string) string {
	s.t.Helper()
	o := s.run("cd " + quote(dir))
	return strings.TrimSpace(o.During + "\n" + o.After)
}

// environ returns the exported variables of the shell, byte exact (env -0).
func (s *session) environ() map[string]string {
	s.t.Helper()
	f := filepath.Join(s.dir, fmt.Sprintf("env%d", s.n+1))
	s.run("/usr/bin/env -0 > " + quote(f))
	b, err := os.ReadFile(f)
	if err != nil {
		s.t.Fatalf("harness error: %v", err)
	}
	m := map[string]string{}
	for _, kv := range strings.Split(string(b), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// capture runs a command line and returns its stdout.
func (s *session) capture(cmdline string) string {
	s.t.Helper()
	f := filepath.Join(s.dir, fmt.Sprintf("out%d", s.n+1))
	s.run(cmdline + " > " + quote(f) + " 2>&1")
	b, err := os.ReadFile(f)
	if err != nil {
		s.t.Fatalf("harness error: %v", err)
	}
	return strings.TrimRight(string(b), "\n")
}

// quote is POSIX single quoting (also valid in fish for these inputs).
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
