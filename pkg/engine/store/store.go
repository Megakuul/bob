package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Substituter is a binary cache bob passes to Nix.
type Substituter struct{ URL, PublicKey, NetrcMachine, NetrcPassword string }

// Config configures bob's private Nix store (SPEC R-NIX-024).
type Config struct {
	Home         string // state home; empty: $BOB_HOME, else ${XDG_DATA_HOME:-$HOME/.local/share}/bob
	Substituters []Substituter
}

// Store is the embedded rootless Nix of one bob command.
type Store struct {
	home   string
	bin    string // <home>/bin/nix-<h>/bin
	inside bool   // this process already runs in bob's namespace (Enter); else every command enters it
}

// Open extracts the embedded Nix on first use, enters the namespace and returns the store.
//
// It creates the state layout of R-NIX-024 and writes nix.conf, netrc and user.conf under the
// flock <home>/lock, extracts Nix (later runs only stat it), and, unless the process already
// runs inside bob's namespace (Enter), checks once that the namespace can be entered: a host
// that blocks unprivileged user namespaces gets BOB-E-7005 with the exact fix and no store.
func Open(ctx context.Context, c Config) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := HomeDir(c.Home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, accessError(err)
	}
	unlock, err := lockState(home)
	if err != nil {
		return nil, err
	}
	bin, err := func() (string, error) {
		defer unlock()
		if err := mkdirs(home); err != nil {
			return "", err
		}
		if err := writeConfig(home, c.Substituters); err != nil {
			return "", err
		}
		return extractNix(home)
	}()
	if err != nil {
		return nil, err
	}
	s := &Store{home: home, bin: bin, inside: inNamespace(home)}
	if !s.inside {
		if err := s.probe(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// probe enters the namespace once and leaves it.
func (s *Store) probe(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/proc/self/exe")
	cmd.Env = os.Environ()
	cmd.Stderr = io.Discard
	r, w, err := prepareNamespace(cmd, s.home, modeProbe)
	if err != nil {
		return err
	}
	if err := startNamespace(cmd, r, w); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("store: namespace probe: %w", err)
	}
	return nil
}

// Home is the state home in use.
func (s *Store) Home() string { return s.home }

// BinDir is the directory of the extracted Nix, <home>/bin/nix-<h>/bin.
func (s *Store) BinDir() string { return s.bin }

// NixEnv is the exact environment of every Nix process (R-NIX-024): NIX_CONF_DIR,
// NIX_USER_CONF_FILES, the four XDG_* below <home>/xdg, HOME, PATH (the extracted Nix only),
// TMPDIR and TERM when set. NIX_PATH and every other NIX_* variable are left out.
func (s *Store) NixEnv() []string {
	home := os.Getenv("HOME")
	if home == "" {
		home = s.home
	}
	env := []string{
		"NIX_CONF_DIR=" + filepath.Join(s.home, "etc", "nix"),
		"NIX_USER_CONF_FILES=" + filepath.Join(s.home, "etc", "nix", "user.conf"),
		"XDG_CONFIG_HOME=" + filepath.Join(s.home, "xdg", "config"),
		"XDG_CACHE_HOME=" + filepath.Join(s.home, "xdg", "cache"),
		"XDG_DATA_HOME=" + filepath.Join(s.home, "xdg", "data"),
		"XDG_STATE_HOME=" + filepath.Join(s.home, "xdg", "state"),
		"HOME=" + home,
		"PATH=" + s.bin,
		"TMPDIR=" + os.TempDir(),
	}
	if t, ok := os.LookupEnv("TERM"); ok {
		env = append(env, "TERM="+t)
	}
	return env
}

// Cmd is a program run inside bob's namespace: a command of the embedded Nix ("nix",
// "nix-store", …) or, given as an absolute path, any store binary (bob exec, bob dev).
type Cmd struct {
	Args   []string  // arguments after the program name
	Env    []string  // KEY=VALUE added to NixEnv; later entries win
	Dir    string    // working directory; "" = the current one
	Stdin  io.Reader // nil = no input
	Stdout io.Writer // nil = discarded
	Stderr io.Writer // nil = discarded

	s    *Store
	ctx  context.Context
	name string
	cmd  *exec.Cmd
}

// Command returns the command name with args, to be run inside bob's namespace.
func (s *Store) Command(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{Args: args, s: s, ctx: ctx, name: name}
}

// path is the program to execute.
func (c *Cmd) path() string {
	if strings.Contains(c.name, "/") {
		return c.name
	}
	return filepath.Join(c.s.bin, c.name)
}

// Start starts the command. Inside bob's namespace the program is executed directly; otherwise
// through the namespace child, and Start returns once the namespace is ready.
func (c *Cmd) Start() error {
	if c.cmd != nil {
		return errors.New("store: command already started")
	}
	env := append(c.s.NixEnv(), c.Env...)
	argv0 := filepath.Base(c.name)
	cmd := exec.CommandContext(c.ctx, c.path())
	cmd.Args = append([]string{argv0}, c.Args...)
	cmd.Env = dedupEnv(env)
	cmd.Dir = c.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	// Nix cleans up its builds and locks on SIGTERM; it gets 10 s before SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	c.cmd = cmd
	if c.s.inside {
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("store: start %s: %w", argv0, err)
		}
		return nil
	}
	path := cmd.Path
	r, w, err := prepareNamespace(cmd, c.s.home, append([]string{modeExec, path}, cmd.Args...)...)
	if err != nil {
		return err
	}
	return startNamespace(cmd, r, w)
}

// Wait waits for the command; a non-zero exit is an *exec.ExitError (wrapped).
func (c *Cmd) Wait() error {
	if c.cmd == nil {
		return errors.New("store: command not started")
	}
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("store: %s: %w", filepath.Base(c.name), err)
	}
	return nil
}

// Run starts the command and waits for it.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// dedupEnv keeps the last value of each variable, in first-appearance order.
func dedupEnv(env []string) []string {
	idx := map[string]int{}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}

// sqliteBusy reports whether a Nix failure is a busy/locked SQLite database (R-NIX-026).
func sqliteBusy(stderr string) bool {
	l := strings.ToLower(stderr)
	return strings.Contains(l, "database is locked") || strings.Contains(l, "database is busy") || strings.Contains(l, "sqlite_busy")
}

// runStore runs a nix-store/nix command with stdin from in (re-readable for retries) and returns
// its stdout. Busy SQLite errors are retried 3 times with 200 ms backoff before BOB-E-7004.
func (s *Store) runStore(ctx context.Context, in func() io.Reader, name string, args ...string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		var so, se bytes.Buffer
		c := s.Command(ctx, name, args...)
		if in != nil {
			c.Stdin = in()
		}
		c.Stdout, c.Stderr = &so, &se
		err := c.Run()
		if err == nil {
			return so.Bytes(), nil
		}
		var de *diag.Error
		if errors.As(err, &de) || ctx.Err() != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if sqliteBusy(se.String()) {
			if attempt < 3 {
				select {
				case <-time.After(200 * time.Millisecond):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			d := diag.New(7004)
			d.Raw = rawLines(se.String())
			return nil, d
		}
		d := diag.New(9005, "reason", name+" "+firstArg(args)+" failed")
		d.Raw = rawLines(se.String())
		return nil, fmt.Errorf("%w (%v)", d, err)
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// rawLines keeps at most the last 40 lines of a Nix stderr for -v output.
func rawLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return lines
}

// validityBatch bounds the arguments of one nix-store --check-validity (the kernel limits argv).
const validityBatch = 2000

// Invalid returns those of paths that are not valid in the store, in one batched
// `nix-store --check-validity --print-invalid`.
func (s *Store) Invalid(ctx context.Context, paths []string) ([]string, error) {
	var out []string
	for i := 0; i < len(paths); i += validityBatch {
		batch := paths[i:min(i+validityBatch, len(paths))]
		for _, p := range batch {
			if !isStorePath(p) {
				return nil, fmt.Errorf("store: %q is not a store path", p)
			}
		}
		so, err := s.runStore(ctx, nil, "nix-store", append([]string{"--check-validity", "--print-invalid"}, batch...)...)
		if err != nil {
			return nil, err
		}
		for l := range strings.SplitSeq(string(so), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
	}
	return out, nil
}

// isStorePath checks the shape /nix/store/<32 base-32 chars>-<name>.
func isStorePath(p string) bool {
	const prefix = "/nix/store/"
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	b := p[len(prefix):]
	if len(b) < 34 || b[32] != '-' || strings.Contains(b, "/") {
		return false
	}
	for _, c := range b[:32] {
		if !strings.ContainsRune(nixBase32Alphabet, c) {
			return false
		}
	}
	for _, c := range b[33:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+-._?=", c)) {
			return false
		}
	}
	return true
}

const nixBase32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

// Import imports items in one `nix-store --import` stream (export format v1, no signatures).
func (s *Store) Import(ctx context.Context, items []ExportItem) error {
	if len(items) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := WriteExport(&buf, items); err != nil {
		return err
	}
	stream := buf.Bytes()
	_, err := s.runStore(ctx, func() io.Reader { return bytes.NewReader(stream) }, "nix-store", "--import")
	return err
}
