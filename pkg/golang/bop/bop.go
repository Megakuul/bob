package bop

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/builder"
)

// Kinds returns the Go job kinds (SPEC §03.0.4); cmd/bob-builder adds them to builder.Core().
// Go's outputs are archives and statically known binaries: the fixup is skipped, so archive
// bytes stay exactly what the compiler wrote.
func Kinds() map[string]builder.Kind {
	return map[string]builder.Kind{
		"go-std":      {Run: stdKind, NoFixup: true},
		"go-tools":    {Run: toolsKind, NoFixup: true},
		"go-compile":  {Run: buildKind, NoFixup: true},
		"go-cgo":      {Run: buildKind, NoFixup: true},
		"go-link":     {Run: buildKind, NoFixup: true},
		"go-testmain": {Run: buildKind, NoFixup: true},
	}
}

// maxStdList bounds the output of go list in the std job.
const maxStdList = 16 << 20

func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return builder.Invalidf("go job args: %v", err)
	}
	return nil
}

func (t Toolchain) check() error {
	if !filepath.IsAbs(t.GoRoot) || t.GOOS == "" || t.GOARCH == "" {
		return builder.Invalidf("go job: goroot, goos and goarch are required")
	}
	return nil
}

// tool is the path of a Go tool of the toolchain (compile, link, asm, cgo).
func (t Toolchain) tool(name string) string {
	return filepath.Join(t.GoRoot, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH, name)
}

// env is the environment of the toolchain's programs (on top of the builder's R-NIX-016 set).
func (t Toolchain) env(tmp string, cgo bool) []string {
	e := map[string]string{
		"PATH": strings.Join(t.Path, ":"), "HOME": tmp, "TMPDIR": tmp, "SOURCE_DATE_EPOCH": "1",
		"TZ": "UTC", "LC_ALL": "C", "GOROOT": t.GoRoot, "GOOS": t.GOOS, "GOARCH": t.GOARCH,
		"GOCACHE": filepath.Join(tmp, "gocache"), "GOPATH": filepath.Join(tmp, "gopath"),
		"GOTOOLCHAIN": "local", "GOFLAGS": "", "GOENV": "off", "GOTELEMETRY": "off",
		"GOPROXY": "off", "GOSUMDB": "off", "CGO_ENABLED": "0", "TERM": "dumb",
	}
	if cgo && t.CC != "" {
		e["CGO_ENABLED"], e["CC"] = "1", t.CC
	}
	if t.GOARCH == "amd64" {
		e["GOAMD64"] = "v1"
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + e[k]
	}
	return out
}

// runner runs the toolchain's programs of one job; their output goes to the build log.
type runner struct {
	c   *builder.Ctx
	t   Toolchain
	tmp string
	cgo bool
	log io.Writer
	mu  sync.Mutex // serialises log writes of parallel compiles
}

func newRunner(c *builder.Ctx, t Toolchain, cgo bool) (*runner, error) {
	tmp, err := c.TmpDir()
	if err != nil {
		return nil, err
	}
	return &runner{c: c, t: t, tmp: tmp, cgo: cgo, log: os.Stderr}, nil
}

// run executes argv in dir and returns its stdout; stderr goes to the build log, stdout too
// unless capture is set.
func (r *runner) run(dir string, capture bool, argv ...string) ([]byte, error) {
	prog := argv[0]
	if !strings.Contains(prog, "/") {
		p, err := lookPath(prog, r.t.Path)
		if err != nil {
			return nil, err
		}
		prog = p
	}
	var stdout, stderr bytes.Buffer
	cmd := &exec.Cmd{Path: prog, Args: argv, Env: r.t.env(r.tmp, r.cgo), Dir: dir, Stdout: &stdout, Stderr: &stderr}
	err := cmd.Run()
	r.mu.Lock()
	if !capture {
		_, _ = r.log.Write(stdout.Bytes())
	}
	_, _ = r.log.Write(stderr.Bytes())
	r.mu.Unlock()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("%s: %s", filepath.Base(argv[0]), ee.ProcessState.String())
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(argv[0]), err)
	}
	return stdout.Bytes(), nil
}

func lookPath(name string, path []string) (string, error) {
	for _, d := range path {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in the job's path %v", name, path)
}

// outPath checks that p lies in a declared output and creates its parent directory.
func outPath(c *builder.Ctx, p string) (string, error) {
	w, err := c.Writable(p)
	if err != nil {
		return "", err
	}
	if err := c.MkdirAll(filepath.Dir(w)); err != nil {
		return "", err
	}
	return w, nil
}

// stdKind is kind go-std: `go list -export -deps std` compiles the standard library for the
// target; each package's archive is copied to $out/pkg/<importpath>.a and listed in
// $out/importcfg.
func stdKind(c *builder.Ctx, raw json.RawMessage) error {
	var a StdArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if err := a.check(); err != nil {
		return err
	}
	r, err := newRunner(c, a.Toolchain, a.CC != "")
	if err != nil {
		return err
	}
	c.Phase("go-std")
	out, err := r.run(r.tmp, true, filepath.Join(a.GoRoot, "bin", "go"), "list", "-trimpath", "-export", "-deps",
		"-f", "{{if .Export}}{{.ImportPath}} {{.Export}}{{end}}", "std")
	if err != nil {
		return err
	}
	if len(out) > maxStdList {
		return fmt.Errorf("go list std printed more than %d bytes", maxStdList)
	}
	var cfg strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		ip, file, ok := strings.Cut(sc.Text(), " ")
		if !ok || ip == "" || strings.Contains(ip, "..") {
			continue
		}
		dst, err := outPath(c, filepath.Join(a.Out, "pkg", filepath.FromSlash(ip)+".a"))
		if err != nil {
			return err
		}
		if err := copyFile(file, dst); err != nil {
			return err
		}
		fmt.Fprintf(&cfg, "packagefile %s=%s\n", ip, dst)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	p, err := outPath(c, filepath.Join(a.Out, "importcfg"))
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(cfg.String()), 0o644)
}

// toolsKind is kind go-tools: cmd/pack, which Go ≥ 1.24 no longer ships prebuilt.
func toolsKind(c *builder.Ctx, raw json.RawMessage) error {
	var a ToolsArgs
	if err := decode(raw, &a); err != nil {
		return err
	}
	if err := a.check(); err != nil {
		return err
	}
	r, err := newRunner(c, a.Toolchain, false)
	if err != nil {
		return err
	}
	dst, err := outPath(c, filepath.Join(a.Out, "bin", "pack"))
	if err != nil {
		return err
	}
	c.Phase("go-tools")
	_, err = r.run(r.tmp, false, filepath.Join(a.GoRoot, "bin", "go"), "build", "-trimpath", "-o", dst, "cmd/pack")
	return err
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// workDir is the package's work directory: $TMPDIR/b<sha256(importpath)[:12]>, the same in
// every grouping (cgo object paths can reach debug info).
func workDir(tmp, importPath string) string {
	h := sha256.Sum256([]byte(importPath))
	return filepath.Join(tmp, "b"+hex.EncodeToString(h[:6]))
}

// cores is the -c of the compiler: min(NIX_BUILD_CORES, 4).
func cores() int {
	n, err := strconv.Atoi(os.Getenv("NIX_BUILD_CORES"))
	if err != nil || n < 1 {
		n = 1
	}
	return min(n, 4)
}
