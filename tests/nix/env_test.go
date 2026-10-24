package nix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/tests/harness"
)

// bobEnv is one private bob state: its own BOB_HOME and XDG directories, and fixture checkouts
// that persist across several runs (so a second `bob build` finds a warm store).
type bobEnv struct {
	Home     string
	XDG      string // XDG_CONFIG_HOME, XDG_DATA_HOME and XDG_CACHE_HOME live below it
	fixtures map[string]string
}

func newBobEnv(t testing.TB) *bobEnv {
	t.Helper()
	root := t.TempDir()
	return &bobEnv{Home: filepath.Join(root, "bobhome"), XDG: filepath.Join(root, "xdg"), fixtures: map[string]string{}}
}

// env is the environment every bob run gets on top of the harness' minimal one.
func (e *bobEnv) env(extra ...string) []string {
	return append([]string{
		"BOB_HOME=" + e.Home,
		"XDG_CONFIG_HOME=" + filepath.Join(e.XDG, "config"),
		"XDG_DATA_HOME=" + filepath.Join(e.XDG, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(e.XDG, "cache"),
	}, extra...)
}

// dir returns the (single) checkout of fixture name.
func (e *bobEnv) dir(t testing.TB, name string) string {
	t.Helper()
	if d, ok := e.fixtures[name]; ok {
		return d
	}
	d := harness.Fixture(t, name)
	e.fixtures[name] = d
	return d
}

// run runs bob in fixture name.
func (e *bobEnv) run(t testing.TB, fixture string, args ...string) harness.Result {
	t.Helper()
	return e.runEnv(t, fixture, nil, args...)
}

func (e *bobEnv) runEnv(t testing.TB, fixture string, extra []string, args ...string) harness.Result {
	t.Helper()
	return harness.Run(t, "bob", harness.Spec{Dir: e.dir(t, fixture), Env: e.env(extra...), Timeout: 20 * time.Minute}, args...)
}

// storeDir is the real directory of bob's /nix/store.
func (e *bobEnv) storeDir() string { return filepath.Join(e.Home, "nix", "store") }

// bobDrvs lists the .drv files of bob's own derivations (builder bob-builder) in bob's store.
func (e *bobEnv) bobDrvs(t testing.TB) []string {
	t.Helper()
	ents, err := os.ReadDir(e.storeDir())
	if err != nil {
		t.Fatalf("bob's store is missing after a successful build: %v", err)
	}
	var out []string
	for _, de := range ents {
		if !strings.HasSuffix(de.Name(), ".drv") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.storeDir(), de.Name()))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		if bytes.Contains(b, []byte("bob-builder")) {
			out = append(out, "/nix/store/"+de.Name())
		}
	}
	sort.Strings(out)
	return out
}

// hostNix runs the host's nix against bob's store (chroot store at Home).
func (e *bobEnv) hostNix(t testing.TB, stdin []byte, args ...string) (string, error) {
	t.Helper()
	o := &oracle{t: t, root: e.Home, home: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := o.cmd(ctx, "nix", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("nix %s: %w: %s", strings.Join(args, " "), err, se.String())
	}
	return strings.TrimSpace(so.String()), nil
}

// crossCheck re-submits the drv at path (a /nix/store path inside bob's store) to Nix with
// `nix derivation add`, where Nix computes the derivation path from the content it reads.
func (e *bobEnv) crossCheck(t testing.TB, path string) error {
	t.Helper()
	show, err := e.hostNix(t, nil, "derivation", "show", path)
	if err != nil {
		return err
	}
	var doc struct {
		Derivations map[string]json.RawMessage `json:"derivations"`
	}
	if err := json.Unmarshal([]byte(show), &doc); err != nil || len(doc.Derivations) != 1 {
		return fmt.Errorf("unexpected `nix derivation show` output: %v", err)
	}
	for _, d := range doc.Derivations {
		got, err := e.hostNix(t, d, "derivation", "add", "--dry-run")
		if err != nil {
			return err
		}
		if got != path {
			return fmt.Errorf("Nix computes the derivation path %s from the content, bob stored %s", got, path)
		}
	}
	return nil
}

// pin is a fetched nixpkgs at the pinned revision with an opened store and index.
type pin struct {
	red    bool
	st     *store.Store
	src    string
	rev    string
	system string
	idx    *nixidx.Index
}

// pinnedNixpkgs opens a store in a temporary home, fetches nixpkgs at the revision R-NIX-005
// names (nixos-26.05, cd4ea8d5…) and opens its index for this host's system.
func pinnedNixpkgs(t testing.TB) *pin {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, store.Config{Home: filepath.Join(t.TempDir(), "bobhome")})
	if notImplemented(t, err, "store.Open") {
		return &pin{red: true}
	}
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rev := nixpkgsRev(t)
	f := fetch.New(st, fetch.Policy{})
	req := fetch.Request{
		URLs: []string{"https://github.com/NixOS/nixpkgs/archive/" + rev + ".tar.gz"},
		Kind: fetch.Archive, Name: "source", Strip: 1, Label: "nixpkgs",
	}
	_, narHash, err := f.Resolve(ctx, req)
	if notImplemented(t, err, "fetch.Resolve") {
		return &pin{red: true}
	}
	if err != nil {
		t.Fatalf("fetch.Resolve nixpkgs: %v", err)
	}
	req.NarHash = narHash
	src, err := f.Ensure(ctx, req)
	if err != nil {
		t.Fatalf("fetch.Ensure nixpkgs: %v", err)
	}
	system := hostSystem(t)
	idx, err := nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: rev, System: system})
	if notImplemented(t, err, "nixidx.Open") {
		return &pin{red: true}
	}
	if err != nil {
		t.Fatalf("nixidx.Open: %v", err)
	}
	return &pin{st: st, src: src, rev: rev, system: system, idx: idx}
}

// hostSystem is the Nix system of the test host.
func hostSystem(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "aarch64", "arm64":
		return "aarch64-linux"
	case "x86_64":
		return "x86_64-linux"
	}
	t.Fatalf("harness: unsupported host architecture %q", out)
	return ""
}

// parsedClosure is the transitive closure of input derivations of root, parsed from the store.
type parsedClosure map[string]*drv.Derivation

func (c parsedClosure) Modulo(p string) ([32]byte, error) {
	d, ok := c[p]
	if !ok {
		return [32]byte{}, fmt.Errorf("derivation %s is not in the closure", p)
	}
	return d.Modulo(c)
}

// memoClosure resolves each derivation's hashDerivationModulo once, as bob's own resolvers do
// (pkg/engine/build resolver.memo). parsedClosure alone rehashes an input once per path that
// leads to it, which is exponential in the depth of the stdenv bootstrap chain (the test ran
// for more than 75 minutes, I-B27/I-B44); the hashes themselves are the same.
type memoClosure struct {
	c    parsedClosure
	memo map[string][32]byte
}

func (m *memoClosure) Modulo(p string) ([32]byte, error) {
	if h, ok := m.memo[p]; ok {
		return h, nil
	}
	d, ok := m.c[p]
	if !ok {
		return [32]byte{}, fmt.Errorf("derivation %s is not in the closure", p)
	}
	h, err := d.Modulo(m)
	if err != nil {
		return h, err
	}
	m.memo[p] = h
	return h, nil
}

func (p *pin) closure(t testing.TB, root string) parsedClosure {
	t.Helper()
	c := parsedClosure{}
	var walk func(string)
	walk = func(path string) {
		if _, ok := c[path]; ok {
			return
		}
		b, err := os.ReadFile(filepath.Join(p.st.Home(), "nix", "store", filepath.Base(path)))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		d, err := drv.Parse(b)
		if err != nil {
			t.Fatalf("drv.Parse %s: %v", path, err)
		}
		c[path] = d
		for _, in := range d.InputDrvs {
			walk(in.Path)
		}
	}
	walk(root)
	return c
}

// recomputeClosure recomputes the output paths and the drv path of attr and of every input
// derivation (parsed, recursively) and compares them with the recorded ones.
func recomputeClosure(t testing.TB, p *pin, attr string) error {
	t.Helper()
	ents, err := p.idx.Lookup(context.Background(), []string{attr})
	if err != nil {
		return fmt.Errorf("lookup %s: %w", attr, err)
	}
	e, ok := ents[attr]
	if !ok || e.DrvPath == "" {
		return fmt.Errorf("the index has no drvPath for %s", attr)
	}
	c := p.closure(t, e.DrvPath)
	t.Logf("%s: %d derivations in the closure", attr, len(c))
	if len(c) < 100 {
		return fmt.Errorf("closure of %s has only %d derivations: the parse is incomplete", attr, len(c))
	}
	var bad []string
	r := &memoClosure{c: c, memo: map[string][32]byte{}}
	for path, orig := range c {
		cp := *orig
		cp.Outputs = append([]drv.Output(nil), orig.Outputs...)
		cp.Env = map[string]string{}
		for k, v := range orig.Env {
			cp.Env[k] = v
		}
		for i := range cp.Outputs {
			cp.Outputs[i].Path = ""
			if _, ok := cp.Env[cp.Outputs[i].Name]; ok {
				cp.Env[cp.Outputs[i].Name] = ""
			}
		}
		if err := cp.Finalize(r); err != nil {
			return fmt.Errorf("finalize %s: %w", path, err)
		}
		for i, o := range cp.Outputs {
			if o.Path != orig.Outputs[i].Path {
				bad = append(bad, fmt.Sprintf("%s output %s: got %s want %s", path, o.Name, o.Path, orig.Outputs[i].Path))
			}
		}
		if got, err := cp.Path(); err != nil || got != path {
			bad = append(bad, fmt.Sprintf("%s: recomputed drv path %s (%v)", path, got, err))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		if len(bad) > 10 {
			bad = bad[:10]
		}
		return fmt.Errorf("%d paths differ, e.g.\n%s", len(bad), strings.Join(bad, "\n"))
	}
	if got := c[e.DrvPath]; got == nil || len(got.Outputs) == 0 {
		return fmt.Errorf("hello has no outputs")
	}
	return nil
}

var (
	revMu    sync.Mutex
	revCache = map[string]string{}
)

// fullRev resolves a short nixpkgs commit id to the full revision through the GitHub API.
func fullRev(t testing.TB, short string) string {
	t.Helper()
	revMu.Lock()
	defer revMu.Unlock()
	if r, ok := revCache[short]; ok {
		return r
	}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/NixOS/nixpkgs/commits/"+short, nil)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	defer resp.Body.Close()
	var c struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil || !strings.HasPrefix(c.SHA, short) {
		t.Fatalf("harness: cannot resolve nixpkgs %s through the GitHub API (HTTP %d, %v)", short, resp.StatusCode, err)
	}
	revCache[short] = c.SHA
	return c.SHA
}

// nixpkgsRev is the full revision of the pin R-NIX-005 names (nixos-26.05, cd4ea8d5…).
func nixpkgsRev(t testing.TB) string { return fullRev(t, "cd4ea8d5") }
