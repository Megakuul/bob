package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// op is one parsed step of a run job.
type op struct {
	name string
	what string // argv[0] for exec, the main path otherwise (for the failure line)
	do   func() error
}

// runKind is kind "run" (bob.Run): every step is decoded and checked before the first runs, so
// an invalid job changes nothing.
func runKind(c *Ctx, raw json.RawMessage) error {
	var a bspec.RunArgs
	if err := decodeStrict(raw, &a); err != nil {
		return Invalidf("run args: %v", err)
	}
	ops := make([]op, 0, len(a.Steps))
	for i, s := range a.Steps {
		o, err := c.parseStep(s)
		if err != nil {
			return fmt.Errorf("step %d %s: %w", i+1, s.Op, err)
		}
		ops = append(ops, o)
	}
	for i, o := range ops {
		c.Phase(o.name)
		if err := o.do(); err != nil {
			return fmt.Errorf("step %d %s %s failed: %w", i+1, o.name, o.what, err)
		}
	}
	return nil
}

// parseStep decodes one step and checks its paths lexically.
func (c *Ctx) parseStep(s bspec.Step) (op, error) {
	o := op{name: s.Op}
	args := s.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	lexical := func(paths ...string) error {
		for _, p := range paths {
			if p == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
				return Invalidf("path %q is not an absolute path", p)
			}
			if _, ok := c.root(filepath.Clean(p)); !ok {
				return Invalidf("path %s is outside the outputs and the build directory", p)
			}
		}
		return nil
	}
	switch s.Op {
	case "mkdir":
		var a bspec.MkdirArgs
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		o.what, o.do = a.Path, func() error { return c.MkdirAll(a.Path) }
		return o, lexical(a.Path)
	case "write":
		var a bspec.WriteArgs
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		o.what, o.do = a.Path, func() error { return c.write(a) }
		return o, lexical(a.Path)
	case "copy":
		var a bspec.CopyArgs
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		if a.From == "" || strings.ContainsRune(a.From, 0) || !filepath.IsAbs(a.From) {
			return o, Invalidf("copy source %q is not an absolute path", a.From)
		}
		o.what, o.do = a.To, func() error { return c.copyAny(a.From, a.To) }
		return o, lexical(a.To)
	case "symlink":
		var a bspec.SymlinkArgs
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		if a.Target == "" || strings.ContainsRune(a.Target, 0) {
			return o, Invalidf("symlink target %q is empty or has a NUL byte", a.Target)
		}
		o.what, o.do = a.Link, func() error { return c.symlink(a) }
		return o, lexical(a.Link)
	case "exec":
		var a bspec.ExecArgs
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		if len(a.Argv) == 0 || a.Argv[0] == "" {
			return o, Invalidf("exec without argv")
		}
		for _, s := range append(append([]string{}, a.Argv...), a.Path...) {
			if strings.ContainsRune(s, 0) {
				return o, Invalidf("exec argument with a NUL byte")
			}
		}
		for k, v := range a.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
				return o, Invalidf("exec environment variable %q is invalid", k)
			}
		}
		o.what, o.do = a.Argv[0], func() error { return c.Exec(a) }
		if a.Cwd != "" {
			return o, lexical(a.Cwd)
		}
		return o, nil
	case "check-outputs":
		var a struct{}
		if err := decodeStrict(args, &a); err != nil {
			return o, Invalidf("%v", err)
		}
		o.what, o.do = "", func() error { return c.checkOutputs(true) }
		return o, nil
	}
	return o, Invalidf("unknown op %q", s.Op)
}

func (c *Ctx) write(a bspec.WriteArgs) error {
	p, err := c.Writable(a.Path)
	if err != nil {
		return err
	}
	if err := c.parents(p); err != nil {
		return err
	}
	mode := os.FileMode(a.Mode & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	// The builder (and fixup) must be able to read and rewrite what it wrote.
	mode |= 0o600
	return writeFile(p, mode, strings.NewReader(a.Content), int64(len(a.Content)))
}

func (c *Ctx) symlink(a bspec.SymlinkArgs) error {
	p, err := c.Writable(a.Link)
	if err != nil {
		return err
	}
	if err := c.parents(p); err != nil {
		return err
	}
	return os.Symlink(a.Target, p)
}

// Exec runs a program with exactly the environment of R-NIX-016: PATH (the declared
// directories), HOME, TMPDIR, NIX_BUILD_CORES, SOURCE_DATE_EPOCH, TZ, LC_ALL, the outputs and
// the step's env. Its stdout and stderr go to the build log.
func (c *Ctx) Exec(a bspec.ExecArgs) error {
	dir := c.BuildTop
	if a.Cwd != "" {
		cwd, err := c.Writable(a.Cwd)
		if err != nil {
			return err
		}
		if fi, err := os.Lstat(cwd); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return Invalidf("working directory %s is a symlink", a.Cwd)
		}
		dir = cwd
	}
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	prog, err := lookPath(a.Argv[0], a.Path)
	if err != nil {
		return err
	}
	env := c.baseEnv(a.Path, tmp)
	maps.Copy(env, a.Env)
	cmd := &exec.Cmd{Path: prog, Args: a.Argv, Env: envList(env), Dir: dir, Stdout: c.stdout, Stderr: c.stderr}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s", ee.ProcessState.String())
		}
		return err
	}
	return nil
}

// lookPath finds argv0 in the declared path (never in the builder's own environment).
func lookPath(argv0 string, path []string) (string, error) {
	if strings.Contains(argv0, "/") {
		return argv0, nil
	}
	for _, d := range path {
		p := filepath.Join(d, argv0)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w in the step's path %v", argv0, fs.ErrNotExist, path)
}

// copyTreeKind is kind "copy-tree": copy a tree, keeping symlinks and the executable bits.
func copyTreeKind(c *Ctx, raw json.RawMessage) error {
	var a bspec.CopyTreeArgs
	if err := decodeStrict(raw, &a); err != nil {
		return Invalidf("copy-tree args: %v", err)
	}
	c.Phase("copy-tree")
	return c.copyAny(a.From, a.To)
}
