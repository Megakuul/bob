package build

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/megakuul/bob/pkg/engine/store"
)

// The validity hint (A314): the store paths a run of one command in one repository asked
// about, kept in the store's drv cache (<home>/cache/drv/validity/, R-NIX-024 layout). The
// next run of the same command asks Nix about all of them in one batch while the evaluator
// runs (store.Prefetch), so the many single checks of planning, fetching and realising are
// answered from that batch. The hint only chooses what to ask: validity itself always comes
// from Nix in the current run, so a stale, foreign or damaged hint costs at most one larger
// query.

// Bounds of a hint file (it is read before every run).
const (
	maxHintPaths = 100000
	maxHintBytes = 16 << 20
)

// hintPath is the hint file of the command and repository of o (one file per repository and
// command, whatever the patterns: a run of other patterns only rewrites it).
func hintPath(home string, o Options) string {
	cmd := o.Command
	if cmd == "" {
		cmd = CmdBuild
	}
	h := sha256.Sum256([]byte(string(cmd) + "\x00" + o.Root))
	return filepath.Join(home, "cache", "drv", "validity", hex.EncodeToString(h[:12]))
}

// readHint returns the paths of the hint file (nil when there is none or it is unusable).
func readHint(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxHintBytes+1))
	if err != nil || len(b) > maxHintBytes {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); strings.HasPrefix(l, "/nix/store/") {
			out = append(out, l)
		}
		if len(out) > maxHintPaths {
			return nil
		}
	}
	return out
}

// startHint starts the prefetch of the hint of o and returns the function that records this
// paths this run asked about at the end. Writing is best effort and happens only when the set changed;
// it is not synced (a lost or torn hint only costs one run its batching).
// A worker's run (Untrusted) has a fresh repository every time and keeps no hint.
func startHint(ctx context.Context, st *store.Store, o Options) func() {
	if o.Untrusted || o.Root == "" {
		return func() {}
	}
	file := hintPath(st.Home(), o)
	old := readHint(file)
	st.Prefetch(ctx, old)
	return func() {
		now := st.AskedPaths()
		if len(now) == 0 || len(now) > maxHintPaths || slices.Equal(old, now) {
			return
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return
		}
		tmp, err := os.CreateTemp(filepath.Dir(file), ".hint-")
		if err != nil {
			return
		}
		w := bufio.NewWriter(tmp)
		for _, p := range now {
			_, _ = w.WriteString(p + "\n")
		}
		werr := w.Flush()
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
			_ = os.Remove(tmp.Name())
		}
	}
}
