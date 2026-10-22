// Package layering_test holds TestLayering, the CI check for R-ARCH-002 and
// R-ARCH-003. It is the one place the layer table is encoded.
package layering_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/megakuul/bob"

type layer string

const (
	l0      layer = "L0 core API"
	l1      layer = "L1 sources"
	l2      layer = "L2 ecosystem APIs"
	l3      layer = "L3 output APIs"
	e0      layer = "E0 engine primitives"
	e1      layer = "E1 engine services"
	e2      layer = "E2 generators"
	e3      layer = "E3 orchestration"
	builder layer = "builder"
	glue    layer = "glue"
)

// The R-ARCH-003 table. A listed package covers its subpackages, except that
// <pkg>/gen is E2 and <pkg>/bop is builder for every L2/L3 package.
var (
	l0Pkgs = []string{"pkg/bob"} // pkg/bob/ext and the other pkg/bob/... helpers (diag, label)
	l1Pkgs = []string{"pkg/nixpkgs"}
	l2Pkgs = []string{"pkg/cc", "pkg/golang", "pkg/rust", "pkg/npm", "pkg/ocaml", "pkg/proto",
		"pkg/connect", "pkg/graphql", "pkg/openapi", "pkg/services"}
	l3Pkgs      = []string{"pkg/output/oci", "pkg/output/appimage", "pkg/output/tarball", "pkg/release"}
	e0Pkgs      = []string{"pkg/engine/drv", "pkg/engine/nar", "pkg/engine/ir"}
	e1Pkgs      = []string{"pkg/engine/store", "pkg/engine/fetch", "pkg/engine/lock", "pkg/engine/nixidx", "pkg/engine/srcs"}
	e3Pkgs      = []string{"pkg/engine/eval", "pkg/engine/build", "pkg/engine/translate"}
	builderPkgs = []string{"pkg/builder"}
	gluePkgs    = []string{"pkg/cli", "cmd/bob"}

	// mayImport lists, per layer, the bob layers it may import besides its own.
	mayImport = map[layer][]layer{
		l0:      nil,
		l1:      {l0},
		l2:      {l0, l1}, // other L2 APIs too; Go itself rejects the cycles
		l3:      {l0, l1, l2},
		e0:      {l0},
		e1:      {e0, l0},
		e2:      {e0, e1, l0, l1, l2, l3},
		e3:      {e0, e1, e2, l0},
		builder: {e0},
		glue:    {l0, l1, l2, l3, e0, e1, e2, e3, builder},
	}
	// thirdParty lists the non-std modules a layer may import.
	thirdParty = map[layer][]string{
		e1: {"golang.org/x/sys", "github.com/klauspost/compress", "github.com/ulikunitz/xz"},
	}

	// serverCode is what the framework never depends on, even transitively (R-ARCH-002).
	serverCode = regexp.MustCompile(`^github.com/megakuul/bob/(internal|proto|cmd/bob-server)(/|$)`)
)

func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func withinAny(p string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return within(p, d) })
}

// classify returns the layer of a bob package given relative to the module
// root ("pkg/cc/gen"); ok is false for packages the table does not list.
func classify(rel string) (l layer, ok bool) {
	if parent, leaf, _ := strings.Cut(reverse(rel), "/"); parent != "" {
		eco := reverse(leaf)
		switch reverse(parent) {
		case "gen":
			if withinAny(eco, l2Pkgs) || withinAny(eco, l3Pkgs) {
				return e2, true
			}
		case "bop":
			if withinAny(eco, l2Pkgs) || withinAny(eco, l3Pkgs) {
				return builder, true
			}
		}
	}
	for _, t := range []struct {
		l    layer
		pkgs []string
	}{{l0, l0Pkgs}, {l1, l1Pkgs}, {l2, l2Pkgs}, {l3, l3Pkgs}, {e0, e0Pkgs}, {e1, e1Pkgs},
		{e3, e3Pkgs}, {builder, builderPkgs}, {glue, gluePkgs}} {
		if withinAny(rel, t.pkgs) {
			return t.l, true
		}
	}
	if rel == "pkg/engine" {
		return e2, true
	}
	return "", false
}

// reverse splits off the last path element: reverse("a/b/c") == "c/b/a".
func reverse(p string) string {
	parts := strings.Split(p, "/")
	slices.Reverse(parts)
	return strings.Join(parts, "/")
}

func isStd(p string) bool { return !strings.Contains(strings.Split(p, "/")[0], ".") }

// checkImports returns the R-ARCH-002/003 violations of one package's
// direct imports.
func checkImports(pkg string, imports []string) []string {
	rel, ok := strings.CutPrefix(pkg, module+"/")
	if !ok {
		return nil
	}
	from, ok := classify(rel)
	if !ok {
		// "pkg" itself only holds this test. cmd/bob-builder is not in the
		// table; R-ARCH-002 alone applies to it.
		if within(rel, "pkg") && rel != "pkg" {
			return []string{fmt.Sprintf("%s is not in the R-ARCH-003 layer table", pkg)}
		}
		return nil
	}
	var bad []string
	for _, imp := range imports {
		switch {
		case isStd(imp):
		case serverCode.MatchString(imp):
			bad = append(bad, fmt.Sprintf("%s (%s) imports server code %s (R-ARCH-002)", pkg, from, imp))
		case within(imp, module):
			to, ok := classify(strings.TrimPrefix(imp, module+"/"))
			if ok && to != from && !slices.Contains(mayImport[from], to) {
				bad = append(bad, fmt.Sprintf("%s (%s) imports %s (%s) (R-ARCH-003)", pkg, from, imp, to))
			}
		case !withinAny(imp, thirdParty[from]):
			bad = append(bad, fmt.Sprintf("%s (%s) imports third-party %s, which its layer does not list (R-ARCH-003)", pkg, from, imp))
		}
	}
	return bad
}

type listedPkg struct {
	ImportPath string
	Imports    []string
}

// TestLayering checks the real tree: no framework package depends on server
// code (R-ARCH-002) and every pkg/ import follows the layer table (R-ARCH-003).
func TestLayering(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json=ImportPath,Imports", "./pkg/...", "./cmd/bob", "./cmd/bob-builder")
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var bad []string
	for {
		var p listedPkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if serverCode.MatchString(p.ImportPath) {
			bad = append(bad, fmt.Sprintf("framework depends on server package %s (R-ARCH-002)", p.ImportPath))
		}
		bad = append(bad, checkImports(p.ImportPath, p.Imports)...)
	}
	for _, b := range bad {
		t.Error(b)
	}
}

// TestLayeringRejects proves the check catches planted violations.
func TestLayeringRejects(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		imports []string
		wantBad bool
	}{
		{"upward L0 to L2", "pkg/bob", []string{module + "/pkg/cc"}, true},
		{"upward L2 to L3", "pkg/cc", []string{module + "/pkg/output/oci"}, true},
		{"API into engine", "pkg/cc", []string{module + "/pkg/engine/ir"}, true},
		{"API into builder", "pkg/rust", []string{module + "/pkg/builder/bspec"}, true},
		{"E0 to E1", "pkg/engine/drv", []string{module + "/pkg/engine/store"}, true},
		{"builder to E1", "pkg/cc/bop", []string{module + "/pkg/engine/fetch"}, true},
		{"server code", "pkg/cli", []string{module + "/internal/store"}, true},
		{"generated proto", "pkg/engine/eval", []string{module + "/proto/bob/build"}, true},
		{"unlisted third party", "pkg/cc", []string{"github.com/spf13/cobra"}, true},
		{"unlisted package", "pkg/foo", nil, true},
		{"L2 to L0 and L1", "pkg/cc", []string{module + "/pkg/bob", module + "/pkg/nixpkgs", "fmt"}, false},
		{"L2 to L2", "pkg/connect", []string{module + "/pkg/proto"}, false},
		{"gen to API", "pkg/cc/gen", []string{module + "/pkg/cc", module + "/pkg/engine", module + "/pkg/engine/store"}, false},
		{"E1 listed third party", "pkg/engine/store", []string{"github.com/klauspost/compress/zstd", "golang.org/x/sys/unix"}, false},
		{"glue to anything in pkg", "pkg/cli", []string{module + "/pkg/engine/build", module + "/pkg/builder"}, false},
		{"bspec within builder", "pkg/builder", []string{module + "/pkg/builder/bspec", module + "/pkg/engine/drv"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := checkImports(module+"/"+tt.pkg, tt.imports)
			if got := len(bad) > 0; got != tt.wantBad {
				t.Errorf("checkImports(%s, %v) = %v, want violation: %v", tt.pkg, tt.imports, bad, tt.wantBad)
			}
		})
	}
}
