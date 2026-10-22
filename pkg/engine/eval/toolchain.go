package eval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// toolchain is the Go toolchain the evaluator is compiled with: the pinned Go the caller
// passes (Request.GoRoot), or the go on PATH.
type toolchain struct {
	goroot  string
	version string // "go1.26.9"
}

func findToolchain(ctx context.Context, goroot string) (toolchain, error) {
	bin := "go"
	if goroot != "" {
		bin = filepath.Join(goroot, "bin", "go")
	}
	// Fast path (the eval key hit budget): GOROOT is the parent of bin/go and names its version
	// in its VERSION file.
	if p, err := exec.LookPath(bin); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			root := filepath.Dir(filepath.Dir(real))
			if b, err := os.ReadFile(filepath.Join(root, "VERSION")); err == nil {
				v, _, _ := strings.Cut(string(b), "\n")
				if strings.HasPrefix(v, "go1.") && !strings.ContainsAny(v, " \t") {
					return toolchain{goroot: root, version: strings.TrimSpace(v)}, nil
				}
			}
		}
	}
	cmd := exec.CommandContext(ctx, bin, "env", "GOROOT", "GOVERSION")
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
	root, err := filepath.EvalSymlinks(f[0])
	if err != nil {
		return toolchain{}, err
	}
	return toolchain{goroot: root, version: f[1]}, nil
}

// supports checks that the toolchain is at least the framework's go directive (with
// GOTOOLCHAIN=local, go would refuse the build with a raw message).
func (t toolchain) supports(goDir string) error {
	have := strings.TrimPrefix(t.version, "go")
	if goDir != "" && versionLess(have, goDir) {
		return ir.Errors{ir.ToolchainMismatch("the evaluator", "bob needs Go "+goDir+" or newer to compile bob.go, the toolchain is Go "+have)}
	}
	return nil
}

// env is the evaluator's complete environment, the S13 §5.2 contract: nothing else ever
// passes, and it is the same for every evaluation on a machine (R-FW-008).
func (t toolchain) env() []string {
	return []string{
		"PATH=" + t.goroot + "/bin", "GOROOT=" + t.goroot, "GOCACHE=/work/gocache", "GOMODCACHE=" + modDir,
		"GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=mod", "HOME=/work/home", "TMPDIR=/work/tmp",
	}
}

// buildEnv is the complete environment of go build in the sandbox (S13 §5.2, R-FW-043.5).
func (t toolchain) buildEnv() []string {
	return []string{
		"PATH=" + t.goroot + "/bin", "GOROOT=" + t.goroot, "GOCACHE=/work/gocache", "GOMODCACHE=" + modDir,
		"GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off", "GOSUMDB=off",
		"HOME=/work/home", "TMPDIR=/work/tmp",
	}
}

// modulePath returns the module path of a go.mod ("" if none).
func modulePath(gomod []byte) string { return parseGoMod(gomod).module }

// goVersion returns the go directive of a go.mod ("" if none).
func goVersion(gomod []byte) string { return parseGoMod(gomod).goDir }

// versionLess compares Go versions such as "1.26" and "1.26.9" numerically.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
