package nixidx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/store"
)

//go:embed resolve.nix
var resolveNix []byte

// indexVersion is part of the cache key: a change of resolve.nix or of the cache layout
// invalidates every cache file.
const indexVersion = 1

// maxOutput caps what one resolve.nix run may print (the names list of nixpkgs is ~1 MB).
const maxOutput = 64 << 20

// Config selects the nixpkgs the index answers for.
type Config struct {
	Source            string // store path (or directory) of the nixpkgs source tree
	Rev               string
	System            string // Nix system, "aarch64-linux"
	AllowUnfree       bool
	PermittedInsecure []string
}

// Entry is the data resolve.nix returns for one attribute (SPEC R-NIX-027).
type Entry struct {
	Missing              bool              `json:"missing"`
	Broken               bool              `json:"broken,omitempty"`
	Insecure             bool              `json:"insecure,omitempty"`
	KnownVulnerabilities []string          `json:"knownVulnerabilities,omitempty"`
	Unfree               bool              `json:"unfree,omitempty"`
	Available            bool              `json:"available,omitempty"`
	Version              string            `json:"version,omitempty"`
	DrvPath              string            `json:"drvPath,omitempty"`
	Outputs              map[string]string `json:"outputs,omitempty"` // output name → store path
	EvalRefused          string            `json:"evalRefused,omitempty"`
}

// Index is the nixpkgs lookup of one (rev, system). It is safe for concurrent use.
type Index struct {
	st        *store.Store
	cfg       Config
	cachePath string
	script    string // resolve.nix on disk

	mu      sync.Mutex
	cache   cacheFile
	checked map[string]bool // drvPaths found valid in this run
	cached  map[string]bool // Cached answers of this run
	subs    []substituter
}

// cacheFile is <home>/cache/nixpkgs/<rev>-<system>.json.
type cacheFile struct {
	Version int               `json:"version"`
	Rev     string            `json:"rev"`
	System  string            `json:"system"`
	Allow   string            `json:"allow"` // hash of AllowUnfree/PermittedInsecure the entries were evaluated with
	Names   []string          `json:"names,omitempty"`
	Attrs   map[string]Entry  `json:"attrs"`
	Modulo  map[string]string `json:"modulo,omitempty"` // drv path → hex hashDerivationModulo
}

var (
	revRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	systemRe = regexp.MustCompile(`^(aarch64|x86_64)-linux$`)
	attrRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_'+-]*(\.[A-Za-z0-9_][A-Za-z0-9_'+-]*)*$`)
	pathRe   = regexp.MustCompile(`^/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`)
)

// Open opens the index; results are cached in <home>/cache/nixpkgs/<rev>-<system>.json.
func Open(ctx context.Context, st *store.Store, c Config) (*Index, error) {
	if st == nil {
		return nil, errors.New("nixidx: no store")
	}
	if !revRe.MatchString(c.Rev) || !systemRe.MatchString(c.System) || c.Source == "" || !filepath.IsAbs(c.Source) {
		return nil, fmt.Errorf("nixidx: invalid config (rev %q, system %q, source %q)", c.Rev, c.System, c.Source)
	}
	for _, s := range c.PermittedInsecure {
		if s == "" || len(s) > 256 || strings.ContainsAny(s, "\x00\n") {
			return nil, fmt.Errorf("nixidx: invalid permitted insecure entry %q", s)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(st.Home(), "cache", "nixpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("nixidx: %w", err)
	}
	sum := sha256.Sum256(resolveNix)
	script := filepath.Join(dir, "resolve-"+hex.EncodeToString(sum[:6])+".nix")
	if b, err := os.ReadFile(script); err != nil || !bytes.Equal(b, resolveNix) {
		if err := writeAtomic(script, resolveNix); err != nil {
			return nil, err
		}
	}
	idx := &Index{st: st, cfg: c, cachePath: filepath.Join(dir, c.Rev+"-"+c.System+".json"), script: script,
		checked: map[string]bool{}, cached: map[string]bool{}}
	idx.cache = idx.load()
	return idx, nil
}

// allowKey identifies the allow settings entries depend on.
func (c Config) allowKey() string {
	ins := slices.Clone(c.PermittedInsecure)
	sort.Strings(ins)
	b, _ := json.Marshal(struct {
		U bool
		I []string
	}{c.AllowUnfree, ins})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// load reads the cache file; a missing, unreadable or foreign file gives an empty cache.
func (i *Index) load() cacheFile {
	empty := cacheFile{Version: indexVersion, Rev: i.cfg.Rev, System: i.cfg.System, Allow: i.cfg.allowKey(), Attrs: map[string]Entry{}}
	b, err := readCapped(i.cachePath)
	if err != nil {
		return empty
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil || c.Version != indexVersion || c.Rev != i.cfg.Rev || c.System != i.cfg.System {
		return empty
	}
	if c.Attrs == nil || c.Allow != empty.Allow {
		c.Attrs, c.Allow = map[string]Entry{}, empty.Allow
	}
	for a, e := range c.Attrs {
		if !attrRe.MatchString(a) || !e.valid() {
			delete(c.Attrs, a)
		}
	}
	return c
}

// save writes the cache file atomically (concurrent bob processes: last writer wins, a lost
// entry is only re-queried).
func (i *Index) save() error {
	b, err := json.Marshal(i.cache)
	if err != nil {
		return fmt.Errorf("nixidx: %w", err)
	}
	return writeAtomic(i.cachePath, b)
}

// valid checks the shape of an entry read from disk or from resolve.nix.
func (e Entry) valid() bool {
	if e.DrvPath != "" && (!pathRe.MatchString(e.DrvPath) || !strings.HasSuffix(e.DrvPath, ".drv")) {
		return false
	}
	for name, p := range e.Outputs {
		if name == "" || len(name) > 64 || !pathRe.MatchString(p) {
			return false
		}
	}
	return len(e.Version) <= 256
}

// Lookup resolves attrs. An attribute that cannot be evaluated is isolated by a per-attribute
// retry and reported as BOB-E-1017 in the returned error (a *diag.Error); the other attributes
// are still answered.
func (i *Index) Lookup(ctx context.Context, attrs []string) (map[string]Entry, error) {
	want := slices.Clone(attrs)
	sort.Strings(want)
	want = slices.Compact(want)
	for _, a := range want {
		if len(a) > 256 || !attrRe.MatchString(a) {
			return nil, fmt.Errorf("nixidx: invalid attribute name %q", a)
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.revalidate(ctx, want); err != nil {
		return nil, err
	}
	var todo []string
	for _, a := range want {
		if _, ok := i.cache.Attrs[a]; !ok {
			todo = append(todo, a)
		}
	}
	var errs []error
	if len(todo) > 0 || i.cache.Names == nil && len(want) > 0 {
		got, names, bad, err := i.resolve(ctx, todo, i.cache.Names == nil)
		if err != nil {
			return nil, err
		}
		for a, e := range got {
			i.cache.Attrs[a] = e
			if e.DrvPath != "" {
				i.checked[e.DrvPath] = true
			}
		}
		if names != nil {
			i.cache.Names = names
		}
		errs = bad
		if err := i.save(); err != nil {
			return nil, err
		}
	}
	out := make(map[string]Entry, len(want))
	for _, a := range want {
		if e, ok := i.cache.Attrs[a]; ok {
			out[a] = e
		}
	}
	return out, errors.Join(errs...)
}

// revalidate drops cached entries whose drvPath no longer exists in the store (garbage
// collected), so they are queried again. A stat of the file under <home>/nix/store stands in for
// a store query: it costs microseconds where a nix-store process costs tens of milliseconds
// (warm index budget 50 ms, R-NIX-027). Each path is checked once per Index.
func (i *Index) revalidate(_ context.Context, attrs []string) error {
	storeDir := filepath.Join(i.st.Home(), "nix", "store")
	for _, a := range attrs {
		e, ok := i.cache.Attrs[a]
		if !ok || e.DrvPath == "" || i.checked[e.DrvPath] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(storeDir, filepath.Base(e.DrvPath))); err != nil {
			delete(i.cache.Attrs, a)
			continue
		}
		i.checked[e.DrvPath] = true
	}
	return nil
}

// resolve runs resolve.nix for attrs. When the batch evaluation fails, the batch is split in
// halves until the failing attributes are isolated; each is reported as BOB-E-1017.
func (i *Index) resolve(ctx context.Context, attrs []string, names bool) (map[string]Entry, []string, []error, error) {
	got, nm, raw, err := i.eval(ctx, attrs, names)
	if err != nil {
		return nil, nil, nil, err
	}
	if got != nil {
		return got, nm, nil, nil
	}
	if names {
		// Names do not depend on the attributes: get them on their own first.
		if _, nm, raw, err = i.eval(ctx, nil, true); err != nil {
			return nil, nil, nil, err
		}
		if nm == nil {
			return nil, nil, nil, i.internal(raw)
		}
	}
	if len(attrs) <= 1 {
		if len(attrs) == 0 {
			return map[string]Entry{}, nm, nil, nil
		}
		return map[string]Entry{}, nm, []error{i.refused(attrs[0], raw)}, nil
	}
	mid := len(attrs) / 2
	out := map[string]Entry{}
	var bad []error
	for _, half := range [][]string{attrs[:mid], attrs[mid:]} {
		g, _, b, err := i.resolve(ctx, half, false)
		if err != nil {
			return nil, nil, nil, err
		}
		maps.Copy(out, g)
		bad = append(bad, b...)
	}
	return out, nm, bad, nil
}

// result is resolve.nix's output; evalRefused is a boolean there.
type result struct {
	Attrs map[string]struct {
		Entry
		EvalRefused bool `json:"evalRefused"`
	} `json:"attrs"`
	Names []string `json:"names"`
}

// eval runs resolve.nix once. A failed evaluation gives nil maps and Nix's stderr (raw); err is
// reserved for failures to run at all (context, store).
func (i *Index) eval(ctx context.Context, attrs []string, names bool) (map[string]Entry, []string, string, error) {
	aj, _ := json.Marshal(nonNil(attrs))
	ins := nonNil(slices.Sorted(slices.Values(i.cfg.PermittedInsecure)))
	ij, _ := json.Marshal(ins)
	var stdout capped
	var stderr capped
	stdout.max, stderr.max = maxOutput, 1<<20
	cmd := i.st.Command(ctx, "nix", "eval", "--json", "--file", i.script,
		"--argstr", "src", i.cfg.Source,
		"--argstr", "system", i.cfg.System,
		"--argstr", "attrs", string(aj),
		"--argstr", "names", fmt.Sprint(names),
		"--argstr", "allowUnfree", fmt.Sprint(i.cfg.AllowUnfree),
		"--argstr", "permittedInsecure", string(ij),
		"result")
	cmd.Env = []string{"NIX_PATH=", "NIXPKGS_ALLOW_UNFREE=", "NIXPKGS_ALLOW_INSECURE=", "NIXPKGS_ALLOW_BROKEN="}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, nil, "", ctx.Err()
	}
	if runErr != nil {
		var ee interface{ ExitCode() int }
		if !errors.As(runErr, &ee) {
			return nil, nil, "", fmt.Errorf("nixidx: running the nixpkgs index: %w", runErr)
		}
		return nil, nil, stderr.String(), nil
	}
	if stdout.over {
		return nil, nil, "", i.internal("the nixpkgs index printed more than 64 MiB")
	}
	var r result
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		return nil, nil, "", i.internal("unexpected index output: " + err.Error())
	}
	out := make(map[string]Entry, len(r.Attrs))
	for _, a := range attrs {
		v, ok := r.Attrs[a]
		if !ok {
			return nil, nil, "", i.internal("the index did not answer " + a)
		}
		e := v.Entry
		if v.EvalRefused {
			e.EvalRefused = refusalReason(e)
		}
		if !e.valid() {
			return nil, nil, "", i.internal("the index answered " + a + " with invalid paths")
		}
		out[a] = e
	}
	return out, r.Names, "", nil
}

// refusalReason explains why nixpkgs refused to evaluate a package's derivation.
func refusalReason(e Entry) string {
	switch {
	case e.Broken:
		return "broken"
	case e.Insecure:
		return "insecure"
	case e.Unfree:
		return "unfree"
	case !e.Available:
		return "unavailable"
	}
	return "refused by nixpkgs"
}

// refused is BOB-E-1017 for an attribute whose evaluation aborts; Nix's text stays in Raw
// (shown only with -v), the abort message (if any) is the cause.
func (i *Index) refused(attr, raw string) error {
	e := diag.New(1017, "attr", attr, "system", i.cfg.System)
	if m := abortRe.FindStringSubmatch(raw); m != nil {
		e.Cause = m[1]
	}
	e.Raw = rawLines(raw)
	return e
}

var abortRe = regexp.MustCompile(`evaluation aborted with the following error message: '([^'\n]{1,500})'`)

// internal is BOB-E-9999: resolve.nix produced something bob does not understand.
func (i *Index) internal(raw string) error {
	e := diag.New(9999)
	e.Cause = "the nixpkgs index returned an unexpected result"
	e.Raw = rawLines(raw)
	return e
}

func rawLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// Names returns the top-level attribute names (for suggestions).
func (i *Index) Names(ctx context.Context) ([]string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cache.Names == nil {
		_, nm, raw, err := i.eval(ctx, nil, true)
		if err != nil {
			return nil, err
		}
		if nm == nil {
			return nil, i.internal(raw)
		}
		i.cache.Names = nm
		if err := i.save(); err != nil {
			return nil, err
		}
	}
	return slices.Clone(i.cache.Names), nil
}

// Modulo returns the recorded hashDerivationModulo (hex) of a parsed nixpkgs derivation.
func (i *Index) Modulo(drvPath string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	h, ok := i.cache.Modulo[drvPath]
	return h, ok
}

// RecordModulo stores modulo hashes of parsed nixpkgs derivations (hex) in the index cache.
func (i *Index) RecordModulo(hashes map[string]string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	changed := false
	for p, h := range hashes {
		if !pathRe.MatchString(p) || len(h) != 64 {
			continue
		}
		if i.cache.Modulo == nil {
			i.cache.Modulo = map[string]string{}
		}
		if i.cache.Modulo[p] != h {
			i.cache.Modulo[p], changed = h, true
		}
	}
	if !changed {
		return nil
	}
	return i.save()
}

// substituter is one configured binary cache with its netrc credentials.
type substituter struct {
	url        *url.URL
	user, pass string
}

// Cached reports whether outPath is available from a configured substituter: one HEAD
// <substituter>/<hash>.narinfo per substituter until one has it, remembered for the run. A
// substituter that cannot be reached counts as not having it (and is asked again next time).
func (i *Index) Cached(ctx context.Context, outPath string) (bool, error) {
	if !pathRe.MatchString(outPath) {
		return false, fmt.Errorf("nixidx: %q is not a store path", outPath)
	}
	hash := strings.TrimPrefix(outPath, "/nix/store/")[:32]
	i.mu.Lock()
	if v, ok := i.cached[hash]; ok {
		i.mu.Unlock()
		return v, nil
	}
	if i.subs == nil {
		i.subs = substituters(i.st.Home())
	}
	subs := i.subs
	i.mu.Unlock()

	client := &http.Client{Timeout: 10 * time.Second}
	sure := true
	for _, s := range subs {
		ok, err := hasNarinfo(ctx, client, s, hash)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			sure = false
			continue
		}
		if ok {
			i.mu.Lock()
			i.cached[hash] = true
			i.mu.Unlock()
			return true, nil
		}
	}
	if sure {
		i.mu.Lock()
		i.cached[hash] = false
		i.mu.Unlock()
	}
	return false, nil
}

func hasNarinfo(ctx context.Context, client *http.Client, s substituter, hash string) (bool, error) {
	u := *s.url
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + hash + ".narinfo"
	if u.Scheme == "file" {
		_, err := os.Stat(u.Path)
		return err == nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return false, err
	}
	if s.user != "" || s.pass != "" {
		req.SetBasicAuth(s.user, s.pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return false, nil
	}
	return false, fmt.Errorf("%s: %s", u.Host, resp.Status)
}

// substituters reads the substituters (and netrc credentials) bob's store was configured with
// from the nix.conf and netrc the store writes (R-NIX-024).
func substituters(home string) []substituter {
	creds := map[string][2]string{}
	if b, err := readCapped(filepath.Join(home, "etc", "nix", "netrc")); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 6 && f[0] == "machine" && f[2] == "login" && f[4] == "password" {
				creds[f[1]] = [2]string{f[3], f[5]}
			}
		}
	}
	b, err := readCapped(filepath.Join(home, "etc", "nix", "nix.conf"))
	if err != nil {
		return []substituter{}
	}
	out := []substituter{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || strings.TrimSpace(k) != "substituters" {
			continue
		}
		for f := range strings.FieldsSeq(v) {
			u, err := url.Parse(f)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "file") {
				continue
			}
			c := creds[u.Hostname()]
			out = append(out, substituter{url: u, user: c[0], pass: c[1]})
		}
	}
	return out
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxOutput+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxOutput {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxOutput)
	}
	return b, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("nixidx: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("nixidx: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("nixidx: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("nixidx: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("nixidx: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// capped is a bytes.Buffer that keeps at most max bytes and notes an overflow.
type capped struct {
	bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); len(p) > room {
		c.over = true
		if room > 0 {
			c.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return c.Buffer.Write(p)
}
