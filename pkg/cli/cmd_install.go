package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/cli/remote"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// cache.nixos.org and its key: the paths of a release closure that the registry's cache does
// not serve come from there (SPEC R-INST-001; same values as the store's default, R-NIX-024).
const (
	nixosCache    = "https://cache.nixos.org"
	nixosCacheKey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
)

// installRef is `[<registry>:]<project>/<release>[@<version>|@latest]` (SPEC §02.8).
type installRef struct {
	registry, project, release, version string
}

// name is the entry name and ref of a generation entry: "<project>/<release>".
func (r installRef) name() string { return r.project + "/" + r.release }

func parseRef(s string) (installRef, error) {
	bad := diag.New(4, "flag", "bob install "+s, "what", "a reference of the form [<registry>:]<project>/<release>[@<version>]")
	var r installRef
	rest := s
	if reg, after, ok := strings.Cut(rest, ":"); ok {
		r.registry, rest = reg, after
		if !nameSlug().MatchString(reg) {
			return r, bad
		}
	}
	if before, v, ok := strings.Cut(rest, "@"); ok {
		rest, r.version = before, v
		if v == "" || len(v) > 64 || strings.ContainsAny(v, " /\t") {
			return r, bad
		}
		if v == "latest" {
			r.version = ""
		}
	}
	p, rel, ok := strings.Cut(rest, "/")
	if !ok || !nameSlug().MatchString(p) || !nameSlug().MatchString(rel) {
		return r, bad
	}
	r.project, r.release = p, rel
	return r, nil
}

// hostPlatform is bob's name of the host platform (linux-amd64, linux-arm64).
func hostPlatform() string { return "linux-" + runtime.GOARCH }

// hintedError is a diagnostic with a hint other than its code's default.
type hintedError struct {
	d    *diag.Error
	hint string
}

func (h *hintedError) Error() string { return h.d.Error() }
func (h *hintedError) Unwrap() error { return h.d }

// failHinted reports err like out.fail, keeping the hint of a hintedError.
func failHinted(e *env, err error) int {
	var h *hintedError
	if errors.As(err, &h) {
		e.out.diagnose(h.d, h.hint)
		return e.out.finish(-1, translate.Counts{}, nil)
	}
	return e.out.fail(err)
}

// installer resolves and downloads releases for one command; the store is opened on first use.
type installer struct {
	e  *env
	p  *profile
	st *store.Store
}

// resolution is a release resolved on its registry, not yet downloaded.
type resolution struct {
	reg   registry
	ref   installRef
	text  string // the reference as the user wrote it (for messages)
	res   *remote.Resolved
	cache *remote.Cache
}

// resolve checks the credentials for the registry (R-NIX-029: AuthService/WhoAmI before any
// download) and asks it for the release. Without credentials every request is anonymous.
func (in *installer) resolve(reg registry, ref installRef, constraint, platform, text string) (*resolution, error) {
	ctx := in.e.ctx
	tok, hasTok, err := in.e.token(ctx, reg.URL)
	if err != nil {
		return nil, err
	}
	api := in.e.api(reg.URL, tok)
	if hasTok {
		if _, err := api.WhoAmI(ctx); err != nil {
			return nil, apiDiag(reg.URL, remote.WhoAmIMethod, ref.project, err)
		}
	}
	res, err := api.ResolveRelease(ctx, ref.project, ref.release, constraint, platform)
	if err != nil {
		return nil, resolveDiag(reg, ref, text, hasTok, err)
	}
	return &resolution{reg: reg, ref: ref, text: text, res: res, cache: &remote.Cache{
		URL: cacheURL(reg.URL, res.Substituters), Token: tok, Keys: reg.PublicKeys, UserAgent: "bob/" + bobVersion(),
	}}, nil
}

// resolveDiag maps a failed ResolveRelease. NotFound without credentials may be a private
// release (the server does not tell, R-INST-003): BOB-E-4010 with a hint naming bob login.
func resolveDiag(reg registry, ref installRef, text string, hasTok bool, err error) error {
	re, ok := remote.AsError(err)
	if ok && re.Code == "not_found" {
		if !hasTok {
			d := diag.New(4010, "server", remote.Host(reg.URL), "action", "ResolveRelease", "project", ref.project,
				"permission", remote.ResolveReleaseMethod)
			d.Raw = []string{re.Error()}
			return &hintedError{d, fmt.Sprintf("%s is private or does not exist; log in with bob login %s (or set BOB_TOKEN)", text, reg.URL)}
		}
		d := diag.New(4011, "ref", text, "registry", reg.Name, "project", ref.project)
		d.Raw = []string{re.Error()}
		return d
	}
	return apiDiag(reg.URL, remote.ResolveReleaseMethod, ref.project, err)
}

// cacheURL is the registry's /nix cache: the substituter ResolveRelease names when it is on
// the registry's own host, else <registry>/nix. The token is never sent to another host.
func cacheURL(reg string, subs []string) string {
	ru, err := url.Parse(reg)
	for _, s := range subs {
		su, serr := url.Parse(s)
		if err == nil && serr == nil && su.Scheme == ru.Scheme && su.Host == ru.Host && su.RawQuery == "" && !strings.ContainsAny(s, " \t\r\n") {
			return strings.TrimRight(s, "/")
		}
	}
	return reg + "/nix"
}

// nothingToInstall is BOB-E-0009 for a release without a closure for the platform.
func (r *resolution) nothingToInstall(platform string) error {
	var kinds []string
	for _, c := range r.res.Release.Closures {
		kinds = append(kinds, fmt.Sprintf("closure for %s", c.Platform))
	}
	for _, im := range r.res.Release.Images {
		n := im.Repository
		if i := strings.LastIndexByte(n, '/'); i >= 0 {
			n = n[i+1:]
		}
		kinds = append(kinds, fmt.Sprintf("oci image %q", n))
	}
	for _, a := range r.res.Release.Assets {
		k := strings.ToLower(strings.TrimPrefix(a.Kind, "ASSET_KIND_"))
		kinds = append(kinds, fmt.Sprintf("%s %q", k, a.File))
	}
	if len(kinds) == 0 {
		kinds = []string{"no artifacts"}
	}
	v := r.ref.version
	if v == "" {
		v = r.res.Release.Version
	}
	u := fmt.Sprintf("%s/p/%s/releases/%s/%s", r.reg.URL, r.ref.project, r.ref.release, v)
	return diag.New(9, "ref", r.text, "kinds", strings.Join(kinds, ", "), "url", u)
}

// store opens bob's store with the same nix.conf as every other command.
func (in *installer) store() (*store.Store, error) {
	if in.st != nil {
		return in.st, nil
	}
	c, err := in.e.loadCreds()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(in.e.ctx, store.Config{Substituters: in.e.substituters(c, nil)})
	if err != nil {
		return nil, err
	}
	in.st = st
	return st, nil
}

// fetch downloads the resolved closure into bob's store and returns the generation entry.
func (in *installer) fetch(r *resolution, platform string) (genEntry, error) {
	cl := r.res.Closure
	if cl == nil || len(cl.Roots) == 0 {
		return genEntry{}, r.nothingToInstall(platform)
	}
	roots := slices.Clone(cl.Roots)
	sort.Strings(roots)
	for _, p := range roots {
		if !remote.IsStorePath(p) {
			return genEntry{}, diag.New(4005, "cache", remote.Host(r.reg.URL), "what", r.text)
		}
	}
	if err := in.realise(r, roots); err != nil {
		return genEntry{}, err
	}
	bins := map[string]string{}
	for _, p := range roots {
		for b, path := range in.p.listBins(p) {
			if _, dup := bins[b]; !dup {
				bins[b] = path
			}
		}
	}
	if len(bins) == 0 {
		return genEntry{}, diag.New(9, "ref", r.text, "kinds", "a closure without programs",
			"url", fmt.Sprintf("%s/p/%s/releases/%s/%s", r.reg.URL, r.ref.project, r.ref.release, r.res.Release.Version))
	}
	return genEntry{Name: r.ref.name(), Registry: r.reg.Name, Ref: r.ref.name(), Version: r.res.Release.Version,
		Platform: platform, StorePaths: roots, Bins: bins}, nil
}

// realise makes roots valid in bob's store: bob downloads the missing part of the closure
// from the registry's cache (bearer token, pinned keys) into a temporary local binary cache,
// and Nix substitutes from there (and from cache.nixos.org for paths the registry lacks),
// checking the same signatures again.
func (in *installer) realise(r *resolution, roots []string) error {
	st, err := in.store()
	if err != nil {
		return err
	}
	ctx := in.e.ctx
	invalid, err := st.Invalid(ctx, roots)
	if err != nil || len(invalid) == 0 {
		return err
	}
	tmpRoot := filepath.Join(st.Home(), "cache", "install")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return diag.New(7002, "reason", err.Error())
	}
	isRoot := map[string]bool{}
	for _, p := range invalid {
		isRoot[p] = true
	}
	skipped := false
	have := func(p string) bool {
		if isRoot[p] {
			return false
		}
		_, err := os.Lstat(in.p.hostPath(p))
		skipped = skipped || err == nil
		return err == nil
	}
	for attempt := 0; ; attempt++ {
		dir, err := os.MkdirTemp(tmpRoot, "fetch-")
		if err != nil {
			return diag.New(7002, "reason", err.Error())
		}
		raw, err := in.substitute(st, r, invalid, have, dir)
		_ = os.RemoveAll(dir)
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		var d *diag.Error
		if errors.As(err, &d) || errors.Is(err, context.Canceled) {
			return err
		}
		if attempt == 0 && skipped {
			// a path that looked present was not valid (a killed substitution): fetch all
			have = nil
			continue
		}
		d = diag.New(9005, "reason", "installing "+r.text+" failed (run with -v for details)")
		d.Raw = raw
		return d
	}
}

// substitute fetches into dir and runs nix-store --realise; a non-diagnostic error carries
// Nix's stderr lines in raw.
func (in *installer) substitute(st *store.Store, r *resolution, paths []string, have func(string) bool, dir string) ([]string, error) {
	missing, err := r.cache.Fetch(in.e.ctx, paths, have, dir)
	if err != nil {
		return nil, cacheDiag(r, err)
	}
	subs := []string{"file://" + dir}
	keys := slices.Clone(r.cache.Keys)
	if len(missing) > 0 {
		subs = append(subs, nixosCache)
		keys = append(keys, nixosCacheKey)
	}
	args := append([]string{"--realise"}, paths...)
	args = append(args, "--option", "substituters", strings.Join(subs, " "),
		"--option", "trusted-public-keys", strings.Join(keys, " "),
		"--option", "narinfo-cache-negative-ttl", "0")
	var se bytes.Buffer
	c := st.Command(in.e.ctx, "nix-store", args...)
	c.Stderr = &se
	if err := c.Run(); err != nil {
		var d *diag.Error
		if errors.As(err, &d) {
			return nil, err
		}
		return rawLines(se.String()), err
	}
	return nil, nil
}

func rawLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return lines
}

// cacheDiag maps a failed download from the registry's cache (SPEC §02.3 4xxx).
func cacheDiag(r *resolution, err error) error {
	var ce *remote.CacheError
	if !errors.As(err, &ce) {
		return err
	}
	host := remote.Host(r.cache.URL)
	what := r.text
	if i := strings.IndexByte(strings.TrimPrefix(ce.Path, "/nix/store/"), '-'); i >= 0 {
		what = fmt.Sprintf("%q of %s", strings.TrimPrefix(ce.Path, "/nix/store/")[i+1:], r.text)
	}
	var d *diag.Error
	hint := ""
	switch ce.Kind {
	case "unauthenticated", "denied":
		d = diag.New(4001, "cache", host)
		hint = "run bob login " + r.reg.URL
	case "missing":
		d = diag.New(4002, "cache", host, "what", what, "status", strconv.Itoa(ce.Status))
	case "untrusted":
		d = diag.New(4004, "cache", host, "what", what)
		hint = "the registry's signing key changed or is not pinned; if the change is expected, run bob registry add --refresh-keys " + r.reg.Name
	case "corrupt":
		d = diag.New(4005, "cache", host, "what", what)
	default:
		reason := "no answer"
		if ce.Status != 0 {
			reason = "HTTP " + strconv.Itoa(ce.Status)
		} else if ce.Err != nil {
			reason = transportReason(ce.Err)
		}
		d = diag.New(4003, "cache", host, "reason", reason)
	}
	d.Raw = []string{ce.Error()}
	if hint == "" {
		return d
	}
	return &hintedError{d, hint}
}

// bobPath is the bob the shims run when $BOB is unset: this executable.
func bobPath() string {
	p, err := os.Executable()
	if err != nil {
		return "bob"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func (e *env) installer() (*installer, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	return &installer{e: e, p: p}, nil
}

// lookupReg resolves a registry name ("" = the default) to its configuration.
func (e *env) lookupReg(name string) (registry, error) {
	c := e.installConfig()
	if len(c.Registries) == 0 {
		return registry{}, diag.New(4, "flag", "bob install", "what", "a registry (run bob registry add <name> <url>, or bob login <url>)")
	}
	r, ok := c.lookupRegistry(name)
	if !ok {
		if name == "" {
			return registry{}, diag.New(4, "flag", "bob install", "what", "a default registry (run bob registry default <name>)")
		}
		return registry{}, diag.New(1, "kind", "registry", "name", name)
	}
	return r, nil
}

// installResult is the `result` data of bob install.
type installResult struct {
	Name       string   `json:"name"`
	Ref        string   `json:"ref"`
	Version    string   `json:"version"`
	Registry   string   `json:"registry"`
	Generation int      `json:"generation"`
	Bins       []string `json:"bins"`
	BinDir     string   `json:"binDir"`
}

// runInstall is `bob install <ref> [--as <name>] [--bin-dir <dir>]` (SPEC §02.8, R-INST-001).
func runInstall(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob install", "what", "one reference ([<registry>:]<project>/<release>[@<version>])"))
	}
	text := e.a.pos[0]
	ref, err := parseRef(text)
	if err != nil {
		return e.out.fail(err)
	}
	as := e.a.str("as")
	if e.a.has("as") && (!safeName(as) || strings.Contains(as, "/")) {
		return e.out.fail(diag.New(4, "flag", "--as", "what", "a name of letters, digits, '.', '_', '+' and '-'"))
	}
	reg, err := e.lookupReg(ref.registry)
	if err != nil {
		return e.out.fail(err)
	}
	in, err := e.installer()
	if err != nil {
		return e.out.fail(err)
	}
	binDir := in.p.binDir
	if d := e.a.str("bin-dir"); d != "" {
		if !filepath.IsAbs(d) {
			d = filepath.Join(e.cwd, d)
		}
		binDir = filepath.Clean(d)
	}
	e.out.begin([]string{text})
	platform := hostPlatform()
	r, err := in.resolve(reg, ref, ref.version, platform, text)
	if err != nil {
		return failHinted(e, err)
	}
	en, err := in.fetch(r, platform)
	if err != nil {
		return failHinted(e, err)
	}
	if as != "" {
		en.Name = as
	}
	if binDir != in.p.binDir {
		en.BinDir = binDir
	}
	unlock, err := in.p.lock()
	if err != nil {
		return e.out.fail(err)
	}
	defer unlock()
	cur, err := in.p.current()
	if err != nil {
		return e.out.fail(err)
	}
	g := cur
	if old := cur.entry(en.Name); old != nil && sameEntry(old, &en) {
		e.out.printf("%s %s is already installed (generation %d)\n", en.Name, en.Version, cur.N)
	} else {
		entries := []genEntry{en}
		for _, o := range cur.Entries {
			if o.Name != en.Name {
				entries = append(entries, o)
			}
		}
		if err := in.p.conflict(&en, entries); err != nil {
			return e.out.fail(err)
		}
		if g, err = in.p.commit(entries); err != nil {
			return e.out.fail(err)
		}
		var shims []string
		for _, b := range en.binNames() {
			shims = append(shims, en.shimName(b))
		}
		e.out.printf("installed %s %s (generation %d): %s → %s\n", en.Ref, en.Version, g.N, strings.Join(shims, ", "), tildePath(in.p.entryBinDir(&en)))
	}
	if err := in.p.syncShims(bobPath(), g, cur); err != nil {
		return e.out.fail(err)
	}
	pathHint(e, in.p.entryBinDir(&en))
	e.out.result(installResult{en.Name, en.Ref, en.Version, en.Registry, g.N, en.binNames(), in.p.entryBinDir(&en)})
	return e.out.finish(0, translate.Counts{}, nil)
}

func sameEntry(a, b *genEntry) bool {
	return a.Ref == b.Ref && a.Version == b.Version && a.Registry == b.Registry && a.BinDir == b.BinDir &&
		slices.Equal(a.StorePaths, b.StorePaths) && slices.Equal(a.binNames(), b.binNames())
}

// upgradeChange is one entry an upgrade replaced.
type upgradeChange struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// runUpgrade is `bob upgrade [names…] [--minor-only]`: every (named) entry is resolved again
// on its own registry (@latest, or the pinned major with --minor-only), and all changes are
// installed as one generation.
func runUpgrade(e *env) int {
	in, err := e.installer()
	if err != nil {
		return e.out.fail(err)
	}
	e.out.begin(e.a.pos)
	cur, err := in.p.current()
	if err != nil {
		return e.out.fail(err)
	}
	for _, n := range e.a.pos {
		if cur.entry(n) == nil {
			return e.out.fail(notInstalled("bob upgrade", n))
		}
	}
	updates := map[string]genEntry{}
	for _, en := range cur.Entries {
		if len(e.a.pos) > 0 && !slices.Contains(e.a.pos, en.Name) {
			continue
		}
		ref, err := parseRef(en.Ref)
		if err != nil {
			return e.out.fail(err)
		}
		reg, err := e.lookupReg(en.Registry)
		if err != nil {
			return e.out.fail(err)
		}
		constraint := ""
		if e.a.bool("minor-only") {
			constraint, _, _ = strings.Cut(en.Version, ".")
		}
		r, err := in.resolve(reg, ref, constraint, en.Platform, en.Registry+":"+en.Ref)
		if err != nil {
			return failHinted(e, err)
		}
		if cl := r.res.Closure; r.res.Release.Version == en.Version && cl != nil && sameRoots(cl.Roots, en.StorePaths) {
			continue
		}
		next, err := in.fetch(r, en.Platform)
		if err != nil {
			return failHinted(e, err)
		}
		next.Name, next.BinDir = en.Name, en.BinDir
		updates[en.Name] = next
	}
	unlock, err := in.p.lock()
	if err != nil {
		return e.out.fail(err)
	}
	defer unlock()
	now, err := in.p.current() // another bob may have changed the profile meanwhile
	if err != nil {
		return e.out.fail(err)
	}
	var changes []upgradeChange
	entries := slices.Clone(now.Entries)
	for i := range entries {
		if u, ok := updates[entries[i].Name]; ok && !sameEntry(&entries[i], &u) {
			changes = append(changes, upgradeChange{u.Name, entries[i].Version, u.Version})
			entries[i] = u
		}
	}
	for _, c := range changes {
		u := updates[c.Name]
		if err := in.p.conflict(&u, entries); err != nil {
			return e.out.fail(err)
		}
	}
	g := now
	if len(changes) > 0 {
		if g, err = in.p.commit(entries); err != nil {
			return e.out.fail(err)
		}
	}
	if err := in.p.syncShims(bobPath(), g, now); err != nil {
		return e.out.fail(err)
	}
	for _, c := range changes {
		e.out.printf("%s  %s → %s\n", c.Name, c.From, c.To)
	}
	if len(changes) > 0 {
		e.out.printf("bob: %d upgraded (generation %d)\n", len(changes), g.N)
	} else {
		e.out.printf("bob: 0 upgraded, everything is up to date\n")
	}
	if changes == nil {
		changes = []upgradeChange{}
	}
	e.out.result(map[string]any{"generation": g.N, "upgraded": changes})
	return e.out.finish(0, translate.Counts{}, nil)
}

func sameRoots(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}

func notInstalled(cmd, name string) *diag.Error {
	return diag.New(4, "flag", cmd, "what", fmt.Sprintf("the name of an installed release (%q is not installed; see bob list)", name))
}

// runUninstall is `bob uninstall <name>…`: a new generation without the entries; their
// shims are removed.
func runUninstall(e *env) int {
	if len(e.a.pos) == 0 {
		return e.out.fail(diag.New(4, "flag", "bob uninstall", "what", "the name of an installed release"))
	}
	p, err := e.profile()
	if err != nil {
		return e.out.fail(err)
	}
	e.out.begin(e.a.pos)
	unlock, err := p.lock()
	if err != nil {
		return e.out.fail(err)
	}
	defer unlock()
	cur, err := p.current()
	if err != nil {
		return e.out.fail(err)
	}
	var entries []genEntry
	for _, n := range e.a.pos {
		if cur.entry(n) == nil {
			return e.out.fail(notInstalled("bob uninstall", n))
		}
	}
	for _, en := range cur.Entries {
		if !slices.Contains(e.a.pos, en.Name) {
			entries = append(entries, en)
		}
	}
	g, err := p.commit(entries)
	if err != nil {
		return e.out.fail(err)
	}
	if err := p.syncShims(bobPath(), g, cur); err != nil {
		return e.out.fail(err)
	}
	e.out.printf("uninstalled %s (generation %d)\n", strings.Join(e.a.pos, ", "), g.N)
	e.out.result(map[string]any{"generation": g.N, "uninstalled": e.a.pos})
	return e.out.finish(0, translate.Counts{}, nil)
}

// runRollback is `bob rollback [--to N]`: `current` goes back to the previous (or N-th)
// generation and the shims follow.
func runRollback(e *env) int {
	p, err := e.profile()
	if err != nil {
		return e.out.fail(err)
	}
	e.out.begin(nil)
	unlock, err := p.lock()
	if err != nil {
		return e.out.fail(err)
	}
	defer unlock()
	cur, err := p.current()
	if err != nil {
		return e.out.fail(err)
	}
	target := 0
	if e.a.has("to") {
		target, _ = strconv.Atoi(e.a.str("to"))
		if !slices.Contains(p.numbers(), target) {
			return e.out.fail(diag.New(4, "flag", "--to", "what", "an existing generation number"))
		}
	} else {
		for _, n := range p.numbers() {
			if n < cur.N {
				target = n
			}
		}
		if target == 0 {
			return e.out.fail(diag.New(4, "flag", "bob rollback", "what", "an earlier generation"))
		}
	}
	g, err := p.rollbackTo(target)
	if err != nil {
		return e.out.fail(err)
	}
	if err := p.syncShims(bobPath(), g, cur); err != nil {
		return e.out.fail(err)
	}
	e.out.printf("generation %d → %d: %s\n", cur.N, g.N, genDiff(cur, g))
	e.out.result(map[string]any{"from": cur.N, "to": g.N})
	return e.out.finish(0, translate.Counts{}, nil)
}

// genDiff describes the entries that differ between two generations.
func genDiff(a, b *generation) string {
	names := map[string]bool{}
	for _, en := range append(slices.Clone(a.Entries), b.Entries...) {
		names[en.Name] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var parts []string
	for _, n := range sorted {
		va, vb := "(none)", "(none)"
		if en := a.entry(n); en != nil {
			va = en.Version
		}
		if en := b.entry(n); en != nil {
			vb = en.Version
		}
		if va != vb {
			parts = append(parts, fmt.Sprintf("%s %s → %s", n, va, vb))
		}
	}
	if len(parts) == 0 {
		return "no version changes"
	}
	return strings.Join(parts, ", ")
}

// listEntry is one row of `bob list --json`.
type listEntry struct {
	Name       string   `json:"name"`
	Ref        string   `json:"ref"`
	Version    string   `json:"version"`
	Registry   string   `json:"registry"`
	Platform   string   `json:"platform"`
	Generation int      `json:"generation"`
	Bins       []string `json:"bins"`
}

// runList is `bob list` (the entries of the current generation) and `bob list --available
// [<registry>:]<project>` (the registry's releases of a project).
func runList(e *env) int {
	if e.a.bool("available") {
		return listAvailable(e)
	}
	p, err := e.profile()
	if err != nil {
		return e.out.fail(err)
	}
	cur, err := p.current()
	if err != nil {
		return e.out.fail(err)
	}
	rows := []listEntry{}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
	for i := range cur.Entries {
		en := &cur.Entries[i]
		rows = append(rows, listEntry{en.Name, en.Ref, en.Version, en.Registry, en.Platform, cur.N, en.binNames()})
		var shims []string
		for _, bn := range en.binNames() {
			shims = append(shims, en.shimName(bn))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", en.Name, en.Version, en.Registry, strings.Join(shims, ", "))
	}
	_ = tw.Flush()
	e.out.printf("%s", trimRows(b.String()))
	e.out.result(map[string]any{"generation": cur.N, "installed": rows})
	return e.out.finish(0, translate.Counts{}, nil)
}

func listAvailable(e *env) int {
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob list --available", "what", "a project ([<registry>:]<project>)"))
	}
	regName, project, ok := strings.Cut(e.a.pos[0], ":")
	if !ok {
		regName, project = "", e.a.pos[0]
	}
	if !nameSlug().MatchString(project) {
		return e.out.fail(diag.New(4, "flag", "bob list --available", "what", "a project name"))
	}
	reg, err := e.lookupReg(regName)
	if err != nil {
		return e.out.fail(err)
	}
	tok, _, err := e.token(e.ctx, reg.URL)
	if err != nil {
		return e.out.fail(err)
	}
	rels, err := e.api(reg.URL, tok).ListReleases(e.ctx, project)
	if err != nil {
		return e.out.fail(apiDiag(reg.URL, remote.ListReleasesMethod, project, err))
	}
	type row struct {
		Release string `json:"release"`
		Version string `json:"version"`
		Ref     string `json:"ref"`
	}
	rows := []row{}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
	for _, r := range rels {
		ref := r.ProjectID + "/" + r.Name + "@" + r.Version
		rows = append(rows, row{r.Name, r.Version, ref})
		fmt.Fprintf(tw, "%s/%s\t%s\n", project, r.Name, r.Version)
	}
	_ = tw.Flush()
	e.out.printf("%s", trimRows(b.String()))
	e.out.result(map[string]any{"registry": reg.Name, "releases": rows})
	return e.out.finish(0, translate.Counts{}, nil)
}

// runExecCmd is `bob exec`: `--installed <name> -- <bin>` runs an installed program (the
// shims of bob install), everything else is the developer environment's bob exec.
func runExecCmd(e *env) int {
	if e.a.has("installed") {
		return runInstalled(e)
	}
	return runExec(e)
}

// runInstalled is `bob exec --installed <name> -- <bin> [args]`: it reads `current` and runs
// the program inside bob's namespace with the caller's environment, unchanged (R-INST-006);
// no project state is read.
func runInstalled(e *env) int {
	name := e.a.str("installed")
	if !e.a.dash || len(e.a.pass) == 0 {
		return e.out.fail(diag.New(4, "flag", "bob exec --installed "+name, "what", "-- <program> [args]"))
	}
	bin := e.a.pass[0]
	p, err := e.profile()
	if err != nil {
		return e.out.fail(err)
	}
	cur, err := p.current()
	if err != nil {
		return e.out.fail(err)
	}
	en := cur.entry(name)
	if en == nil {
		return e.out.fail(notInstalled("bob exec --installed", name))
	}
	path, ok := en.Bins[bin]
	if !ok {
		return e.out.fail(diag.New(4, "flag", "bob exec --installed "+name, "what",
			fmt.Sprintf("one of its programs (%s)", strings.Join(en.binNames(), ", "))))
	}
	argv := append([]string{path}, e.a.pass[1:]...)
	env := os.Environ()
	if store.Inside(p.home) { // the program replaces bob
		err := syscall.Exec(path, argv, env)
		d := diag.New(5, "label", name+" "+bin)
		d.Raw = []string{err.Error()}
		return e.out.fail(d)
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	return runIn(e, p.home, cmd, bin)
}
