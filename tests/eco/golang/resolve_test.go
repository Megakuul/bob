package golang

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// corpusModule is one real module the build-list comparison runs on: a scratch workspace
// module requiring it, with a main package importing pkg (empty: nothing imported).
type corpusModule struct{ path, version, pkg string }

// corpus is the set of real-world modules besides the fixture and bob itself (R-GO-002b asks
// for >= 10 modules incl. the fixture, bob, client-go and grpc-go).
var corpus = []corpusModule{
	{"k8s.io/client-go", "v0.31.0", "k8s.io/client-go/kubernetes"},
	{"google.golang.org/grpc", "v1.67.0", "google.golang.org/grpc"},
	{"github.com/google/uuid", "v1.6.0", "github.com/google/uuid"},
	{"github.com/spf13/cobra", "v1.8.1", "github.com/spf13/cobra"},
	{"go.uber.org/zap", "v1.27.0", "go.uber.org/zap"},
	{"github.com/stretchr/testify", "v1.9.0", "github.com/stretchr/testify/assert"},
	{"golang.org/x/text", "v0.20.0", "golang.org/x/text/language"},
	{"github.com/google/go-cmp", "v0.7.0", "github.com/google/go-cmp/cmp"},
	{"github.com/prometheus/client_golang", "v1.20.5", "github.com/prometheus/client_golang/prometheus"},
	{"google.golang.org/protobuf", "v1.35.1", "google.golang.org/protobuf/proto"},
}

// bobRequirements returns the direct requirements of bob's own go.mod.
func bobRequirements(t *testing.T) []corpusModule {
	t.Helper()
	var out []corpusModule
	in := false
	for _, l := range strings.Split(readFile(t, filepath.Join(harness.Root(t), "go.mod")), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "require (":
			in = true
		case l == ")":
			in = false
		case in && !strings.Contains(l, "// indirect"):
			if f := strings.Fields(l); len(f) >= 2 {
				out = append(out, corpusModule{path: f[0], version: f[1]})
			}
		}
	}
	return out
}

// corpusRepo writes a scratch repository with one workspace module example.com/corpus
// requiring reqs, and returns its directory.
func corpusRepo(t *testing.T, reqs []corpusModule) string {
	t.Helper()
	dir := t.TempDir()
	var requires, imports strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&requires, "\t\t\tgolang.Require(%q, %q),\n", r.path, r.version)
		if r.pkg != "" {
			fmt.Fprintf(&imports, "\t_ %q\n", r.pkg)
		}
	}
	files := map[string]string{
		"go.mod": "module example.com/corproot\n\ngo 1.26.0\n",
		"bob.go": "//go:build bob\n\npackage corproot\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"corpus/bob.go": "//go:build bob\n\npackage corpus\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/golang\"\n)\n\n" +
			"// Module is example.com/corpus (label //corpus:module).\nvar Module = bob.Define(func(p *bob.P) *golang.Module {\n\treturn golang.NewModule(p, golang.ModuleOpts{\n\t\tPath: \"example.com/corpus\", Go: \"1.26\",\n\t\tRequire: []golang.Requirement{\n" +
			requires.String() + "\t\t},\n\t})\n})\n\n// Main is the binary (label //corpus/cmd/corpus:corpus).\nvar Main = bob.Define(func(p *bob.P) *golang.Binary {\n\treturn Module(p).Binary(\"./cmd/corpus\", golang.BinaryOpts{})\n})\n",
		"corpus/cmd/corpus/main.go": "package main\n\nimport (\n" + imports.String() + ")\n\nfunc main() {}\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "corpus"},
	} {
		gitIn(t, dir, args...)
	}
	return dir
}

// lockedBuildLists returns, per workspace module path of the lock, bob's build list
// ("path@version"), read from the go section of bob.lock.
func lockedBuildLists(t *testing.T, dir string) map[string][]string {
	t.Helper()
	var lock struct {
		Go struct {
			Workspaces map[string]struct {
				Modules []string `json:"modules"`
			} `json:"workspaces"`
		} `json:"go"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "bob.lock"))), &lock); err != nil {
		t.Fatalf("bob.lock: %v", err)
	}
	out := map[string][]string{}
	for ws, bl := range lock.Go.Workspaces {
		out[ws] = bl.Modules
	}
	return out
}

// goIn runs the go command of the environment env in dir (offline: GOPROXY points at the
// directory bob wrote, GOFLAGS=-mod=readonly, GOSUMDB=off, GOTOOLCHAIN=local from bob env).
func goIn(t *testing.T, dir string, env []string, args ...string) harness.Result {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	// cgo is whatever `bob env` prescribes (SPEC does not switch it off): the fixture's cgo
	// package must be built and vetted too.
	cmd.Env = append(env, "HOME="+t.TempDir(), "GOCACHE="+t.TempDir(), "GOPATH="+t.TempDir())
	return runCmd(t, cmd, args)
}

// R-GO-002b: with bob's generated go.mod/go.sum and its GOPROXY directory, `go list -m all`
// (GOFLAGS=-mod=readonly, GOSUMDB=off, GOTOOLCHAIN=local) exits 0 and lists exactly bob's
// build list, and `go build ./... && go vet ./...` succeed without network. The corpus is the
// Go fixture (3 workspace modules), bob's own requirements and the modules of `corpus`.
func TestRGO002bBuildListEqualsGoCommand(t *testing.T) {
	parity.Covers(t, "R-GO-002b")
	type entry struct {
		name string
		repo func(t *testing.T) string
		mods []string // workspace module dirs, repo-relative
	}
	entries := []entry{
		{"fixture-go", func(t *testing.T) string { return harness.Fixture(t, "go") }, []string{"api", "lib", "tools"}},
		{"bob", func(t *testing.T) string { return corpusRepo(t, bobRequirements(t)) }, []string{"corpus"}},
	}
	for _, c := range corpus {
		c := c
		entries = append(entries, entry{c.path, func(t *testing.T) string { return corpusRepo(t, []corpusModule{c}) }, []string{"corpus"}})
	}
	if len(entries) < 10 {
		t.Fatalf("harness: the corpus has %d modules, SPEC asks for at least 10", len(entries))
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			home := t.TempDir()
			dir := e.repo(t)
			if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
				return
			}
			if r := bobEnv(t, dir, registryEnv(home), "gen"); !ok(t, r) {
				return
			}
			env, good := devEnv(t, dir, home)
			if !good {
				return
			}
			lists := lockedBuildLists(t, dir)
			for _, m := range e.mods {
				modDir := filepath.Join(dir, m)
				path := modulePath(t, modDir)
				want := append([]string(nil), lists[path]...)
				sort.Strings(want)
				if len(want) == 0 {
					t.Errorf("bob.lock has no build list for workspace module %s", path)
					continue
				}
				r := goIn(t, modDir, env, "list", "-m", "all")
				if !exitIs(t, r, 0) {
					continue
				}
				var got []string
				for i, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
					f := strings.Fields(l)
					if i == 0 && len(f) == 1 { // the main module has no version
						continue
					}
					if len(f) != 2 {
						t.Errorf("go list -m all: unexpected line %q", l)
						continue
					}
					got = append(got, f[0]+"@"+f[1])
				}
				sort.Strings(got)
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s: go list -m all differs from bob's build list\ngo:\n%s\nbob:\n%s", path, strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				for _, sub := range []string{"build", "vet"} {
					exitIs(t, goIn(t, modDir, env, sub, "./..."), 0)
				}
			}
		})
	}
}

// modulePath reads the module path of the go.mod in dir.
func modulePath(t *testing.T, dir string) string {
	t.Helper()
	for _, l := range strings.Split(readFile(t, filepath.Join(dir, "go.mod")), "\n") {
		if p, found := strings.CutPrefix(l, "module "); found {
			return strings.TrimSpace(p)
		}
	}
	t.Fatalf("harness: no module line in %s/go.mod", dir)
	return ""
}
