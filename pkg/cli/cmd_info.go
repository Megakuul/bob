package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// runVersion is `bob version [--json]`: bob, the embedded Nix and the default nixpkgs channel.
func runVersion(e *env) int {
	if len(e.a.pos) > 0 {
		return e.out.fail(diag.New(4, "flag", "bob version", "what", "no arguments"))
	}
	data := struct {
		Bob     string `json:"bob"`
		Nix     string `json:"nix"`
		Nixpkgs string `json:"nixpkgs"`
	}{bobVersion(), nixVersion, lock.DefaultChannel}
	e.out.result(data)
	e.out.printf("bob %s\nnix %s\nnixpkgs %s\n", data.Bob, data.Nix, data.Nixpkgs)
	return e.out.finish(0, translate.Counts{}, nil)
}

// enterStore runs the rest of the command inside bob's namespace (one entry per command,
// R-NIX-021) and opens the store there. done = true means the command is over (the outer
// process waited for the re-executed one, or entering failed) and exit is its status.
func (e *env) enterStore() (st *store.Store, exit int, done bool) {
	st, _, warns, exit, done := e.openStore(false)
	for _, w := range warns {
		e.out.diagnose(w, "")
	}
	return st, exit, done
}

// openStore is enterStore with bob's binary caches (R-DEV-004): the caches of config.json and
// the logged-in servers. With check, the credential pre-check of R-NIX-029 runs first (inside
// the namespace, so once per command) and an unreachable server is left out with a warning;
// caches are then the substituters for build.Options.Caches.
func (e *env) openStore(check bool) (st *store.Store, caches []store.Substituter, warns []*diag.Error, exit int, done bool) {
	inside, code, err := store.Enter(e.ctx, store.Config{})
	if err != nil {
		return nil, nil, nil, e.out.fail(err), true
	}
	if !inside {
		// The re-executed bob wrote the whole output; this process only passes its status on.
		e.out.started, e.out.finished = true, true
		return nil, nil, nil, code, true
	}
	var skip map[string]bool
	if check {
		if skip, warns, err = e.preCheck(e.ctx); err != nil {
			return nil, nil, nil, e.out.fail(err), true
		}
	}
	creds, _ := e.loadCreds()
	caches = e.substituters(creds, skip)
	st, err = store.Open(e.ctx, store.Config{Substituters: caches})
	if err != nil {
		return nil, nil, nil, e.out.fail(err), true
	}
	return st, caches, warns, 0, false
}

// namespaceMs is the duration of the namespace entry of this process (the `namespace` stage
// event, R-NIX-021).
func namespaceMs() int64 {
	d, _ := store.Entered()
	return d.Milliseconds()
}

// runDoctor is `bob doctor [--repair]`: namespace check, store verify, config check.
// `--print-nix-env` prints the exact environment of every Nix process (R-NIX-024).
func runDoctor(e *env) int {
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	if e.a.bool("print-nix-env") {
		for _, kv := range st.NixEnv() {
			e.out.printf("%s\n", kv)
		}
		return e.out.finish(0, translate.Counts{}, nil)
	}
	var problems []*diag.Error
	check := func(what string, err error) {
		if err == nil {
			e.out.printf("ok      %s\n", what)
			return
		}
		e.out.printf("FAILED  %s\n", what)
		problems = append(problems, diagErrors(err)...)
	}
	check("namespace", nil)
	_, err := st.Builder(e.ctx)
	check("builder", err)
	args := []string{"--verify"}
	if e.a.bool("repair") {
		args = append(args, "--check-contents", "--repair")
	}
	var so, se bytes.Buffer
	c := st.Command(e.ctx, "nix-store", args...)
	c.Stdout, c.Stderr = &so, &se
	if err := c.Run(); err != nil {
		d := diag.New(7003, "what", "the store database does not match the store")
		d.Raw = strings.Split(strings.TrimSpace(se.String()), "\n")
		check("store", d)
	} else {
		check("store", nil)
	}
	check("config", checkConfig(e))
	if len(problems) == 0 {
		e.out.printf("\nbob: no problems\n")
		return e.out.finish(0, translate.Counts{}, nil)
	}
	e.out.printf("\n")
	for _, p := range problems {
		e.out.diagnose(p, "")
	}
	return e.out.finish(-1, translate.Counts{}, nil)
}

// checkConfig validates the JSON of config.json and credentials.json (credentials 0600).
func checkConfig(e *env) error {
	dir := configDir(e.getenv)
	for _, name := range []string{"config.json", "credentials.json"} {
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return diag.New(7002, "reason", p+" cannot be read")
		}
		var v map[string]json.RawMessage
		if err := readJSON(p, &v); err != nil {
			return diag.New(7002, "reason", p+" is not valid JSON")
		}
		if name == "credentials.json" && fi.Mode().Perm()&0o077 != 0 {
			return diag.New(7002, "reason", p+" must have mode 0600")
		}
	}
	return nil
}

// lastBuild is <root>/.bob/last-build.json: label → derivation of the last local build of each
// package, which `bob log <label>` reads (R-CLI-008).
const lastBuild = ".bob/last-build.json"

// recordBuild merges label → drv into the record of root. Failures are ignored: the record
// only serves bob log.
func recordBuild(root string, drvs map[string]string) {
	if len(drvs) == 0 {
		return
	}
	p := filepath.Join(root, filepath.FromSlash(lastBuild))
	m := map[string]string{}
	_ = readJSON(p, &m)
	for l, d := range drvs {
		m[l] = d
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// runLog is `bob log <label>`: the full log of the label's last local build, exactly as Nix
// stored it (raw text is the point here). `--build` reads a server build.
func runLog(e *env) int {
	if e.a.has("build") {
		return runServerLog(e)
	}
	if e.a.bool("f") || e.a.has("attempt") {
		return e.out.fail(diag.New(4, "flag", "-f/--attempt", "what", "--build <id> (they apply to server builds)"))
	}
	if len(e.a.pos) != 1 {
		return e.out.fail(diag.New(4, "flag", "bob log", "what", "one label"))
	}
	label := e.a.pos[0]
	if _, err := bob.ParseLabel(label); err != nil {
		return e.out.fail(diag.New(1, "kind", "label", "name", label))
	}
	root, err := e.root(false)
	if err != nil {
		return e.out.fail(err)
	}
	m := map[string]string{}
	_ = readJSON(filepath.Join(root, filepath.FromSlash(lastBuild)), &m)
	d, ok := m[label]
	if !ok || !strings.HasPrefix(d, "/nix/store/") || strings.ContainsAny(d, "\n\x00") {
		return e.out.fail(diag.New(3, "pattern", label))
	}
	st, exit, done := e.enterStore()
	if done {
		return exit
	}
	var so, se bytes.Buffer
	c := st.Command(e.ctx, "nix-store", "--read-log", d)
	c.Stdout, c.Stderr = &so, &se
	if err := c.Run(); err != nil {
		de := diag.New(3, "pattern", label)
		de.Raw = strings.Split(strings.TrimSpace(se.String()), "\n")
		return e.out.fail(de)
	}
	if e.out.json {
		e.out.begin([]string{label})
		for _, l := range strings.Split(strings.TrimRight(so.String(), "\n"), "\n") {
			e.out.event("log", struct {
				Label string `json:"label"`
				Line  string `json:"line"`
			}{label, l})
		}
		return e.out.finish(0, translate.Counts{}, nil)
	}
	_, _ = e.out.w.Write(so.Bytes())
	return 0
}
