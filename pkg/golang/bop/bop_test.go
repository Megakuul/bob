package bop

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder"
)

// goroot is the toolchain of the test run (the go command on PATH).
func goroot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Skipf("no go command: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// runJob runs one job through builder.Main like Nix does: structured attributes in a file,
// NIX_BUILD_TOP, outputs as absolute paths.
func runJob(t *testing.T, top, kind string, args any, outputs map[string]string) (int, string) {
	t.Helper()
	job := map[string]any{"v": 1, "kind": kind, "label": "//t:t", "outputs": []string{"out"}, "args": args,
		"fixup": map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []string{},
			"tools": map[string]string{"strip": "", "objcopy": "", "patchelf": ""}}}
	attrs, err := json.Marshal(map[string]any{"bobSpec": job, "outputs": outputs})
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(top, ".attrs.json")
	if err := os.WriteFile(f, attrs, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIX_ATTRS_JSON_FILE", f)
	t.Setenv("NIX_BUILD_TOP", top)
	kinds := builder.Core()
	for k, v := range Kinds() {
		kinds[k] = v
	}
	var stdout, stderr bytes.Buffer
	code := builder.Main([]string{kind}, kinds, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

// stdConfig writes an importcfg for the std packages fmt needs, from the test toolchain's
// export data (what kind go-std produces).
func stdConfig(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}", "fmt", "strings", "embed")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list std: %v", err)
	}
	p := filepath.Join(dir, "importcfg")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompileAndLink(t *testing.T) {
	root := goroot(t)
	top := t.TempDir()
	src := filepath.Join(top, "src")
	files := map[string]string{
		"lib/lib.go":     "package lib\n\nimport \"strings\"\n\n// Up upper-cases.\nfunc Up(s string) string { return strings.ToUpper(s) }\n",
		"main/main.go":   "package main\n\nimport (\n\t_ \"embed\"\n\t\"fmt\"\n\n\t\"example.com/m/lib\"\n)\n\n//go:embed hello.txt\nvar hello string\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(lib.Up(hello), version) }\n",
		"main/hello.txt": "hi",
	}
	for rel, body := range files {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(top, "out")
	tc := Toolchain{GoRoot: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Path: []string{}}
	std := stdConfig(t, top)
	args := BuildArgs{Toolchain: tc, Std: std, Packages: []Package{
		{ImportPath: "example.com/m/lib", Name: "lib", Flags: []string{"-p", "example.com/m/lib", "-lang=go1.26"},
			Dir: filepath.Join(src, "lib"), GoFiles: []string{"lib.go"}, Imports: map[string]string{},
			Trim: "example.com/m/lib", Out: out + "/pkg/example.com/m/lib.a"},
		{ImportPath: "example.com/m/main", Name: "main", Flags: []string{"-p", "main", "-lang=go1.26"},
			Dir: filepath.Join(src, "main"), GoFiles: []string{"main.go"},
			Imports: map[string]string{"example.com/m/lib": out + "/pkg/example.com/m/lib.a"},
			Embed:   &Embed{Patterns: map[string][]string{"hello.txt": {"hello.txt"}}, Files: []string{"hello.txt"}},
			Trim:    "example.com/m/main", Out: out + "/pkg/example.com/m/main.a"},
	}, Link: &Link{Main: out + "/pkg/example.com/m/main.a", Packages: map[string]string{
		"example.com/m/lib": out + "/pkg/example.com/m/lib.a", "example.com/m/main": out + "/pkg/example.com/m/main.a"},
		Out: out + "/bin/m", Mode: "exe", X: []string{"main.version=7"}}}
	code, log := runJob(t, top, "go-link", args, map[string]string{"out": out})
	if code != 0 {
		t.Fatalf("go-link exited %d:\n%s", code, log)
	}
	got, err := exec.Command(out + "/bin/m").Output()
	if err != nil || string(got) != "HI 7\n" {
		t.Errorf("the linked program printed %q (%v), want \"HI 7\"", got, err)
	}
	// The source directory must not reach the binary (trimmed paths).
	b, _ := os.ReadFile(out + "/bin/m")
	if bytes.Contains(b, []byte(src)) {
		t.Errorf("the binary names the source directory %s", src)
	}
}

func TestInvalidJobs(t *testing.T) {
	top := t.TempDir()
	out := filepath.Join(top, "out")
	for name, args := range map[string]any{
		"unknown field": map[string]any{"goroot": "/x", "goos": "linux", "goarch": "arm64", "bogus": 1},
		"no packages":   BuildArgs{Toolchain: Toolchain{GoRoot: "/x", GOOS: "linux", GOARCH: "arm64"}, Std: "/x/importcfg"},
		"relative root": BuildArgs{Toolchain: Toolchain{GoRoot: "x", GOOS: "linux", GOARCH: "arm64"}, Std: "/x"},
	} {
		if code, log := runJob(t, top, "go-compile", args, map[string]string{"out": out}); code != builder.ExitInvalid {
			t.Errorf("%s: exit %d, want %d (invalid job)\n%s", name, code, builder.ExitInvalid, log)
		}
	}
}

func TestLevels(t *testing.T) {
	pkgs := []Package{
		{ImportPath: "a", Out: "/o/a.a", Imports: map[string]string{}},
		{ImportPath: "b", Out: "/o/b.a", Imports: map[string]string{"a": "/o/a.a"}},
		{ImportPath: "c", Out: "/o/c.a", Imports: map[string]string{"x": "/other/x.a"}},
		{ImportPath: "d", Out: "/o/d.a", Imports: map[string]string{"b": "/o/b.a", "c": "/o/c.a"}},
	}
	var got []string
	for _, l := range levels(pkgs) {
		var names []string
		for _, p := range l {
			names = append(names, p.ImportPath)
		}
		got = append(got, strings.Join(names, ","))
	}
	if strings.Join(got, " | ") != "a,c | b | d" {
		t.Errorf("levels = %v", got)
	}
}
