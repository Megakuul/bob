// Package fixturegraph describes the S0a fixture the way a bob.go would, producing an ir.Graph.
// The helper functions stand in for bob's future typed DSL; only the IR they produce matters here.
package fixturegraph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"bobspike/s07/ir"
)

const (
	NixpkgsRev = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67" // nixos-26.05
	System     = "aarch64-linux"
)

type crate struct {
	name, version, edition string
	features               []string
}

var crates = []crate{
	{"itoa", "1.0.18", "2021", nil},
	{"hex", "0.4.3", "2018", []string{"std", "alloc"}},
}

type gomod struct{ path, version string }

var gomods = []gomod{
	{"github.com/google/uuid", "v1.6.0"},
	{"golang.org/x/sync", "v0.24.0"},
	{"google.golang.org/protobuf", "v1.36.11"}, // matches nixpkgs protoc-gen-go 1.36.11
}

// Fetches lists every download of the fixture (used by cmd/lock).
func Fetches() []ir.Fetch {
	var fs []ir.Fetch
	for _, c := range crates {
		fs = append(fs, ir.Fetch{Name: c.name + "-" + c.version + ".crate", URL: fmt.Sprintf("https://static.crates.io/crates/%s/%s-%s.crate", c.name, c.name, c.version), Unpack: "tar.gz"})
	}
	for _, m := range gomods {
		fs = append(fs, ir.Fetch{Name: goZipName(m), URL: fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.zip", m.path, m.version), Unpack: "zip"})
	}
	return fs
}

func goZipName(m gomod) string {
	return filepath.Base(m.path) + "-" + m.version + ".zip"
}

type builder struct{ g *ir.Graph }

func at() string {
	_, file, line, _ := runtime.Caller(1)
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

func (b *builder) add(p *ir.Package) *ir.Package {
	b.g.Packages = append(b.g.Packages, p)
	return p
}

// Fixture builds the full mixed graph. root is the directory containing fixture/.
func Fixture(root string, lock map[string]string) (*ir.Graph, error) {
	b := &builder{g: &ir.Graph{Version: 1, System: System, Nixpkgs: ir.Pin{Rev: NixpkgsRev}, Root: root}}

	fetch := func(f ir.Fetch) string {
		h, ok := lock[f.Name]
		if !ok {
			return ""
		}
		f.Hash = h
		label := "//third_party:" + f.Name
		b.add(&ir.Package{Label: label, Kind: ir.KindFetch, DefinedAt: at(), Fetch: &f})
		return label
	}
	fetchByName := map[string]string{}
	for _, f := range Fetches() {
		l := fetch(f)
		if l == "" {
			return nil, fmt.Errorf("lock.json has no hash for %s (run cmd/lock)", f.Name)
		}
		fetchByName[f.Name] = l
	}

	// Third-party crates, compiled per crate (fine-grained).
	crateLabels := map[string]string{}
	for _, c := range crates {
		id := c.name + "-" + c.version
		crateLabels[c.name] = b.add(&ir.Package{
			Label: "//third_party/crates:" + id, Kind: ir.KindRustCrate, DefinedAt: at(),
			Src:  &ir.Source{FromLabel: fetchByName[id+".crate"], Subdir: id},
			Rust: &ir.RustAttrs{CrateName: c.name, Version: c.version, Edition: c.edition, CrateType: "lib", Root: "src/lib.rs", Features: c.features},
		}).Label
	}

	libfoo := b.add(&ir.Package{
		Label: "//libfoo:foo", Kind: ir.KindCCLibrary, DefinedAt: at(),
		Src:     &ir.Source{Path: "fixture/libfoo"},
		Nixpkgs: []string{"openssl"},
		CC:      &ir.CCAttrs{Lang: "c++", Std: "c++20", Srcs: []string{"src/foo.cpp"}, Hdrs: "include", Shared: true, Lib: "foo"},
	})

	rcore := b.add(&ir.Package{
		Label: "//rcore:rcore", Kind: ir.KindRustCrate, DefinedAt: at(),
		Src:  &ir.Source{Path: "fixture/rcore"},
		Deps: []string{crateLabels["itoa"], crateLabels["hex"]},
		Rust: &ir.RustAttrs{CrateName: "rcore", Version: "0.1.0", Edition: "2021", CrateType: "staticlib", Root: "src/lib.rs",
			Externs: map[string]string{"itoa": crateLabels["itoa"], "hex": crateLabels["hex"]}, Hdrs: "include"},
	})

	// S7: a proc-macro (host unit) and a binary with a build script and a feature flag.
	rmac := b.add(&ir.Package{
		Label: "//rdemo:rmac", Kind: ir.KindRustCrate, DefinedAt: at(),
		Src:  &ir.Source{Path: "fixture/rmac"},
		Deps: []string{crateLabels["itoa"]},
		Rust: &ir.RustAttrs{CrateName: "rmac", Version: "0.1.0", Edition: "2021", CrateType: "proc-macro", Root: "src/lib.rs",
			Externs: map[string]string{"itoa": crateLabels["itoa"]}},
	})
	b.add(&ir.Package{
		Label: "//rdemo:rdemo", Kind: ir.KindRustCrate, DefinedAt: at(),
		Src:  &ir.Source{Path: "fixture/rdemo"},
		Deps: []string{crateLabels["itoa"], rmac.Label},
		Rust: &ir.RustAttrs{CrateName: "rdemo", Version: "0.1.0", Edition: "2021", CrateType: "bin", Root: "src/main.rs",
			BuildScript: "build.rs", Externs: map[string]string{"itoa": crateLabels["itoa"], "rmac": rmac.Label}},
	})

	stubs := b.add(&ir.Package{
		Label: "//proto:greeting_go", Kind: ir.KindProtoGo, DefinedAt: at(),
		Src:     &ir.Source{Path: "fixture/proto"},
		Nixpkgs: []string{"protobuf", "protoc-gen-go"},
		Proto:   &ir.ProtoAttrs{Files: []string{"greeting.proto"}, GoPackage: "example.com/api/gen/greetingv1"},
	})

	var reqs []ir.GoRequire
	deps := []string{libfoo.Label, rcore.Label, stubs.Label}
	for _, m := range gomods {
		l := fetchByName[goZipName(m)]
		reqs = append(reqs, ir.GoRequire{Path: m.path, Version: m.version, Label: l})
		deps = append(deps, l)
	}
	b.add(&ir.Package{
		Label: "//api:api", Kind: ir.KindGoBinary, DefinedAt: at(),
		Src:     &ir.Source{Path: "fixture/api"},
		Deps:    deps,
		Nixpkgs: []string{"go"},
		Go: &ir.GoAttrs{ModulePath: "example.com/api", GoVersion: "1.26", Main: "./cmd/api", CGo: true, Requires: reqs,
			Generated: []ir.GoGen{{Label: stubs.Label, Subdir: "gen/greetingv1"}}, Binary: "api"},
	})
	return b.g, b.g.Validate()
}

// Synthetic writes n tiny C libraries under dir (lib i depends on i-1 and i-2) and returns their graph.
// Used for evaluation-scaling measurements; root is dir.
func Synthetic(dir string, n int) (*ir.Graph, error) {
	b := &builder{g: &ir.Graph{Version: 1, System: System, Nixpkgs: ir.Pin{Rev: NixpkgsRev}, Root: dir}}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("s%05d", i)
		pd := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(pd, "include"), 0o755); err != nil {
			return nil, err
		}
		var deps []string
		body := fmt.Sprintf("int %s(void) { return %d", name, i)
		hdr := ""
		for _, j := range []int{i - 1, i - 2} {
			if j >= 0 {
				dn := fmt.Sprintf("s%05d", j)
				deps = append(deps, "//"+dn+":"+dn)
				body += " + " + dn + "()"
				hdr += fmt.Sprintf("#include \"%s.h\"\n", dn)
			}
		}
		body += "; }\n"
		files := map[string]string{
			"include/" + name + ".h": fmt.Sprintf("#pragma once\nint %s(void);\n", name),
			name + ".c":              hdr + fmt.Sprintf("#include \"%s.h\"\n", name) + body,
		}
		for f, c := range files {
			if err := os.WriteFile(filepath.Join(pd, f), []byte(c), 0o644); err != nil {
				return nil, err
			}
		}
		b.add(&ir.Package{
			Label: "//" + name + ":" + name, Kind: ir.KindCCLibrary, DefinedAt: at(),
			Src:  &ir.Source{Path: name},
			Deps: deps,
			CC:   &ir.CCAttrs{Lang: "c", Std: "c17", Srcs: []string{name + ".c"}, Hdrs: "include", Shared: false, Lib: name},
		})
	}
	return b.g, b.g.Validate()
}

func LoadLock(path string) (map[string]string, error) {
	m := map[string]string{}
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(bs, &m)
}

// GoSynthetic writes a pure-Go workspace module example.com/syn with n library packages in
// layers of `width` (each package imports two packages of the layer below and std strings/strconv)
// and a main package importing the top layer. Returns the graph (one go_binary, no requires).
func GoSynthetic(dir string, n, width int) (*ir.Graph, error) {
	b := &builder{g: &ir.Graph{Version: 1, System: System, Nixpkgs: ir.Pin{Rev: NixpkgsRev}, Root: dir}}
	mod := filepath.Join(dir, "syn")
	write := func(rel, body string) error {
		p := filepath.Join(mod, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(body), 0o644)
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
		if err := write(fmt.Sprintf("lib/%s/%s.go", name(i), name(i)), s.String()); err != nil {
			return nil, err
		}
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
	if err := write("cmd/syn/main.go", m.String()); err != nil {
		return nil, err
	}
	b.add(&ir.Package{Label: "//syn:syn", Kind: ir.KindGoBinary, DefinedAt: at(), Src: &ir.Source{Path: "syn"}, Nixpkgs: []string{"go"},
		Go: &ir.GoAttrs{ModulePath: "example.com/syn", GoVersion: "1.26", Main: "./cmd/syn", Binary: "syn"}})
	return b.g, b.g.Validate()
}
