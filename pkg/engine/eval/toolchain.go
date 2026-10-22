package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// toolchain is the Go toolchain the evaluator is compiled with.
//
// Until bob pins the evaluator's Go in its own store (R-FW-043.5), this is the go on PATH,
// which must be a Go that matches the framework's go.mod.
type toolchain struct {
	goroot  string
	version string
}

func findToolchain(ctx context.Context) (toolchain, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOROOT", "GOVERSION")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS="}
	cmd.Dir = os.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return toolchain{}, fmt.Errorf("evaluating bob.go needs a Go toolchain: go env: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || !filepath.IsAbs(f[0]) {
		return toolchain{}, fmt.Errorf("evaluating bob.go needs a Go toolchain: unexpected go env output %q", out)
	}
	goroot, err := filepath.EvalSymlinks(f[0])
	if err != nil {
		return toolchain{}, err
	}
	return toolchain{goroot: goroot, version: f[1]}, nil
}

// env is the complete environment of the evaluator (S13 §5.2); nothing else ever passes.
func (t toolchain) env() []string {
	return []string{
		"PATH=" + t.goroot + "/bin", "GOROOT=" + t.goroot, "GOCACHE=/work/gocache", "GOMODCACHE=/gomod",
		"GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=mod", "HOME=/work/home", "TMPDIR=/work/tmp",
	}
}

const frameworkModule = "github.com/megakuul/bob"

// findFramework returns the root of the bob module whose framework packages this binary was
// built from, and its go directive. R-FW-043.4 replaces this with the embedded framework
// module zip; until then the evaluator compiles against the source tree of this bob.
func findFramework() (dir, goDirective string, err error) {
	_, file, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(file) {
		dir := filepath.Dir(file)
		for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
			b, err := os.ReadFile(filepath.Join(d, "go.mod"))
			if err == nil && modulePath(b) == frameworkModule {
				return d, goVersion(b), nil
			}
		}
	}
	return "", "", errors.New("internal error: the bob framework sources are not available to the evaluator; this is a bob bug")
}

// modulePath returns the module path of a go.mod ("" if none).
func modulePath(gomod []byte) string {
	for _, l := range strings.Split(string(gomod), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
			return strings.Trim(strings.TrimSpace(p), `"`)
		}
	}
	return ""
}

// goVersion returns the go directive of a go.mod ("" if none).
func goVersion(gomod []byte) string {
	for _, l := range strings.Split(string(gomod), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "go "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
