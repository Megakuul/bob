package cc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/golden"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// R-CC-003a: after bob gen, compile_commands.json equals the golden (directory replaced by
// $REPO) and `.bob/bin/clangd --check=<file>` as a non-root user reports 0 errors for a file
// including <openssl/evp.h> (the fixture's pack.cpp; the SPEC names foo.cpp / ssl.h). The
// "host without /nix" condition cannot be enforced by this test; the CI host must lack it.
func TestRCC003aCompileCommandsResolveIncludes(t *testing.T) {
	parity.Covers(t, "R-CC-003a")
	uid, gid := unprivileged(t)
	srv := newArchiveServer(t, "original")
	home := t.TempDir()
	for _, sub := range []string{"user", "bob", "xdg"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	dir := ccFixture(t, srv)
	chownTree(t, dir, uid, gid)
	chownTree(t, home, uid, gid)
	userHome := filepath.Join(home, "user")
	env := registryEnv(filepath.Join(home, "bob"), "XDG_CACHE_HOME="+filepath.Join(home, "xdg"))
	for _, args := range [][]string{{"lock"}, {"gen"}, {"env", "--format", "json"}} {
		if r := runAs(t, uid, gid, userHome, dir, env, harness.Bin(t, "bob"), args...); !ok(t, r) {
			return
		}
	}
	cc := readFile(t, filepath.Join(dir, "compile_commands.json"))
	var entries []struct {
		Directory string `json:"directory"`
		File      string `json:"file"`
	}
	if err := json.Unmarshal([]byte(cc), &entries); err != nil {
		t.Fatalf("compile_commands.json: %v", err)
	}
	for _, e := range entries {
		if e.Directory != dir {
			t.Errorf("entry for %s has directory %q, want %q", e.File, e.Directory, dir)
		}
	}
	golden.Assert(t, "tests/goldens/eco/cc/compile_commands.json", []byte(cc), golden.Reqs("R-CC-003a"),
		// the fixture lives in a temp dir, not under the repo root golden.Assert replaces
		golden.Normalize(func(b []byte) []byte { return bytes.ReplaceAll(b, []byte(dir), []byte("$REPO")) }))

	const file = "libs/pack/src/pack.cpp"
	if !strings.Contains(readFile(t, filepath.Join(dir, file)), "#include <openssl/evp.h>") {
		t.Fatalf("harness: %s no longer includes <openssl/evp.h>", file)
	}
	r := runAs(t, uid, gid, userHome, dir, append(env, "PATH="+filepath.Join(dir, ".bob", "bin")+":/usr/bin:/bin"),
		filepath.Join(dir, ".bob", "bin", "clangd"), "--check="+file)
	out := r.Stdout + r.Stderr
	if r.Exit != 0 || !strings.Contains(out, "All checks completed, 0 errors") {
		t.Errorf("clangd --check=%s exited %d without \"All checks completed, 0 errors\":\n%s", file, r.Exit, out)
	}
	if strings.Contains(out, "file not found") {
		t.Errorf("clangd could not resolve an include:\n%s", out)
	}
}
