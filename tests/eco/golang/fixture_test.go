package golang

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// registryEnv points bob's resolver at the offline registry mirror (tools/regmirror, HARNESS
// §4.3) when the harness started one: BOB_TEST_GOPROXY is its Go module proxy URL. The run
// then needs no network. Without it bob uses its configured default proxy.
func registryEnv(home string, extra ...string) []string {
	env := []string{"BOB_HOME=" + home, "BOB_TEST_HTTP_LOOPBACK=1"} // the mirror is plain http (A109)
	if p := os.Getenv("BOB_TEST_GOPROXY"); p != "" {
		env = append(env, "BOB_GOPROXY="+p)
	}
	return append(env, extra...)
}

// lockedFixture copies repository fixture name and runs `bob lock` in it.
func lockedFixture(t *testing.T, name, home string) (string, bool) {
	t.Helper()
	dir := harness.Fixture(t, name)
	if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
		return dir, false
	}
	return dir, true
}

// devEnv returns the process environment `bob env --format json` prescribes for dir: the
// outside variables and PATH (the shims first).
func devEnv(t *testing.T, dir, home string) ([]string, bool) {
	t.Helper()
	r := bobEnv(t, dir, registryEnv(home), "env", "--format", "json")
	if !ok(t, r) {
		return nil, false
	}
	var data struct {
		Outside struct {
			Set     map[string]string   `json:"set"`
			Prepend map[string][]string `json:"prepend"`
		} `json:"outside"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &data); err != nil {
		t.Errorf("bob env --format json: not a JSON object: %v\n%s", err, r.Stdout)
		return nil, false
	}
	var env []string
	for k, v := range data.Outside.Set {
		env = append(env, k+"="+v)
	}
	path := "/usr/bin:/bin"
	if p := data.Outside.Prepend["PATH"]; len(p) > 0 {
		path = strings.Join(p, ":") + ":" + path
	}
	sort.Strings(env)
	return append(env, "PATH="+path), true
}

// synRepo creates the synthetic repository of n packages (module example.com/syn at //syn)
// and returns its directory and, per package, its imports.
func synRepo(t *testing.T, n, width int) (string, map[int][]int) {
	t.Helper()
	dir := t.TempDir()
	imports := map[int][]int{}
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	name := func(i int) string { return fmt.Sprintf("p%03d", i) }
	var top []int
	for i := 0; i < n; i++ {
		layer := i / width
		var imps []int
		if layer > 0 {
			base := (layer - 1) * width
			imps = []int{base + (i*7)%width, base + (i*13+5)%width}
			if imps[0] == imps[1] {
				imps = imps[:1]
			}
		}
		imports[i] = imps
		if i >= n-width {
			top = append(top, i)
		}
		var s strings.Builder
		fmt.Fprintf(&s, "package %s\n\nimport (\n\t\"strconv\"\n\t\"strings\"\n", name(i))
		for _, j := range imps {
			fmt.Fprintf(&s, "\n\t\"example.com/syn/lib/%s\"", name(j))
		}
		fmt.Fprintf(&s, "\n)\n\n// Value combines this package's number with its dependencies.\nfunc Value() int {\n\tv := %d\n", i)
		for _, j := range imps {
			fmt.Fprintf(&s, "\tv += %s.Value() %% 1000\n", name(j))
		}
		s.WriteString("\treturn v\n}\n\n")
		for k := 0; k < 8; k++ {
			fmt.Fprintf(&s, "// Name%d formats a label.\nfunc Name%d(xs []int) string {\n\tvar b strings.Builder\n\tfor i, x := range xs {\n\t\tif i > 0 {\n\t\t\tb.WriteString(\",\")\n\t\t}\n\t\tb.WriteString(strconv.Itoa(x*%d + Value()))\n\t}\n\treturn strings.ToUpper(b.String())\n}\n\n", k, k, k+1)
		}
		write(fmt.Sprintf("syn/lib/%s/%s.go", name(i), name(i)), s.String())
	}
	var m strings.Builder
	m.WriteString("package main\n\nimport (\n\t\"fmt\"\n")
	for _, j := range top {
		fmt.Fprintf(&m, "\t\"example.com/syn/lib/%s\"\n", name(j))
	}
	m.WriteString(")\n\nfunc main() {\n\tt := 0\n")
	for _, j := range top {
		fmt.Fprintf(&m, "\tt += %s.Value()\n", name(j))
	}
	m.WriteString("\tfmt.Println(\"total\", t)\n}\n")
	write("syn/cmd/syn/main.go", m.String())
	write("go.mod", "module example.com/synroot\n\ngo 1.26.0\n")
	write("bob.go", "//go:build bob\n\npackage synroot\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n")
	write("syn/bob.go", `//go:build bob

package syn

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
)

// Module is example.com/syn (label //syn:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{Path: "example.com/syn", Go: "1.26"})
})

// Syn is the binary (label //syn/cmd/syn:syn).
var Syn = bob.Define(func(p *bob.P) *golang.Binary {
	return Module(p).Binary("./cmd/syn", golang.BinaryOpts{})
})
`)
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=bob-harness", "-c", "user.email=harness@bob.invalid", "commit", "-q", "-m", "synthetic"},
	} {
		gitIn(t, dir, args...)
	}
	return dir, imports
}

// importersOf is the number of packages that import package s directly or transitively,
// s included.
func importersOf(imports map[int][]int, s int) int {
	seen := map[int]bool{s: true}
	for changed := true; changed; {
		changed = false
		for i, imps := range imports {
			if seen[i] {
				continue
			}
			for _, j := range imps {
				if seen[j] {
					seen[i] = true
					changed = true
					break
				}
			}
		}
	}
	return len(seen)
}
