package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

// The trusted seed GOCACHE (S13-6): the standard library and the framework compiled once per
// toolchain and framework, by trusted code only, inside the same sandbox and with the same
// flags as an evaluation. Evaluator builds mount it as the overlay lower layer of /work/gocache,
// so they start warm (S13: 13.5 s -> 0.56 s) and can never poison it.

// seedLimits bound the seed build: it compiles the whole standard library once.
func seedLimits() evalbox.Limits {
	return evalbox.Limits{WallClock: 10 * time.Minute, Pids: 4096, MemoryBytes: 8 << 30, WorkBytes: 2 << 30}
}

// ensureSeed returns the seed directory for tc and fw, building it when missing. "" (no seed,
// cold builds) when there is no user cache directory.
func ensureSeed(ctx context.Context, tc toolchain, fw *framework, modcache string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", nil
	}
	h := sha256.Sum256([]byte(tc.version + "\x00" + tc.goroot + "\x00" + runtime.GOARCH + "\x00" + fw.version + "\x00" + fw.h1))
	dir := filepath.Join(base, "bob", "eval-seed", hex.EncodeToString(h[:8]))
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".tmp-")
	if err != nil {
		return "", err
	}
	defer func() { _ = makeWritable(tmp); _ = os.RemoveAll(tmp) }()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	cache := filepath.Join(tmp, "gocache")
	repo := filepath.Join(tmp, "repo")
	for _, d := range []string{cache, repo} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return "", err
		}
	}
	gomod := "module bobseed\n\ngo " + fw.goDir + "\n\nrequire " + frameworkModule + " " + fw.version + "\n"
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte(gomod), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(repo, "go.sum"), []byte(fw.sumLines()), 0o644); err != nil {
		return "", err
	}
	args := append([]string{tc.goroot + "/bin/go", "build", "-tags=bob", "-trimpath", "std"}, fw.packages()...)
	_, err = evalbox.Run(ctx, evalbox.Spec{
		Binds: []evalbox.Bind{
			{Host: tc.goroot, Sandbox: tc.goroot, Exec: true},
			{Host: repo, Sandbox: srcDir},
			{Host: modcache, Sandbox: modDir},
		},
		Seed:   cache,
		SeedRW: true,
		Dir:    srcDir,
		Steps:  []evalbox.Step{{Args: args, Env: tc.buildEnv()}},
		Limits: seedLimits(),
	})
	if err != nil {
		return "", fmt.Errorf("internal error: building the evaluator's seed cache failed; this is a bob bug: %w", err)
	}
	if err := os.Rename(cache, dir); err != nil && !errors.Is(err, os.ErrExist) {
		if _, statErr := os.Stat(dir); statErr != nil { // not a concurrent winner
			return "", err
		}
	}
	return dir, nil
}
