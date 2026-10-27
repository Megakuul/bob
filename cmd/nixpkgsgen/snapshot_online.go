//go:build online

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/store"
)

// The online step is built only with -tags online: gen and -check stay free of the network,
// the store and cgo (CI runs -check offline on hosts without a C compiler).

//go:embed snapshot.nix
var snapshotNix []byte

const (
	batchSize = 1500 // attributes per evaluation (memory bound, R-NIX-027)
	parallel  = 3
)

// snapshot evaluates nixpkgs rev in bob's own namespace (R-NIX-021) and writes the snapshot.
// Online: the nixpkgs source is fetched (once) like a pin of bob lock (R-NIX-005).
func snapshot(ctx context.Context, root, rev, out string, extra []string) error {
	if !hex40.MatchString(rev) {
		return fmt.Errorf("snapshot: -rev %q is not a 40-hex nixpkgs revision", rev)
	}
	st, err := store.Open(ctx, store.Config{})
	if err != nil {
		return err
	}
	f := fetch.New(st, fetch.Policy{})
	pin, err := lock.PinNixpkgs(ctx, f, lock.DefaultChannel+"@"+rev, lock.NixpkgsPin{}, false, nil)
	if err != nil {
		return fmt.Errorf("snapshot: pinning nixpkgs %s: %w", rev, err)
	}
	src, err := f.Ensure(ctx, lock.NixpkgsRequest(pin))
	if err != nil {
		return fmt.Errorf("snapshot: fetching nixpkgs %s: %w", rev, err)
	}
	dir := filepath.Join(st.Home(), "cache", "nixpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sum := sha256.Sum256(snapshotNix)
	script := filepath.Join(dir, "nixpkgsgen-"+hex.EncodeToString(sum[:6])+".nix")
	if err := os.WriteFile(script, snapshotNix, 0o644); err != nil {
		return err
	}
	ev := evaluator{st: st, script: script, src: src, system: store.HostSystem()}
	_, names, err := ev.eval(ctx, nil, true)
	if err != nil {
		return fmt.Errorf("snapshot: listing attributes: %w", err)
	}
	if names == nil {
		return errors.New("snapshot: the attribute list could not be evaluated")
	}
	want := slices.Concat(names, extra)
	sort.Strings(want)
	want = slices.Compact(want)
	snap := &Snapshot{Rev: rev, System: ev.system, Names: names, Attrs: map[string]Info{}}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	start := time.Now()
	done := 0
	for i := 0; i < len(want); i += batchSize {
		batch := want[i:min(i+batchSize, len(want))]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					firstErr = cmp(firstErr, fmt.Errorf("snapshot: %v", r))
					mu.Unlock()
				}
			}()
			got, err := ev.resolve(ctx, batch)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				firstErr = cmp(firstErr, err)
				return
			}
			for a, inf := range got {
				snap.Attrs[a] = inf
			}
			done += len(batch)
			fmt.Fprintf(os.Stderr, "nixpkgsgen: %d/%d attributes (%s)\n", done, len(want), time.Since(start).Round(time.Second))
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return writeSnapshot(out, snap)
}

func cmp(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

type evaluator struct {
	st                  *store.Store
	script, src, system string
}

// resolve evaluates attrs; a failing batch is split until the failing attributes are isolated,
// which are recorded as Failed (excluded by gen: evaluation fails, ADR 0020 §2).
func (e evaluator) resolve(ctx context.Context, attrs []string) (map[string]Info, error) {
	got, _, err := e.eval(ctx, attrs, false)
	if err != nil {
		return nil, err
	}
	if got != nil {
		return got, nil
	}
	if len(attrs) == 1 {
		return map[string]Info{attrs[0]: {Failed: true}}, nil
	}
	out := map[string]Info{}
	mid := len(attrs) / 2
	for _, half := range [][]string{attrs[:mid], attrs[mid:]} {
		g, err := e.resolve(ctx, half)
		if err != nil {
			return nil, err
		}
		for a, inf := range g {
			out[a] = inf
		}
	}
	return out, nil
}

// eval runs snapshot.nix once; a failed evaluation returns nil maps and no error.
func (e evaluator) eval(ctx context.Context, attrs []string, names bool) (map[string]Info, []string, error) {
	if attrs == nil {
		attrs = []string{}
	}
	aj, _ := json.Marshal(attrs)
	var stdout, stderr bytes.Buffer
	cmd := e.st.Command(ctx, "nix", "eval", "--json", "--file", e.script,
		"--argstr", "src", e.src, "--argstr", "system", e.system,
		"--argstr", "attrs", string(aj), "--argstr", "names", fmt.Sprint(names), "result")
	cmd.Env = []string{"NIX_PATH=", "NIXPKGS_ALLOW_UNFREE=", "NIXPKGS_ALLOW_INSECURE=", "NIXPKGS_ALLOW_BROKEN="}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		var ee interface{ ExitCode() int }
		if !errors.As(err, &ee) {
			return nil, nil, fmt.Errorf("running nix eval: %w", err)
		}
		if len(attrs) <= 1 {
			fmt.Fprintf(os.Stderr, "nixpkgsgen: %v fails to evaluate: %s\n", attrs, lastLine(stderr.String()))
		}
		return nil, nil, nil
	}
	var r struct {
		Attrs map[string]Info `json:"attrs"`
		Names []string        `json:"names"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		return nil, nil, fmt.Errorf("unexpected nix eval output: %w", err)
	}
	for _, a := range attrs {
		if _, ok := r.Attrs[a]; !ok {
			return nil, nil, fmt.Errorf("nix eval did not answer %s", a)
		}
	}
	return r.Attrs, r.Names, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// writeSnapshot writes s deterministically (sorted keys through encoding/json maps).
func writeSnapshot(path string, s *Snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	if _, err := enc.Write(b); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
