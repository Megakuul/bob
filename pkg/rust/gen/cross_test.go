package gen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/rust/bop"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
)

const (
	xccOut  = "/nix/store/43jyj7s1b10awhkkilbfxsaq3g9i1gdi-gcc-wrapper-15.3.0"
	xbinOut = "/nix/store/53jyj7s1b10awhkkilbfxsaq3g9i1gdi-binutils-wrapper-2.45"
	stdOut  = "/nix/store/63jyj7s1b10awhkkilbfxsaq3g9i1gdi-rustc-1.95.0"
	qemuOut = "/nix/store/73jyj7s1b10awhkkilbfxsaq3g9i1gdi-qemu-user-10.2.4"
)

// crossFixture is fixture with every crate and test node built for the other platform: the
// evaluator's cross deps (cross C compiler, binutils, target rustc; qemu for the test).
func crossFixture(t *testing.T) (*ir.Graph, engine.PlanRequest, bob.Toolchain) {
	t.Helper()
	g, req := fixture(t, "")
	host := engine.HostPlatform()
	other := bob.LinuxAMD64
	if host == bob.LinuxAMD64 {
		other = bob.LinuxARM64
	}
	tc := bob.ToolchainFor(host, other)
	proj := json.RawMessage(`"` + string(other) + `"`)
	for _, id := range []string{"nixpkgs:xcc", "nixpkgs:xbin", "nixpkgs:std", "nixpkgs:qemu"} {
		g.Nodes = append(g.Nodes, &ir.Node{ID: id, Kind: "nixpkgs.tool", Label: id, Nix: &ir.Nix{Attr: id}})
	}
	req.Inputs["nixpkgs:xcc"] = engine.Resolved{Outputs: map[string]string{"out": xccOut}}
	req.Inputs["nixpkgs:xbin"] = engine.Resolved{Outputs: map[string]string{"out": xbinOut}}
	req.Inputs["nixpkgs:std"] = engine.Resolved{Outputs: map[string]string{"out": stdOut}}
	req.Inputs["nixpkgs:qemu"] = engine.Resolved{Outputs: map[string]string{"out": qemuOut}}
	for _, n := range g.Nodes {
		switch n.Kind {
		case rsspec.KindCrate:
			n.Proj = map[string]json.RawMessage{"platform": proj}
			var s rsspec.Crate
			if err := json.Unmarshal(n.Spec, &s); err != nil {
				t.Fatal(err)
			}
			at := len(n.Deps)
			n.Deps = append(n.Deps, "nixpkgs:xcc", "nixpkgs:xbin", "nixpkgs:std")
			s.Cross = &rsspec.Cross{Triple: tc.Triple, Prefix: tc.Prefix, CC: at, Binutils: at + 1, Std: at + 2}
			n.Spec = rawJSON(t, s)
		case rsspec.KindTest:
			n.Proj = map[string]json.RawMessage{"platform": proj}
			n.Deps = append(n.Deps, "nixpkgs:qemu")
			n.Spec = rawJSON(t, rsspec.Test{Crate: 0, Args: []string{}, Emulator: 1, EmulatorCmd: tc.EmulatorCmd})
		}
	}
	return g, req, tc
}

func jobOf(t *testing.T, d *drv.Derivation) bspec.Job {
	t.Helper()
	var j bspec.Job
	raw, _ := json.Marshal(d.Structured["bobSpec"])
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

// R-FW-033/034/035: a cross plan compiles target units with --target, the rust-std sysroot and
// the cross linker; host units (the proc-macro, build-script compiles, their dependencies) are
// the same derivations as in the native plan; tests run under the emulator.
func TestPlanCross(t *testing.T) {
	g, req := fixture(t, "")
	req.Nodes = nodesOf(g, "core@"+platform(), "mac@", "app@", "core_test@")
	native, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	nativeDrvs := map[string]bool{}
	for _, d := range native.Drvs {
		p, _ := d.Path()
		nativeDrvs[p] = true
	}

	xg, xreq, tc := crossFixture(t)
	xreq.Nodes = nodesOf(xg, "core@"+platform(), "mac@", "app@", "core_test@")
	cross, err := Ecosystem{}.Plan(context.Background(), xreq)
	if err != nil {
		t.Fatal(err)
	}
	var std string
	shared := map[string]bool{}
	for _, d := range cross.Drvs {
		p, _ := d.Path()
		j := jobOf(t, d)
		if d.System != engine.HostSystem() {
			t.Errorf("%s: system %s, want the host's", d.Name, d.System)
		}
		if nativeDrvs[p] {
			shared[d.Name] = true
		}
		if j.Kind == "rust-std" {
			var a bop.RustStdArgs
			strictDecode(t, d, &a)
			if a.From != stdOut || a.Triple != tc.Triple || d.Name != "rust-std-"+tc.Triple {
				t.Errorf("rust-std job %s: %+v", d.Name, a)
			}
			for _, o := range d.Outputs {
				std = o.Path
			}
		}
	}
	if std == "" {
		t.Fatalf("no rust-std-%s derivation", tc.Triple)
	}
	// Host units are shared with the native plan: the proc-macro, proc-macro2 (host) with its
	// build script and run, unicode-ident (host), core's build-script compile.
	for _, name := range []string{"mac-host", "proc-macro2-1.0.101-host", "proc-macro2-1.0.101-host-build-script",
		"proc-macro2-1.0.101-host-build-run", "unicode-ident-1.0.19-host", "core-build-script"} {
		if !shared[name] {
			t.Errorf("host unit %s differs from the native plan (shared: %v)", name, shared)
		}
	}
	for _, d := range cross.Drvs {
		j := jobOf(t, d)
		switch j.Kind {
		case "rustc":
			var a bop.RustcArgs
			strictDecode(t, d, &a)
			host := strings.HasSuffix(d.Name, "-host") || strings.HasSuffix(d.Name, "-build-script")
			if host && (a.Target != "" || a.Sysroot != "" || a.Linker != "") {
				t.Errorf("host unit %s compiles for the target: %+v", d.Name, a)
			}
			if !host && (a.Target != tc.Triple || a.Sysroot != std || a.Linker != tc.Prefix+"cc" || a.Path[1] != xccOut+"/bin") {
				t.Errorf("target unit %s: target %q sysroot %q linker %q path %v", d.Name, a.Target, a.Sysroot, a.Linker, a.Path)
			}
		case "rust-build-run":
			var a bop.BuildRunArgs
			strictDecode(t, d, &a)
			if a.Env["HOST"] != engine.HostPlatform().Triple() {
				t.Errorf("%s: HOST %q", d.Name, a.Env["HOST"])
			}
			if d.Name == "core-build-run" && (a.Env["TARGET"] != tc.Triple || a.Env["TARGET_CC"] != tc.Prefix+"cc" || a.Env["PKG_CONFIG_ALLOW_CROSS"] != "1") {
				t.Errorf("core's build run: %v", a.Env)
			}
		case "test-run":
			var a bspec.TestRunArgs
			strictDecode(t, d, &a)
			if len(a.Argv) < 2 || a.Argv[0] != qemuOut+"/bin/"+tc.EmulatorCmd {
				t.Errorf("test run argv %v, want the emulator first", a.Argv)
			}
		}
	}
}

// A crate evaluated without the cross toolchain (or with another rustc than the target std's)
// cannot be planned for another platform.
func TestPlanCrossMismatch(t *testing.T) {
	g, req, _ := crossFixture(t)
	req.Inputs["nixpkgs:std"] = engine.Resolved{Outputs: map[string]string{"out": "/nix/store/63jyj7s1b10awhkkilbfxsaq3g9i1gdi-rustc-1.97.1"}}
	req.Nodes = nodesOf(g, "core@"+platform())
	if _, err := (Ecosystem{}).Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "default rustc") {
		t.Errorf("std of another rustc: err = %v", err)
	}
}
