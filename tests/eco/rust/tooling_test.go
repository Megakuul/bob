package rust

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// unresolvedRe matches the counters of `rust-analyzer analysis-stats` about unresolved items.
var unresolvedRe = regexp.MustCompile(`(?i)unresolved (imports|macro calls?)\D{0,20}(\d+)`)

// R-RS-003a: in the Rust fixture after bob gen, through the shims as a non-root user:
// rust-analyzer analysis-stats . reports 0 unresolved imports and 0 unresolved macro calls
// (proc-macros expand from store dylibs); cargo metadata --locked --offline exits 0. The
// "host without /nix" condition cannot be enforced here; the CI host must lack /nix.
func TestRRS003aRustAnalyzerAndCargoMetadata(t *testing.T) {
	parity.Covers(t, "R-RS-003a")
	uid, gid := unprivileged(t)
	dir := harness.Fixture(t, "rust")
	home := t.TempDir()
	for _, sub := range []string{"user", "bob", "xdg"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	chownTree(t, dir, uid, gid)
	chownTree(t, home, uid, gid)
	userHome := filepath.Join(home, "user")
	env := registryEnv(filepath.Join(home, "bob"), "XDG_CACHE_HOME="+filepath.Join(home, "xdg"))
	for _, args := range [][]string{{"lock"}, {"gen"}, {"env", "--format", "json"}} {
		if r := runAs(t, uid, gid, userHome, dir, env, harness.Bin(t, "bob"), args...); !ok(t, r) {
			return
		}
	}
	shimEnv := append(env, "PATH="+filepath.Join(dir, ".bob", "bin")+":/usr/bin:/bin")
	ra := runAs(t, uid, gid, userHome, dir, shimEnv, filepath.Join(dir, ".bob", "bin", "rust-analyzer"), "analysis-stats", ".")
	if ra.Exit != 0 {
		t.Errorf("rust-analyzer analysis-stats . exited %d:\n%s%s", ra.Exit, ra.Stdout, ra.Stderr)
	}
	counters := unresolvedRe.FindAllStringSubmatch(ra.Stdout+ra.Stderr, -1)
	if len(counters) == 0 {
		t.Errorf("analysis-stats printed no unresolved imports / macro calls counters:\n%s%s", ra.Stdout, ra.Stderr)
	}
	for _, c := range counters {
		if n, _ := strconv.Atoi(c[2]); n != 0 {
			t.Errorf("rust-analyzer reports %s unresolved %s", c[2], c[1])
		}
	}
	cm := runAs(t, uid, gid, userHome, dir, shimEnv, filepath.Join(dir, ".bob", "bin", "cargo"), "metadata", "--locked", "--offline", "--format-version", "1")
	if cm.Exit != 0 {
		t.Errorf("cargo metadata --locked --offline exited %d:\n%s", cm.Exit, cm.Stderr)
	}
}
