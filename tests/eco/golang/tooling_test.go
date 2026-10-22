package golang

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// bobAs runs bob as the unprivileged test user with its own home and XDG cache, so the
// goproxy directory and the shims belong to that user.
func bobAs(t *testing.T, uid, gid uint32, home, dir string, args ...string) harness.Result {
	t.Helper()
	return runAs(t, uid, gid, filepath.Join(home, "user"), dir,
		registryEnv(filepath.Join(home, "bob"), "XDG_CACHE_HOME="+filepath.Join(home, "xdg")),
		harness.Bin(t, "bob"), args...)
}

// R-GO-003a: in the Go fixture, through the shims, as a non-root user: go build ./..., go vet
// ./... and gopls check on a file importing a third-party and a generated package succeed
// offline; go get golang.org/x/text@latest fails and leaves the files unchanged; go mod tidy is
// detected as drift by the next bob build (R-FW-061).
func TestRGO003aGoToolingOnGeneratedFiles(t *testing.T) {
	parity.Covers(t, "R-GO-003a")
	uid, gid := unprivileged(t)
	dir := harness.Fixture(t, "go")
	home := t.TempDir()
	for _, sub := range []string{"user", "bob", "xdg"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	chownTree(t, dir, uid, gid)
	chownTree(t, home, uid, gid)
	for _, args := range [][]string{{"lock"}, {"gen"}, {"env", "--format", "json"}} {
		if r := bobAs(t, uid, gid, home, dir, args...); !ok(t, r) {
			return
		}
	}
	shim := func(name string) string { return filepath.Join(dir, ".bob", "bin", name) }
	env := registryEnv(filepath.Join(home, "bob"), "XDG_CACHE_HOME="+filepath.Join(home, "xdg"), "PATH="+filepath.Join(dir, ".bob", "bin")+":/usr/bin:/bin")
	userHome := filepath.Join(home, "user")
	api := filepath.Join(dir, "api")
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
		if r := runAs(t, uid, gid, userHome, api, env, shim("go"), args...); r.Exit != 0 {
			t.Errorf("go %v through the shim exited %d:\n%s%s", args, r.Exit, r.Stdout, r.Stderr)
		}
	}
	// msg.go imports github.com/google/uuid (third-party) and the generated msg/v1 package.
	if r := runAs(t, uid, gid, userHome, api, env, shim("gopls"), "check", "internal/msg/msg.go"); r.Exit != 0 || r.Stdout+r.Stderr != "" {
		t.Errorf("gopls check internal/msg/msg.go exited %d with diagnostics:\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}

	before := treeHash(t, dir)
	if r := runAs(t, uid, gid, userHome, api, env, shim("go"), "get", "golang.org/x/text@latest"); r.Exit == 0 {
		t.Errorf("go get golang.org/x/text@latest succeeded; go.mod must be read-only or the proxy must miss:\n%s%s", r.Stdout, r.Stderr)
	}
	if after := treeHash(t, dir); after != before {
		t.Errorf("go get changed files of the working tree")
	}

	// go mod tidy after making go.mod/go.sum writable (the read-only mode already stopped
	// go get). If tidy rewrites either file, the next bob build must reject it as drift; if it
	// leaves both byte-identical there is nothing to detect and the build must still pass.
	goMod, goSum := filepath.Join(api, "go.mod"), filepath.Join(api, "go.sum")
	origMod, origSum := readFile(t, goMod), readFile(t, goSum)
	for _, p := range []string{goMod, goSum} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	r := runAs(t, uid, gid, userHome, api, env, shim("go"), "mod", "tidy")
	changed := readFile(t, goMod) != origMod || readFile(t, goSum) != origSum
	b := bobAs(t, uid, gid, home, dir, "build", "//...", "--json")
	if changed {
		if !exitIs(t, b, 3) {
			return
		}
		e, found := errorWithCode(t, b, "BOB-E-1011")
		if found && e.Message == "" {
			t.Errorf("the drift error has no message")
		}
	} else if r.Exit == 0 && !exitIs(t, b, 0) {
		return
	}
}
