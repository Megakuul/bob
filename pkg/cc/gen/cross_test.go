package gen

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/cc/bop"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// otherPlatform is the platform the host cross-compiles for.
func otherPlatform() string {
	if hostPlatform() == "linux-arm64" {
		return "linux-amd64"
	}
	return "linux-arm64"
}

// crossFixture is the fixture graph with every C/C++ node built for the other platform, in
// the given profile and sanitisers, and an emulator for the test.
func crossFixture(t *testing.T, profile string, sanitizers ...string) *fixture {
	t.Helper()
	f := newFixture(t)
	san, _ := json.Marshal(append([]string{}, sanitizers...))
	proj := map[string]json.RawMessage{"platform": json.RawMessage(`"` + otherPlatform() + `"`),
		"profile": json.RawMessage(`"` + profile + `"`), "sanitizers": san}
	q := &ir.Node{ID: "qemu", Kind: "nixpkgs.tool", Label: "nixpkgs:qemu-user", Nix: &ir.Nix{Attr: "qemu-user", Set: "host"}}
	f.g.Nodes = append(f.g.Nodes, q)
	f.in["qemu"] = engine.Resolved{Drv: "/nix/store/6wpyrjh2q489wizpbsw8sylbgmqsrhv2-qemu-user-10.2.4.drv",
		Outputs: map[string]string{"out": "/nix/store/6wpyrjh2q489wizpbsw8sylbgmqsrhv2-qemu-user-10.2.4"}}
	for _, n := range f.g.Nodes {
		if !strings.HasPrefix(n.Kind, "cc.") {
			continue
		}
		n.Proj = proj
		if n.ID == "mathx_test" {
			var s ccspec.Target
			if err := json.Unmarshal(n.Spec, &s); err != nil {
				t.Fatal(err)
			}
			s.Tools.Emulator = len(n.Deps)
			n.Deps = append(n.Deps, "qemu")
			n.Spec, _ = json.Marshal(s)
		}
	}
	return f
}

// TestCrossPlan: a cross build keeps the host system (R-PLAT-004), uses the prefixed compiler,
// archiver and binutils (R-FW-033), names derivations with the platform and projection tag
// (R-FW-038) and runs the test under the emulator (R-FW-039).
func TestCrossPlan(t *testing.T) {
	f := crossFixture(t, "debug", "asan")
	drvs, _, err := f.plan(t, "mathx", "pack", "zstd", "app", "mathx_test")
	if err != nil {
		t.Fatal(err)
	}
	other := otherPlatform()
	triple := map[string]string{"linux-amd64": "x86_64-unknown-linux-gnu", "linux-arm64": "aarch64-unknown-linux-gnu"}[other]
	for _, name := range []string{"cc-mathx-", "cc-pack-", "cc-app-", "ccx-zstd-", "ccrun-mathx_test-"} {
		d := drvs[name+other+"-dbg-address"]
		if d == nil {
			t.Fatalf("no derivation %s%s-dbg-address; have %v", name, other, keys(drvs))
		}
		if d.System != hostSystem() {
			t.Errorf("%s has system %s, want the host's %s", d.Name, d.System, hostSystem())
		}
	}
	var mathx, app bop.CompileLinkArgs
	job(t, drvs["cc-mathx-"+other+"-dbg-address"], &mathx)
	j := job(t, drvs["cc-app-"+other+"-dbg-address"], &app)
	if mathx.Compile[0].Argv[0] != triple+"-gcc" || mathx.Link.Argv[0] != triple+"-ar" {
		t.Errorf("mathx compile %q link %q: want %s-gcc and %s-ar", mathx.Compile[0].Argv, mathx.Link.Argv, triple, triple)
	}
	if a := mathx.Compile[0].Argv; !slices.Contains(a, "-O0") || !slices.Contains(a, "-fsanitize=address") {
		t.Errorf("mathx compile lacks the debug and asan flags: %q", a)
	}
	if app.Link.Argv[0] != triple+"-gcc" || !strings.HasSuffix(j.Fixup.Tools.Strip, "/bin/"+triple+"-strip") {
		t.Errorf("app link %q, strip %q", app.Link.Argv, j.Fixup.Tools.Strip)
	}
	var run struct {
		Argv []string `json:"argv"`
	}
	b, _ := json.Marshal(drvs["ccrun-mathx_test-"+other+"-dbg-address"].Structured["bobSpec"].(map[string]any)["args"])
	if err := json.Unmarshal(b, &run); err != nil || len(run.Argv) < 2 || !strings.HasSuffix(run.Argv[0], "/bin/qemu-"+strings.Split(triple, "-")[0]) {
		t.Errorf("the cross test does not run under qemu: %q (%v)", run.Argv, err)
	}
	var x bop.ExternalArgs
	job(t, drvs["ccx-zstd-"+other+"-dbg-address"], &x)
	if x.Env["CC"] != triple+"-gcc" || x.Env["AR"] != triple+"-ar" {
		t.Errorf("External env %v: want the cross CC and AR", x.Env)
	}
}

// TestCrossSanitiserRunpath: a sanitised cross program links with the cross gcc-lib directory
// in its RUNPATH (R-FW-037).
func TestCrossSanitiserRunpath(t *testing.T) {
	f := crossFixture(t, "release", "asan")
	lib := &ir.Node{ID: "gcclib", Kind: "nixpkgs.tool", Label: "nixpkgs:cc", Nix: &ir.Nix{Attr: "pkgsCross.gnu64.stdenv.cc.cc", Set: "host"}}
	f.g.Nodes = append(f.g.Nodes, lib)
	libOut := "/nix/store/54cd56yscrlh84g0fhn67gjpl9cd1w22-x86_64-unknown-linux-gnu-gcc-15.3.0-lib"
	f.in["gcclib"] = engine.Resolved{Drv: "/nix/store/2arc275y6y91lna8fdyjy8daynbq9bxw-x86_64-unknown-linux-gnu-gcc-15.3.0.drv",
		Outputs: map[string]string{"out": "/nix/store/4kb7pr4klijmssxpry0in21y3l60dlj8-x86_64-unknown-linux-gnu-gcc-15.3.0", "lib": libOut}}
	n := f.node("app")
	var s ccspec.Target
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		t.Fatal(err)
	}
	s.Tools.CCLib = len(n.Deps)
	n.Deps = append(n.Deps, "gcclib")
	n.Spec, _ = json.Marshal(s)
	drvs, _, err := f.plan(t, "mathx", "pack", "zstd", "app")
	if err != nil {
		t.Fatal(err)
	}
	var app bop.CompileLinkArgs
	job(t, drvs["cc-app-"+otherPlatform()+"-address"], &app)
	if !slices.Contains(app.Link.Argv, "-Wl,-rpath,"+libOut+"/lib") {
		t.Errorf("the sanitised cross link lacks the gcc-lib RUNPATH: %q", app.Link.Argv)
	}
}
