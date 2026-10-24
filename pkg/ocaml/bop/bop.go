// Package bop implements the OCaml builder operations (job kinds opam-build, ml-compile and
// ml-link, SPEC §03.0.4, §03.5.3) that bob-builder runs inside OCaml derivations.
package bop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
)

// Kinds returns the OCaml job kinds; cmd/bob-builder adds them to builder.Core(). Their
// outputs go through the fixup (reference check against the build-only tools).
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"opam-build": {Run: opamBuildKind},
		"ml-compile": {Run: compileKind},
		"ml-link":    {Run: linkKind},
	}
}

func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return builder.Invalidf("ocaml job args: %v", err)
	}
	return nil
}

// runner runs the programs of one job; their output goes to the build log.
type runner struct {
	c    *builder.Ctx
	path []string
	env  []string
	log  io.Writer
	dir  string // repository directory of the sources, for diagnostics ("" = root)
	top  string // where copySources puts the repository sources
}

func newRunner(c *builder.Ctx, pathDirs []string, extra map[string]string) (*runner, error) {
	tmp, err := c.TmpDir()
	if err != nil {
		return nil, err
	}
	e := map[string]string{
		"PATH": strings.Join(pathDirs, ":"), "HOME": tmp, "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
		"SOURCE_DATE_EPOCH": "1", "TZ": "UTC", "LC_ALL": "C", "TERM": "dumb", "NIX_BUILD_CORES": strconv.Itoa(cores()),
		"ZERO_AR_DATE": "1", "DUNE_CACHE": "disabled", "OCAMLFIND_LDCONF": "ignore",
	}
	if sh, err := lookPath("bash", pathDirs); err == nil {
		e["SHELL"], e["CONFIG_SHELL"] = sh, sh
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
	return &runner{c: c, path: pathDirs, env: env, log: os.Stderr, top: filepath.Join(c.BuildTop, "src")}, nil
}

// run executes argv in dir. Its output goes to the build log; OCaml and C compiler errors also
// become structured diagnostics for bob.
func (r *runner) run(dir string, argv ...string) error {
	if len(argv) == 0 {
		return builder.Invalidf("empty command")
	}
	argv, err := r.resolve(dir, argv)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := &exec.Cmd{Path: argv[0], Args: argv, Env: r.env, Dir: dir, Stdout: &out, Stderr: &out}
	err = cmd.Run()
	_, _ = r.log.Write(out.Bytes())
	if err != nil {
		r.diags(out.Bytes())
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s: %s", path.Base(argv[0]), ee.ProcessState.String())
		}
		return fmt.Errorf("%s: %w", path.Base(argv[0]), err)
	}
	return nil
}

// resolve finds argv[0] (in PATH, or relative to dir) and runs scripts whose interpreter does
// not exist in the sandbox (#!/usr/bin/env x, #!/bin/bash) through the job's own programs.
func (r *runner) resolve(dir string, argv []string) ([]string, error) {
	prog := argv[0]
	if !strings.Contains(prog, "/") {
		p, err := lookPath(prog, r.path)
		if err != nil {
			return nil, err
		}
		return append([]string{p}, argv[1:]...), nil
	}
	if !filepath.IsAbs(prog) {
		prog = filepath.Join(dir, prog)
	}
	interp, err := shebang(prog)
	if err != nil || len(interp) == 0 {
		return append([]string{prog}, argv[1:]...), nil
	}
	name := path.Base(interp[0])
	args := interp[1:]
	viaEnv := name == "env" && len(args) > 0
	if viaEnv { // #!/usr/bin/env x: x from the job's PATH, never the host's
		name, args = path.Base(args[0]), args[1:]
	} else if _, err := os.Stat(interp[0]); err == nil {
		return append([]string{prog}, argv[1:]...), nil
	}
	p, err := lookPath(name, r.path)
	if err != nil {
		return append([]string{prog}, argv[1:]...), nil
	}
	return append(append(append([]string{p}, args...), prog), argv[1:]...), nil
}

// shebang returns the interpreter line of a script (nil for other files).
func shebang(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	rest, ok := strings.CutPrefix(line, "#!")
	if !ok {
		return nil, nil
	}
	return strings.Fields(rest), nil
}

var (
	ocamlLoc = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^File "([^"]+)", lines? ([0-9]+)(?:-[0-9]+)?, characters ([0-9]+)-`)
	})
	gccDiag = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^([^\s:][^:]*):([0-9]+):([0-9]+): (?:fatal )?error: (.*)$`)
	})
)

// diags reports compiler errors (at most 20) as structured diagnostics with repository paths.
func (r *runner) diags(out []byte) {
	n := 0
	lines := strings.Split(string(out), "\n")
	for i := 0; i < len(lines) && n < 20; i++ {
		if m := gccDiag().FindStringSubmatch(lines[i]); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			r.c.Diag("error", r.repoPath(m[1]), ln, col, m[4])
			n++
			continue
		}
		m := ocamlLoc().FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		for j := i + 1; j < len(lines) && j < i+40; j++ {
			if ocamlLoc().MatchString(lines[j]) {
				break
			}
			if msg, ok := strings.CutPrefix(lines[j], "Error: "); ok {
				ln, _ := strconv.Atoi(m[2])
				col, _ := strconv.Atoi(m[3])
				r.c.Diag("error", r.repoPath(m[1]), ln, col+1, msg)
				n++
				break
			}
		}
	}
}

// repoPath maps a file name the compiler printed (relative to the source copy) to its
// repository path.
func (r *runner) repoPath(f string) string {
	if rel, ok := strings.CutPrefix(f, r.top+"/"); ok {
		return rel
	}
	if filepath.IsAbs(f) {
		return f
	}
	return path.Join(r.dir, f)
}

func lookPath(name string, dirs []string) (string, error) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in the job's path", name)
}

// cores is the parallelism of a job: NIX_BUILD_CORES (at least 1).
func cores() int {
	n, err := strconv.Atoi(os.Getenv("NIX_BUILD_CORES"))
	if err != nil || n < 1 {
		n = 1
	}
	return n
}

// copySources copies dir of the source path src (a read-only store tree with repository paths)
// to <build>/src/<dir> and returns the copy's directory: writable, so compilers can write next
// to the sources as dune and ocamlfind expect.
func copySources(c *builder.Ctx, src, dir string) (string, error) {
	if !filepath.IsAbs(src) || !localRel(dir) {
		return "", builder.Invalidf("source %q / dir %q is not valid", src, dir)
	}
	to := filepath.Join(c.BuildTop, "src", dir)
	if err := c.MkdirAll(filepath.Dir(to)); err != nil {
		return "", err
	}
	if err := c.Copy(filepath.Join(src, dir), to); err != nil {
		return "", err
	}
	return to, makeWritable(to)
}

// makeWritable gives the owner write access below root (store copies are read-only).
func makeWritable(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode().Perm()|0o200)
	})
}

// localRel reports whether p is "" or a clean relative path without "..".
func localRel(p string) bool {
	if p == "" {
		return true
	}
	return !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.Contains(p, "\x00")
}

// localFiles checks that every file is a clean relative path.
func localFiles(files []string) error {
	for _, f := range files {
		if f == "" || !localRel(f) || f == "." {
			return builder.Invalidf("file %q is not a relative path", f)
		}
	}
	return nil
}
