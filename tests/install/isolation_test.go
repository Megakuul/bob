package install

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

func envLines(s string) map[string]string {
	m := map[string]string{}
	for _, l := range lines(s) {
		k, v, _ := strings.Cut(l, "=")
		m[k] = v
	}
	return m
}

// diffEnv returns the names whose values differ (or exist on one side only), minus ignore.
func diffEnv(a, b map[string]string, ignore func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for k, v := range a {
		seen[k] = true
		if w, ok := b[k]; (!ok || w != v) && !ignore(k) {
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] && !ignore(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestRINST006NoProjectState: an installed program runs with the caller's environment and no
// project's .bob/env.json: from inside an activated project (hook active, and under `bob dev`)
// its environment equals the caller's, and equals the environment from `/` apart from PWD,
// BOB_PROJECT, BOB_ENV_* and the project's exported outside variables; installing never touches
// a working tree.
func TestRINST006NoProjectState(t *testing.T) {
	parity.Covers(t, "R-INST-006")
	e, srv := setup(t)
	if r := e.addRegistry(t, "default", srv); harness.Red(t, r) {
		return
	}
	proj := harness.Fixture(t, "acme")
	before := tree(t, proj)
	if r := e.bob(t, proj, "install", "acme/cli@1.0.0"); r.Exit != 0 {
		t.Fatalf("install: exit %d\n%s", r.Exit, r.Combined())
	}
	if tree(t, proj) != before {
		t.Errorf("installing from inside a working tree changed it")
	}
	if r := e.bob(t, proj, "allow"); r.Exit != 0 {
		t.Fatalf("bob allow: exit %d\n%s", r.Exit, r.Combined())
	}
	// the project's exported outside variables, from `bob env --format json`.
	var outside []string
	envj := e.bob(t, proj, "env", "--format", "json")
	outside = append(outside, jsonKeys(envj.Stdout)...)
	ignore := func(k string) bool {
		if k == "PWD" || k == "OLDPWD" || k == "_" || k == "SHLVL" || k == "BOB_PROJECT" || strings.HasPrefix(k, "BOB_ENV_") {
			return true
		}
		for _, o := range outside {
			if o == k {
				return true
			}
		}
		return false
	}
	prog := e.shim("acme")
	bash := func(dir, script string) map[string]string {
		t.Helper()
		out := e.run(t, dir, "/bin/bash", "--norc", "--noprofile", "-c", script)
		if out.Exit != 0 {
			t.Fatalf("bash: exit %d\n%s", out.Exit, out.Combined())
		}
		return envLines(out.Stdout)
	}
	hook := `eval "$(` + harness.Bin(t, "bob") + ` hook bash)"; `
	fromRoot := bash("/", prog+" --env")
	// hook active: cd into the project, run the prompt hook, then the program.
	inProject := bash(proj, hook+`_bob_hook; `+prog+` --env`)
	caller := bash(proj, hook+`_bob_hook; env | sed "s/^_=.*//"`)
	if d := diffEnv(inProject, fromRoot, ignore); len(d) != 0 {
		t.Errorf("program environment from inside the activated project differs from the one from / in %v", d)
	}
	if d := diffEnv(inProject, caller, ignore); len(d) != 0 {
		t.Errorf("program environment differs from the caller's environment in %v (bob must add nothing)", d)
	}

	// under `bob dev`: the program sees the dev shell's environment as its caller's, nothing more.
	script := `env | sed "s/^_=.*//" > "$1/caller"; ` + prog + ` --env > "$1/prog"`
	dir := t.TempDir()
	r := e.bob(t, proj, "dev", "--", "sh", "-c", script, "sh", dir)
	if r.Exit != 0 {
		t.Fatalf("bob dev -- acme --env: exit %d\n%s", r.Exit, r.Combined())
	}
	c, p := envLines(readFile(t, filepath.Join(dir, "caller"))), envLines(readFile(t, filepath.Join(dir, "prog")))
	if d := diffEnv(p, c, func(k string) bool { return k == "_" || k == "PWD" || k == "OLDPWD" || k == "SHLVL" }); len(d) != 0 {
		t.Errorf("under bob dev the program's environment differs from its caller's in %v", d)
	}
}

// jsonKeys returns every object key at depth ≤ 3 below "outside" in `bob env --format json`.
func jsonKeys(s string) []string {
	var doc struct {
		Outside struct {
			Set     map[string]any `json:"set"`
			Prepend map[string]any `json:"prepend"`
		} `json:"outside"`
	}
	if err := unmarshal(s, &doc); err != nil {
		return nil
	}
	var out []string
	for k := range doc.Outside.Set {
		out = append(out, k)
	}
	for k := range doc.Outside.Prepend {
		out = append(out, k)
	}
	return out
}
