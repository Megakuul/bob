package resolve

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeMod is one module version of a test proxy: its go.mod and its files (module-relative).
type fakeMod struct {
	path, version, gomod string
	files                map[string]string
}

// writeProxy writes a file:// GOPROXY directory serving mods.
func writeProxy(t *testing.T, mods []fakeMod) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range mods {
		mv := Module{m.path, m.version}
		dir := filepath.Join(root, filepath.FromSlash(EscapePath(m.path)), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, EscapePath(m.version)+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		files := map[string]string{"go.mod": m.gomod}
		for k, v := range m.files {
			files[k] = v
		}
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			w, err := zw.Create(mv.String() + "/" + n)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(files[n]))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := os.WriteFile(filepath.Join(dir, EscapePath(m.version)+".mod"), []byte(m.gomod), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := WriteVersionInfo(root, mv); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func gomod(path, goV string, reqs ...string) string {
	s := "module " + path + "\n"
	if goV != "" {
		s += "\ngo " + goV + "\n"
	}
	for _, r := range reqs {
		s += "\nrequire " + r + "\n"
	}
	return s
}

// scenario is a workspace module with sources and a test proxy.
type scenario struct {
	name    string
	mods    []fakeMod
	require []Module
	files   map[string]string // workspace files
	want    []string          // expected build list
}

func scenarios() []scenario {
	pkg := func(name string, imports ...string) string {
		s := "package " + name + "\n"
		for _, i := range imports {
			s += "\nimport _ \"" + i + "\"\n"
		}
		return s
	}
	return []scenario{
		{
			name: "pruned",
			mods: []fakeMod{
				{"example.org/a", "v1.0.0", gomod("example.org/a", "1.21", "example.org/b v1.0.0", "example.org/c v1.0.0"), map[string]string{"a.go": pkg("a", "example.org/b")}},
				{"example.org/b", "v1.0.0", gomod("example.org/b", "1.21", "example.org/d v1.2.0"), map[string]string{"b.go": pkg("b")}},
				{"example.org/c", "v1.0.0", gomod("example.org/c", "1.21", "example.org/e v1.0.0"), map[string]string{"c.go": pkg("c")}},
				{"example.org/d", "v1.2.0", gomod("example.org/d", "1.21", "example.org/f v1.0.0"), map[string]string{"d.go": pkg("d")}},
			},
			require: []Module{{"example.org/a", "v1.0.0"}},
			files:   map[string]string{"main.go": "package main\n\nimport _ \"example.org/a\"\n\nfunc main() {}\n"},
			want:    []string{"example.org/a@v1.0.0", "example.org/b@v1.0.0", "example.org/c@v1.0.0", "example.org/d@v1.2.0"},
		},
		{
			name: "unpruned",
			mods: []fakeMod{
				{"example.org/old", "v1.0.0", gomod("example.org/old", "1.16", "example.org/x v1.0.0"), map[string]string{"old.go": pkg("old")}},
				{"example.org/x", "v1.0.0", gomod("example.org/x", "1.21", "example.org/y v1.1.0"), map[string]string{"x.go": pkg("x")}},
				{"example.org/y", "v1.1.0", gomod("example.org/y", "1.21", "example.org/z v1.0.0"), map[string]string{"y.go": pkg("y")}},
				{"example.org/y", "v1.0.0", gomod("example.org/y", "1.21"), map[string]string{"y.go": pkg("y")}},
				{"example.org/z", "v1.0.0", gomod("example.org/z", ""), map[string]string{"z.go": pkg("z")}},
			},
			require: []Module{{"example.org/old", "v1.0.0"}, {"example.org/y", "v1.0.0"}},
			files: map[string]string{"main.go": "package main\n\nimport _ \"example.org/old\"\n\nfunc main() {}\n",
				"lib/lib_test.go": "package lib\n\nimport _ \"example.org/y\"\n"},
			want: []string{"example.org/old@v1.0.0", "example.org/x@v1.0.0", "example.org/y@v1.1.0", "example.org/z@v1.0.0"},
		},
		{
			name: "upgrade-and-tags",
			mods: []fakeMod{
				{"example.org/p", "v1.2.0", gomod("example.org/p", "1.22", "example.org/q v1.5.0"), map[string]string{"p.go": pkg("p", "example.org/q/sub")}},
				{"example.org/q", "v1.5.0", gomod("example.org/q", "1.22"), map[string]string{"sub/s.go": pkg("sub"), "q.go": pkg("q")}},
				{"example.org/q", "v1.4.0", gomod("example.org/q", "1.22"), map[string]string{"sub/s.go": pkg("sub"), "q.go": pkg("q")}},
				{"example.org/w", "v1.0.0", gomod("example.org/w", "1.22"), map[string]string{"w.go": pkg("w")}},
				{"example.org/ign", "v1.0.0", gomod("example.org/ign", "1.22"), map[string]string{"i.go": pkg("ign")}},
			},
			require: []Module{{"example.org/p", "v1.2.0"}, {"example.org/q", "v1.4.0"}, {"example.org/w", "v1.0.0"}},
			files: map[string]string{
				"main.go":    "package main\n\nimport _ \"example.org/p\"\n\nfunc main() {}\n",
				"win.go":     "//go:build windows\n\npackage main\n\nimport _ \"example.org/w\"\n",
				"ignored.go": "//go:build ignore\n\npackage main\n\nimport _ \"example.org/ign\"\n",
			},
			want: []string{"example.org/p@v1.2.0", "example.org/q@v1.5.0", "example.org/w@v1.0.0"},
		},
	}
}

// goModFor writes the go.mod and go.sum bob would write for a resolved workspace.
func goModFor(t *testing.T, dir, path string, res *Result, decl []Module) {
	t.Helper()
	declared := map[string]bool{}
	for _, m := range decl {
		declared[m.Path] = true
	}
	packages := map[string]bool{}
	for _, m := range res.Modules {
		if m.Packages {
			packages[m.Path] = true
		}
	}
	bl := res.BuildLists[path]
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo 1.26.0\n", path)
	for _, r := range Roots(bl, declared, packages) {
		fmt.Fprintf(&b, "\nrequire %s %s\n", r.Path, r.Version)
	}
	inBL := map[string]bool{}
	for _, m := range bl {
		inBL[m.Path] = true
	}
	var sum []string
	for _, m := range res.Modules {
		if !inBL[m.Path] {
			continue
		}
		sum = append(sum, fmt.Sprintf("%s %s/go.mod %s", m.Path, m.Version, m.ModH1))
		if m.Packages {
			h1, err := m.Zip.H1()
			if err != nil {
				t.Fatal(err)
			}
			sum = append(sum, fmt.Sprintf("%s %s %s", m.Path, m.Version, h1))
		}
	}
	sort.Strings(sum)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(b.String()), 0o644)
	os.WriteFile(filepath.Join(dir, "go.sum"), []byte(strings.Join(sum, "\n")+"\n"), 0o644)
}

// TestBuildListEqualsGoCommand resolves each scenario and checks with the go command on PATH
// that `go list -m all` lists exactly bob's build list and `go build ./...` works offline with
// bob's go.mod/go.sum (R-GO-002b on synthetic graphs: pruning, unpruned go.mod files,
// upgrades by dependencies, build tags, test imports).
func TestBuildListEqualsGoCommand(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			proxyDir := writeProxy(t, sc.mods)
			p, err := NewProxy("file://" + proxyDir)
			if err != nil {
				t.Fatal(err)
			}
			ws := t.TempDir()
			for n, c := range sc.files {
				os.MkdirAll(filepath.Join(ws, filepath.Dir(n)), 0o755)
				os.WriteFile(filepath.Join(ws, n), []byte(c), 0o644)
			}
			res, err := Resolve(context.Background(), Request{Proxy: p, TmpDir: t.TempDir(),
				Workspaces: []Workspace{{Path: "example.com/ws", Go: "1.26", Dir: ws, BobGo: "bob.go", Require: sc.require}}})
			if err != nil {
				t.Fatal(err)
			}
			defer res.Close()
			var got []string
			for _, m := range res.BuildLists["example.com/ws"] {
				got = append(got, m.String())
			}
			if strings.Join(got, " ") != strings.Join(sc.want, " ") {
				t.Errorf("build list %v, want %v", got, sc.want)
			}
			goModFor(t, ws, "example.com/ws", res, sc.require)
			env := append(os.Environ(), "GOPROXY=file://"+proxyDir, "GOFLAGS=-mod=readonly", "GOSUMDB=off",
				"GOTOOLCHAIN=local", "GOWORK=off", "GOPATH="+t.TempDir(), "GOMODCACHE="+t.TempDir(), "CGO_ENABLED=0")
			run := func(args ...string) string {
				cmd := exec.Command(goBin, args...)
				cmd.Dir, cmd.Env = ws, env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("go %v: %v\n%s\ngo.mod:\n%s", args, err, out, readFileT(t, filepath.Join(ws, "go.mod")))
				}
				return string(out)
			}
			var goList []string
			for _, l := range strings.Split(strings.TrimSpace(run("list", "-m", "all")), "\n")[1:] {
				goList = append(goList, strings.Join(strings.Fields(l), "@"))
			}
			if strings.Join(goList, " ") != strings.Join(got, " ") {
				t.Errorf("go list -m all: %v\nbob: %v", goList, got)
			}
			run("build", "./...")
			run("vet", "./...")
		})
	}
}

func readFileT(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestToolchainAndNotFound(t *testing.T) {
	proxyDir := writeProxy(t, []fakeMod{
		{"github.com/foo/bar", "v1.2.0", gomod("github.com/foo/bar", "1.27.0"), map[string]string{"bar.go": "package bar\n"}},
	})
	p, _ := NewProxy("file://" + proxyDir)
	_, err := Resolve(context.Background(), Request{Proxy: p, TmpDir: t.TempDir(), Workspaces: []Workspace{{
		Path: "example.com/api", Go: "1.26", BobGo: "api/bob.go", Require: []Module{{"github.com/foo/bar", "v1.2.0"}}}}})
	var te *ToolchainError
	if !errors.As(err, &te) || err.Error() != "github.com/foo/bar@v1.2.0 needs go 1.27.0, but example.com/api uses go 1.26 (set Go in api/bob.go)" {
		t.Errorf("toolchain error: %v", err)
	}
	_, err = Resolve(context.Background(), Request{Proxy: p, TmpDir: t.TempDir(), Workspaces: []Workspace{{
		Path: "example.com/api", Go: "1.26", BobGo: "api/bob.go", Require: []Module{{"golang.org/x/net", "v0.99.0"}}}}})
	var nf *NotFoundError
	if !errors.As(err, &nf) || !strings.Contains(err.Error(), "golang.org/x/net@v0.99.0: not found on ") {
		t.Errorf("not found error: %v", err)
	}
}

// TestUpdateAndPins: a plain resolution keeps the previous pins; Update moves the named
// module to its latest version and its requirements with it (bob lock --update).
func TestUpdateAndPins(t *testing.T) {
	proxyDir := writeProxy(t, []fakeMod{
		{"golang.org/x/text", "v0.1.0", gomod("golang.org/x/text", "1.20"), nil},
		{"golang.org/x/text", "v0.2.0", gomod("golang.org/x/text", "1.20"), nil},
		{"golang.org/x/net", "v0.1.0", gomod("golang.org/x/net", "1.20", "golang.org/x/text v0.1.0"), nil},
		{"golang.org/x/net", "v0.2.0", gomod("golang.org/x/net", "1.20", "golang.org/x/text v0.2.0"), nil},
		{"golang.org/x/crypto", "v0.5.0", gomod("golang.org/x/crypto", "1.20"), nil},
	})
	p, _ := NewProxy("file://" + proxyDir)
	decl := []Module{{"golang.org/x/net", "v0.1.0"}, {"golang.org/x/crypto", "v0.5.0"}}
	resolve := func(prev []Module, update ...string) []string {
		res, err := Resolve(context.Background(), Request{Proxy: p, TmpDir: t.TempDir(), Update: update,
			Workspaces: []Workspace{{Path: "example.com/tools", Go: "1.26", Require: decl, Prev: prev}}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		var out []string
		for _, m := range res.BuildLists["example.com/tools"] {
			out = append(out, m.String())
		}
		return out
	}
	first := resolve(nil)
	if want := "golang.org/x/crypto@v0.5.0 golang.org/x/net@v0.1.0 golang.org/x/text@v0.1.0"; strings.Join(first, " ") != want {
		t.Fatalf("first: %v", first)
	}
	upd := resolve(nil, "golang.org/x/net")
	if want := "golang.org/x/crypto@v0.5.0 golang.org/x/net@v0.2.0 golang.org/x/text@v0.2.0"; strings.Join(upd, " ") != want {
		t.Fatalf("update: %v", upd)
	}
	var pins []Module
	for _, s := range upd {
		p, v, _ := strings.Cut(s, "@")
		pins = append(pins, Module{p, v})
	}
	if again := resolve(pins); strings.Join(again, " ") != strings.Join(upd, " ") {
		t.Errorf("plain resolution after the update lost the pins: %v", again)
	}
}

func TestSemverCompare(t *testing.T) {
	order := []string{"v0.0.0-20200101000000-abcdefabcdef", "v0.1.0-alpha", "v0.1.0-alpha.1", "v0.1.0-alpha.beta", "v0.1.0-beta.2", "v0.1.0-beta.11", "v0.1.0", "v1.0.0", "v1.10.0", "v2.0.0+incompatible"}
	for i := 0; i+1 < len(order); i++ {
		if Compare(order[i], order[i+1]) >= 0 || Compare(order[i+1], order[i]) <= 0 {
			t.Errorf("%s < %s does not hold", order[i], order[i+1])
		}
	}
	for _, bad := range []string{"1.0.0", "v1.0", "v01.0.0", "v1.0.0-01", "v1.0.0+meta"} {
		if ValidVersion(bad) {
			t.Errorf("%q is valid", bad)
		}
	}
	gorder := []string{"1.16", "1.21", "1.21rc1", "1.21.0", "1.21.3", "1.26", "1.26.0", "1.27.0"}
	for i := 0; i+1 < len(gorder); i++ {
		if GoCompare(gorder[i], gorder[i+1]) >= 0 {
			t.Errorf("go %s < %s does not hold", gorder[i], gorder[i+1])
		}
	}
}

func TestModH1(t *testing.T) {
	// go.sum line of github.com/google/uuid v1.6.0 (sum.golang.org).
	if got := ModH1([]byte("module github.com/google/uuid\n")); got != "h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=" {
		t.Errorf("ModH1 = %s", got)
	}
}
