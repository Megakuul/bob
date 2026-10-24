package eval

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// TestAddBobImports: bob.Import string literals become requirements of the root go.mod, the
// highest version wins and bob.go overrides go.mod; other forms are definition errors.
func TestAddBobImports(t *testing.T) {
	own := map[string]*bobFile{
		".":   parseBob(".", "bob.go", []byte("//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Import(\"example.com/dep\", \"v1.0.0\")\n")),
		"use": parseBob("use", "use/bob.go", []byte("//go:build bob\n\npackage use\n\nimport b \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = b.Import(\"example.com/dep\", \"v1.2.0\")\nvar _ = b.Import(\"example.com/other\", \"v0.1.0\")\n")),
		"x":   parseBob("x", "x/bob.go", []byte("//go:build bob\n\npackage x\n\nimport \"example.com/notbob\"\n\nvar _ = notbob.Import(\"example.com/ignored\", \"v1.0.0\")\n")),
	}
	m := parseGoMod([]byte("module example.com/app\n\ngo 1.26.0\n\nrequire example.com/other v0.0.1\n"))
	if err := addBobImports(&m, own); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"example.com/dep": "v1.2.0", "example.com/other": "v0.1.0"}
	if len(m.require) != len(want) {
		t.Errorf("require = %v, want %v", m.require, want)
	}
	for k, v := range want {
		if m.require[k] != v {
			t.Errorf("require[%s] = %q, want %q", k, m.require[k], v)
		}
	}

	bad := map[string]*bobFile{".": parseBob(".", "bob.go", []byte("//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar v = \"v1.0.0\"\nvar _ = bob.Import(\"example.com/dep\", v)\n"))}
	err := addBobImports(&m, bad)
	var errs ir.Errors
	if !errors.As(err, &errs) || len(errs) != 1 || !strings.Contains(err.Error(), "bob.go:8:9") ||
		!strings.Contains(err.Error(), "string literals") {
		t.Errorf("err = %v, want one definition error at bob.go:8:9", err)
	}
}

// TestEvaluateCrossRepo (R-FW-022): a fresh consumer whose go.mod does not require the
// dependency bob repository yet evaluates: bob.Import is the requirement, the module comes
// from the user's GOPROXY, and the dependency's package appears under its module label.
func TestEvaluateCrossRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluations need the sandbox and a Go toolchain")
	}
	proxy := t.TempDir()
	writeModuleZip(t, proxy, "example.com/dep", "v1.0.0", map[string]string{
		"go.mod": "module example.com/dep\n\ngo 1.26.0\n",
		"libs/bar/bob.go": `//go:build bob

package bar

import "github.com/megakuul/bob/pkg/bob"

// Lib is example.com/dep//libs/bar:lib.
var Lib = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "lib", bob.RunOpts{Cmd: []string{"true"}, Outputs: []string{"lib.txt"}})
})
`,
	})
	t.Setenv("GOPROXY", "file://"+proxy)
	t.Setenv("GOSUMDB", "off")
	modcache := filepath.Join(t.TempDir(), "modcache")
	t.Setenv("GOMODCACHE", modcache)
	t.Cleanup(func() { _ = makeWritable(modcache) }) // go mod download writes read-only trees
	root := writeRepo(t, "app", map[string]string{
		"go.mod":   "module example.com/app\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   "//go:build bob\n\npackage app\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n\nvar _ = bob.Import(\"example.com/dep\", \"v1.0.0\")\n",
		"use/bob.go": `//go:build bob

package use

import (
	"example.com/dep/libs/bar"
	"github.com/megakuul/bob/pkg/bob"
)

// Consumer is //use:consumer.
var Consumer = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "consumer", bob.RunOpts{Cmd: []string{"true"}, Inputs: map[string]bob.Tree{"bar": bar.Lib(p)}, Outputs: []string{"out"}})
})
`,
	})
	res, err := evaluate(t, root, "//use:consumer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.IR), `"example.com/dep//libs/bar:lib"`) {
		t.Errorf("IR does not name example.com/dep//libs/bar:lib:\n%s", res.IR)
	}
}

// writeModuleZip writes module@version into the file:// GOPROXY directory proxy.
func writeModuleZip(t *testing.T, proxy, module, version string, files map[string]string) {
	t.Helper()
	vdir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	zf, err := os.Create(filepath.Join(vdir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for name, body := range files {
		w, err := zw.Create(module + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		version + ".mod":  files["go.mod"],
		version + ".info": `{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`,
		"list":            version + "\n",
	} {
		if err := os.WriteFile(filepath.Join(vdir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
