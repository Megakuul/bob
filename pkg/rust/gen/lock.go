package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// indexEnv overrides the crates.io sparse index (a mirror; the harness points it at
// tools/regmirror). bob.lock still records crates.io: the mirror serves the same crates.
const indexEnv = "BOB_CRATES_INDEX"

// maxCrate caps one .crate download (crates.io allows 10 MiB; mirrors may be larger).
const maxCrate = 256 << 20

// fetchParallel bounds the parallel crate downloads of bob lock.
const fetchParallel = 8

// indexURL is the sparse index bob resolves against.
func indexURL() string {
	if u := strings.TrimSpace(os.Getenv(indexEnv)); u != "" {
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		return u
	}
	return resolve.CratesIO
}

// cacheDir is the index cache: $XDG_CACHE_HOME/bob/crates-index, one subdirectory per mirror.
func cacheDir(url string) string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return ""
		}
		base = filepath.Join(h, ".cache")
	}
	dir := filepath.Join(base, "bob", "crates-index")
	if url != resolve.CratesIO {
		sum := sha256.Sum256([]byte(url))
		dir = filepath.Join(dir, "mirror-"+hex.EncodeToString(sum[:6]))
	}
	return dir
}

// lockState is the input of Lock and Stale: the workspace and its declaration hash.
type lockState struct {
	ws      *workspace
	members []resolve.Member
	inputs  string
}

func readLockState(g *ir.Graph) (*lockState, error) {
	ws, err := readWorkspace(g)
	if err != nil {
		return nil, err
	}
	members, err := ws.resolveMembers()
	if err != nil {
		return nil, err
	}
	inputs, err := lock.Inputs(declare(members, toolchainAttrs(g, ws)))
	if err != nil {
		return nil, err
	}
	return &lockState{ws: ws, members: members, inputs: inputs}, nil
}

// toolchainAttrs names the rustc attributes of the members ("rustc", "rust_1_94"), sorted:
// the resolution prefers crates whose rust-version they satisfy.
func toolchainAttrs(g *ir.Graph, ws *workspace) string {
	ids := map[string]*ir.Node{}
	for _, n := range g.Nodes {
		ids[n.ID] = n
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range ws.names {
		m := ws.members[name]
		if m.spec.Toolchain < 0 || m.spec.Toolchain >= len(m.node.Deps) {
			continue
		}
		if n := ids[m.node.Deps[m.spec.Toolchain]]; n != nil && n.Nix != nil && !seen[n.Nix.Attr] {
			seen[n.Nix.Attr] = true
			out = append(out, n.Nix.Attr)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// rustVersion is the lowest version of the members' toolchains ("" when unknown: then the
// resolution ignores rust-version, like Cargo's resolver 2).
func rustVersion(ctx context.Context, req engine.LockRequest, attrs string) string {
	if req.Nixpkgs == nil || attrs == "" {
		return ""
	}
	es, err := req.Nixpkgs.Lookup(ctx, strings.Split(attrs, ","))
	if err != nil {
		return ""
	}
	low := ""
	var lowV resolve.Version
	for _, e := range es {
		v, err := resolve.ParseVersion(e.Version)
		if err != nil {
			continue
		}
		if low == "" || v.Compare(lowV) < 0 {
			low, lowV = e.Version, v
		}
	}
	return low
}

// Lock resolves the crates of every local crate (bob lock): Cargo's lockfile resolution against
// the sparse index, then each crate is downloaded once, its checksum verified, its source
// imported (narHash) and its Cargo.toml facts read (meta). A section whose inputs are unchanged
// is returned as it is, without any request (R-FW-062).
func (Ecosystem) Lock(ctx context.Context, req engine.LockRequest) (json.RawMessage, error) {
	st, err := readLockState(req.Graph)
	if err != nil {
		return nil, err
	}
	var prev *lock.RustSection
	if len(req.Prev) > 0 {
		if v, err := lock.DecodeSection(lock.SectionRust, req.Prev); err == nil {
			prev = v.(*lock.RustSection)
		}
	}
	if prev != nil && prev.Inputs == st.inputs && len(req.Update) == 0 {
		return lock.EncodeSection(lock.SectionRust, prev)
	}
	sec := &lock.RustSection{Inputs: st.inputs, Index: resolve.IndexURL, Crates: []lock.Crate{}}
	if len(st.members) == 0 {
		return lock.EncodeSection(lock.SectionRust, sec)
	}
	url := indexURL()
	ix := resolve.NewIndex(url, cacheDir(url))
	ix.Client = &http.Client{Timeout: 60 * time.Second}
	res, err := resolve.Resolve(ctx, ix, st.members, resolve.Options{RustVersion: rustVersion(ctx, req, toolchainAttrs(req.Graph, st.ws)),
		Locked: keptPins(prev, req.Update)})
	if err != nil {
		return nil, resolveError(st, err)
	}
	records, err := res.Records()
	if err != nil {
		return nil, err
	}
	dl, err := downloadTemplate(ctx, ix.Client, url)
	if err != nil {
		return nil, err
	}
	old := map[string]lock.Crate{}
	if prev != nil {
		for _, c := range prev.Crates {
			old[c.Name+"@"+c.Version] = c
		}
	}
	crates := make([]lock.Crate, len(records))
	errs := make([]error, len(records))
	sem := make(chan struct{}, fetchParallel)
	var wg sync.WaitGroup
	for i := range records {
		r := records[i]
		c, err := toLockCrate(r)
		if err != nil {
			return nil, err
		}
		if o, ok := old[r.Name+"@"+r.Version]; ok && o.Checksum == c.Checksum && o.NarHash != "" && o.Meta.Edition != "" {
			c.NarHash, c.Meta = o.NarHash, o.Meta
			crates[i] = c
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if v := recover(); v != nil {
					errs[i] = fmt.Errorf("crate %s@%s: internal error: %v", r.Name, r.Version, v)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = fetchCrate(ctx, req.Fetch, ix.Client, dl, &c)
			crates[i] = c
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sec.Crates = crates
	return lock.EncodeSection(lock.SectionRust, sec)
}

// keptPins are the locked crates a re-resolution prefers (R-CLI-010: bob lock never moves an
// existing pin unless asked): every previous crate without --update, none with --update of
// everything or of "rust", all but the named crates ("serde", "crates.io:serde",
// "serde@1.0.200") otherwise.
func keptPins(prev *lock.RustSection, update []string) []string {
	if prev == nil || update != nil && len(update) == 0 {
		return nil
	}
	moved := map[string]bool{}
	for _, u := range update {
		if u == lock.SectionRust {
			return nil
		}
		moved[strings.TrimPrefix(u, "crates.io:")] = true
	}
	var out []string
	for _, c := range prev.Crates {
		if !moved[c.Name] && !moved[c.Name+"@"+c.Version] {
			out = append(out, c.Name+"@"+c.Version)
		}
	}
	return out
}

// resolveError is BOB-E-1015 naming the first workspace crate.
func resolveError(st *lockState, err error) error {
	if !errors.Is(err, resolve.ErrUnresolvable) {
		return fmt.Errorf("resolving crates: %w", err)
	}
	root := "the Rust crates"
	var at ir.Pos
	if len(st.ws.names) > 0 {
		m := st.ws.members[st.ws.names[0]]
		root, at = m.node.Label, m.node.DefinedAt
	}
	reason := strings.TrimPrefix(err.Error(), resolve.ErrUnresolvable.Error()+": ")
	e := diag.New(1015, "ecosystem", "Rust", "root", root, "reason", reason, "label", root)
	e.DefinedAt = diag.Pos{File: at.File, Line: at.Line}
	return e
}

// toLockCrate converts a resolver record (same JSON shape) to lock.Crate.
func toLockCrate(r resolve.Crate) (lock.Crate, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return lock.Crate{}, err
	}
	var c lock.Crate
	if err := json.Unmarshal(b, &c); err != nil {
		return lock.Crate{}, err
	}
	return c, nil
}

// fromLockCrates converts bob.lock's crates to resolver records.
func fromLockCrates(cs []lock.Crate) ([]resolve.Crate, error) {
	b, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	var out []resolve.Crate
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// dlTemplate is the index's crate download rule (config.json "dl").
type dlTemplate string

const cratesIODL = dlTemplate("https://static.crates.io/crates/{crate}/{crate}-{version}.crate")

// downloadTemplate reads config.json of the index (crates.io's is known).
func downloadTemplate(ctx context.Context, c *http.Client, index string) (dlTemplate, error) {
	if index == resolve.CratesIO {
		return cratesIODL, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, index+"config.json", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("crates index %s: %w", index, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("crates index %sconfig.json: %s", index, resp.Status)
	}
	var cfg struct {
		DL string `json:"dl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cfg); err != nil || cfg.DL == "" {
		return "", fmt.Errorf("crates index %sconfig.json has no dl", index)
	}
	return dlTemplate(cfg.DL), nil
}

// url is the download URL of name@version (Cargo's registry index rules).
func (t dlTemplate) url(name, version, hexSum string) string {
	s := string(t)
	markers := []string{"{crate}", "{version}", "{prefix}", "{lowerprefix}", "{sha256-checksum}"}
	has := false
	for _, m := range markers {
		if strings.Contains(s, m) {
			has = true
		}
	}
	if !has {
		return strings.TrimSuffix(s, "/") + "/" + name + "/" + version + "/download"
	}
	prefix := strings.TrimSuffix(resolve.IndexPath(name), "/"+strings.ToLower(name))
	r := strings.NewReplacer("{crate}", name, "{version}", version, "{prefix}", prefix,
		"{lowerprefix}", strings.ToLower(prefix), "{sha256-checksum}", hexSum)
	return r.Replace(s)
}

// sourceName is the store name of a crate's unpacked source.
func sourceName(name, version string) string { return drv.Name("crate", name, version) }

// fetchCrate downloads c once for its Cargo.toml facts, then lets the fetcher import the
// unpacked source and report its NAR hash.
func fetchCrate(ctx context.Context, f fetch.Fetcher, client *http.Client, dl dlTemplate, c *lock.Crate) error {
	hexSum, err := resolve.HexOfSRI(c.Checksum)
	if err != nil {
		return err
	}
	u := dl.url(c.Name, c.Version, hexSum)
	body, err := download(ctx, client, u)
	if err != nil {
		return fmt.Errorf("crates.io:%s@%s: %w", c.Name, c.Version, err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hexSum {
		e := diag.New(3001, "label", "crates.io:"+c.Name+"@"+c.Version)
		e.Args["url"], e.Args["expected"], e.Args["got"] = u, c.Checksum, "sha256:"+hex.EncodeToString(sum[:])
		return e
	}
	meta, err := resolve.MetaFromCrate(c.Name, c.Version, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("crates.io:%s@%s: %w", c.Name, c.Version, err)
	}
	c.Meta = lock.CrateMeta{Edition: meta.Edition, LibName: meta.LibName, LibPath: meta.LibPath, ProcMacro: meta.ProcMacro, Build: meta.Build}
	if f == nil {
		return fmt.Errorf("crates.io:%s@%s: no fetcher", c.Name, c.Version)
	}
	_, narHash, err := f.Resolve(ctx, fetch.Request{URLs: []string{u}, Kind: fetch.Archive, Hash: c.Checksum,
		Name: sourceName(c.Name, c.Version), Strip: 1, Label: "crates.io:" + c.Name + "@" + c.Version})
	if err != nil {
		return err
	}
	c.NarHash = narHash
	return nil
}

func download(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			last = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxCrate+1))
		_ = resp.Body.Close()
		switch {
		case err != nil:
			last = err
			continue
		case resp.StatusCode >= 500:
			last = fmt.Errorf("%s: %s", u, resp.Status)
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		case len(b) > maxCrate:
			return nil, fmt.Errorf("%s is larger than %d bytes", u, maxCrate)
		}
		return b, nil
	}
	return nil, last
}

// Stale reports whether the rust section still resolves the crates of the graph (R-FW-062).
// Equal inputs mean fresh. Otherwise the graph's crates (a build of part of the repository sees
// only part of them) are resolved against the locked crates alone: the section is stale exactly
// when that fails, i.e. when a declaration needs a crate, version or feature bob.lock lacks.
func (Ecosystem) Stale(ctx context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	st, err := readLockState(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	v, err := lock.DecodeSection(lock.SectionRust, req.Prev)
	if err != nil {
		return "the rust section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	sec := v.(*lock.RustSection)
	if sec.Inputs == st.inputs || len(st.members) == 0 {
		return "", ir.Pos{}, nil
	}
	records, err := fromLockCrates(sec.Crates)
	if err != nil {
		return "the rust section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	reg, err := lockedRegistry(records)
	if err != nil {
		return "the rust section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	if _, err := resolve.Resolve(ctx, reg, st.members, resolve.Options{}); err == nil {
		return "", ir.Pos{}, nil
	} else if ctx.Err() != nil {
		return "", ir.Pos{}, err
	}
	// Name the first crate whose own requirement bob.lock does not hold, else the first crate.
	byName := map[string][]*resolve.Crate{}
	for i := range records {
		byName[records[i].Name] = append(byName[records[i].Name], &records[i])
	}
	for _, m := range st.members {
		for _, d := range m.Deps {
			if !d.Local && resolve.MatchLocked(byName[d.Package], d.Req) == nil {
				n := st.ws.members[m.Name].node
				return fmt.Sprintf("Rust crate %s requires %s %s, which is not locked", n.Label, d.Package, d.Req), n.DefinedAt, nil
			}
		}
	}
	n := st.ws.members[st.ws.names[0]].node
	return "the Rust crate declarations need crates or features bob.lock does not hold", n.DefinedAt, nil
}

// lockedRegistry is a registry of exactly the locked crates: every dependency requires the
// version it was resolved to.
func lockedRegistry(records []resolve.Crate) (resolve.MemRegistry, error) {
	reg := resolve.MemRegistry{}
	for _, c := range records {
		hexSum, err := resolve.HexOfSRI(c.Checksum)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", c.Name, c.Version, err)
		}
		ie := resolve.IndexEntry{Name: c.Name, Vers: c.Version, Cksum: hexSum, Features: c.Features, Links: c.Links}
		for _, d := range c.Deps {
			n, v, _ := strings.Cut(d.Crate, "@")
			def := !d.NoDefault
			ie.Deps = append(ie.Deps, resolve.IndexDep{Name: d.Name, Req: "=" + v, Features: d.Features, Optional: d.Optional,
				DefaultFeatures: &def, Target: d.Target, Kind: d.Kind, Package: n})
		}
		e, err := resolve.NewEntry(&ie)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(c.Name)
		reg[key] = append(reg[key], e)
	}
	return reg, nil
}
