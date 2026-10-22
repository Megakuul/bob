package bop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
)

// Kinds returns the C/C++ job kinds (SPEC §03.1.3); cmd/bob-builder adds them to
// builder.Core(). Their outputs go through the fixup (strip, RUNPATH, reference check).
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"cc-compile-link": {Run: compileLinkKind},
		"cc-external":     {Run: externalKind},
	}
}

func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return builder.Invalidf("cc job args: %v", err)
	}
	return nil
}

// runner runs the programs of one job; their output goes to the build log.
type runner struct {
	c    *builder.Ctx
	path []string
	env  []string
	log  io.Writer
	mu   sync.Mutex // serialises log writes of parallel compiles
}

func newRunner(c *builder.Ctx, path []string, extra map[string]string) (*runner, error) {
	tmp, err := c.TmpDir()
	if err != nil {
		return nil, err
	}
	e := map[string]string{
		"PATH": strings.Join(path, ":"), "HOME": tmp, "TMPDIR": tmp, "SOURCE_DATE_EPOCH": "1",
		"TZ": "UTC", "LC_ALL": "C", "TERM": "dumb", "NIX_BUILD_CORES": strconv.Itoa(cores()),
		"ZERO_AR_DATE": "1",
	}
	for k, v := range extra {
		e[k] = v
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, len(keys))
	for i, k := range keys {
		env[i] = k + "=" + e[k]
	}
	return &runner{c: c, path: path, env: env, log: os.Stderr}, nil
}

// run executes argv in dir; its output goes to the build log. Compiler diagnostics
// ("file:line:col: error: msg") also become structured diagnostics for bob.
func (r *runner) run(dir string, argv ...string) error {
	prog, err := lookPath(argv[0], r.path)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := &exec.Cmd{Path: prog, Args: argv, Env: r.env, Dir: dir, Stdout: &out, Stderr: &out}
	err = cmd.Run()
	r.mu.Lock()
	_, _ = r.log.Write(out.Bytes())
	if err != nil {
		r.diags(out.Bytes())
	}
	r.mu.Unlock()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s: %s", filepath.Base(argv[0]), ee.ProcessState.String())
		}
		return fmt.Errorf("%s: %w", filepath.Base(argv[0]), err)
	}
	return nil
}

var gccDiag = regexp.MustCompile(`^([^\s:][^:]*):([0-9]+):([0-9]+): (?:fatal )?error: (.*)$`)

// diags reports the compiler's error lines (at most 20) as structured diagnostics.
func (r *runner) diags(out []byte) {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		m := gccDiag.FindStringSubmatch(line)
		if m == nil || n == 20 {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		r.c.Diag("error", m[1], ln, col, m[4])
		n++
	}
}

func lookPath(name string, path []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, d := range path {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in the job's path %v", name, path)
}

// cores is the parallelism of a job: NIX_BUILD_CORES (at least 1).
func cores() int {
	n, err := strconv.Atoi(os.Getenv("NIX_BUILD_CORES"))
	if err != nil || n < 1 {
		n = 1
	}
	return n
}

// compileLinkKind is kind cc-compile-link.
func compileLinkKind(c *builder.Ctx, raw json.RawMessage) error {
	var a CompileLinkArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.Src) || len(a.Link.Argv) == 0 {
		return builder.Invalidf("cc-compile-link: src and a link command are required")
	}
	r, err := newRunner(c, a.Path, nil)
	if err != nil {
		return err
	}
	c.Phase("pkg-config")
	flags := make([]pcFlags, len(a.PkgConfig))
	for i, pc := range a.PkgConfig {
		if flags[i], err = resolve(pc); err != nil {
			return err
		}
	}
	expand := func(argv []string) ([]string, error) {
		var out []string
		for _, s := range argv {
			kind, idx, ok := strings.Cut(s, ":")
			if !strings.HasPrefix(s, "@") || !ok {
				out = append(out, s)
				continue
			}
			i, err := strconv.Atoi(idx)
			if err != nil || i < 0 || i >= len(flags) {
				return nil, builder.Invalidf("argument %q names no pkg-config entry", s)
			}
			switch kind {
			case "@cflags":
				out = append(out, flags[i].cflags...)
			case "@libs":
				out = append(out, flags[i].libs...)
			default:
				return nil, builder.Invalidf("argument %q is not @cflags:<i> or @libs:<i>", s)
			}
		}
		return out, nil
	}
	c.Phase("compile")
	cmds := make([][]string, len(a.Compile))
	for i, cmd := range a.Compile {
		if len(cmd.Argv) == 0 {
			return builder.Invalidf("compile command %d is empty", i)
		}
		if cmds[i], err = expand(cmd.Argv); err != nil {
			return err
		}
		if err := mkParent(c, cmd.Out); err != nil {
			return err
		}
	}
	if err := parallel(cores(), len(cmds), func(i int) error { return r.run(a.Src, cmds[i]...) }); err != nil {
		return err
	}
	c.Phase("link")
	link, err := expand(a.Link.Argv)
	if err != nil {
		return err
	}
	if err := mkParent(c, a.Link.Out); err != nil {
		return err
	}
	if err := r.run(a.Src, link...); err != nil {
		return err
	}
	for _, h := range a.Headers {
		if err := copyTree(c, h.From, h.To); err != nil {
			return err
		}
	}
	return nil
}

// mkParent creates the directory of a file the job writes (checked to be writable).
func mkParent(c *builder.Ctx, p string) error {
	if p == "" {
		return builder.Invalidf("command without an output")
	}
	w, err := c.Writable(p)
	if err != nil {
		return err
	}
	return c.MkdirAll(filepath.Dir(w))
}

// parallel runs f(0..n-1) on at most workers goroutines and returns the first error (all
// started calls finish first).
func parallel(workers, n int, f func(i int) error) error {
	workers = max(1, min(workers, n))
	next := make(chan int)
	errs := make([]error, n)
	call := func(i int) {
		defer func() {
			if v := recover(); v != nil {
				errs[i] = fmt.Errorf("internal error: %v", v)
			}
		}()
		errs[i] = f(i)
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				call(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
	return errors.Join(errs...)
}

// copyTree copies the regular files, directories and symlinks below from (a read-only store
// tree) to to; files keep their executable bit, everything becomes writable by the owner.
func copyTree(c *builder.Ctx, from, to string) error {
	dst, err := c.Writable(to)
	if err != nil {
		return err
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return c.MkdirAll(target)
		case info.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.Mode().IsRegular():
			mode := fs.FileMode(0o644)
			if info.Mode()&0o111 != 0 {
				mode = 0o755
			}
			return copyFile(p, target, mode)
		}
		return nil // sockets, devices: not part of a source tree
	})
}

func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// externalKind is kind cc-external: the project's own build system, inside the derivation.
func externalKind(c *builder.Ctx, raw json.RawMessage) error {
	var a ExternalArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if !filepath.IsAbs(a.Src) || !filepath.IsAbs(a.Out) || !filepath.IsAbs(a.Shell) {
		return builder.Invalidf("cc-external: src, out and shell must be absolute paths")
	}
	if a.Profile != "release" && a.Profile != "debug" {
		return builder.Invalidf("cc-external: profile %q is not release or debug", a.Profile)
	}
	env := map[string]string{"SHELL": a.Shell, "CONFIG_SHELL": a.Shell}
	for k, v := range a.Env {
		env[k] = v
	}
	r, err := newRunner(c, a.Path, env)
	if err != nil {
		return err
	}
	src := filepath.Join(c.BuildTop, "src")
	c.Phase("unpack")
	if err := copyTree(c, a.Src, src); err != nil {
		return err
	}
	if err := c.MkdirAll(a.Out); err != nil {
		return err
	}
	jobs := "-j" + strconv.Itoa(cores())
	build := filepath.Join(c.BuildTop, "build")
	steps, err := externalSteps(a, src, build, jobs)
	if err != nil {
		return err
	}
	for _, s := range steps {
		if s.phase != "" {
			c.Phase(s.phase)
		}
		if err := r.run(s.dir, s.argv...); err != nil {
			return err
		}
	}
	return nil
}

type step struct {
	phase string
	dir   string
	argv  []string
}

// externalSteps are the commands of one build system (SPEC §03.1.2).
func externalSteps(a ExternalArgs, src, build, jobs string) ([]step, error) {
	buildType := map[string]string{"release": "Release", "debug": "Debug"}[a.Profile]
	switch a.Build {
	case "make":
		argv := append([]string{"make", jobs, "SHELL=" + a.Shell, "PREFIX=" + a.Out, "install"}, a.Args...)
		return []step{{"build", src, argv}}, nil
	case "cmake":
		cfg := append([]string{"cmake", "-S", ".", "-B", build, "-G", "Ninja", "-DCMAKE_INSTALL_PREFIX=" + a.Out,
			"-DCMAKE_BUILD_TYPE=" + buildType, "-DCMAKE_INSTALL_LIBDIR=lib", "-DCMAKE_INSTALL_INCLUDEDIR=include"}, a.Args...)
		return []step{
			{"configure", src, cfg},
			{"build", src, []string{"cmake", "--build", build, "--parallel", strings.TrimPrefix(jobs, "-j")}},
			{"install", src, []string{"cmake", "--install", build}},
		}, nil
	case "meson":
		cfg := append([]string{"meson", "setup", build, "--prefix=" + a.Out, "--libdir=lib",
			"--buildtype=" + a.Profile}, a.Args...)
		return []step{
			{"configure", src, cfg},
			{"build", src, []string{"ninja", "-C", build, jobs}},
			{"install", src, []string{"ninja", "-C", build, "install"}},
		}, nil
	case "autotools":
		var steps []step
		if _, err := os.Stat(filepath.Join(src, "configure")); err != nil {
			steps = append(steps, step{"autoreconf", src, []string{a.Shell, "-c", "autoreconf -fi"}})
		}
		cfg := append([]string{a.Shell, "./configure", "--prefix=" + a.Out, "--libdir=" + a.Out + "/lib"}, a.Args...)
		return append(steps,
			step{"configure", src, cfg},
			step{"build", src, []string{"make", jobs, "SHELL=" + a.Shell}},
			step{"install", src, []string{"make", "SHELL=" + a.Shell, "install"}},
		), nil
	}
	return nil, builder.Invalidf("cc-external: build system %q is not cmake, autotools, meson or make", a.Build)
}

// sortedUnique returns s sorted without duplicates.
func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}
