package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/rust/bop"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
)

// fakeResolver gives every foreign derivation a fixed modulo hash.
type fakeResolver struct{}

func (fakeResolver) Modulo(p string) ([32]byte, error) { return sha256.Sum256([]byte(p)), nil }

const (
	rustcOut  = "/nix/store/x1qs8rk2sry6ph7jfgwwiaw1lcrq3346-rustc-wrapper-1.95.0"
	rustcOld  = "/nix/store/a1qs8rk2sry6ph7jfgwwiaw1lcrq3346-rustc-wrapper-1.94.1"
	gccOut    = "/nix/store/23jyj7s1b10awhkkilbfxsaq3g9i1gdi-gcc-wrapper-15.3.0"
	binOut    = "/nix/store/33jyj7s1b10awhkkilbfxsaq3g9i1gdi-binutils-wrapper-2.45"
	builderP  = "/nix/store/sm9f61327ycyq4jhdfglc6yk7hd74xjf-bob-builder-2"
	coreSrc   = "/nix/store/4b8d1wz8yfh1b4hvyfj6x6lbm6pw2l3f-core-src"
	appSrc    = "/nix/store/5b8d1wz8yfh1b4hvyfj6x6lbm6pw2l3f-app-src"
	testSrc   = "/nix/store/6b8d1wz8yfh1b4hvyfj6x6lbm6pw2l3f-core_test-src"
	rustcNode = "nixpkgs:rustc"
)

func sri(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(h[:])
}

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func platform() string {
	if runtime.GOARCH == "amd64" {
		return `"linux-amd64"`
	}
	return `"linux-arm64"`
}

// fixture is a graph with a library core (build script, itoa, feature fast), a proc-macro
// mac (proc-macro2), a binary app (core, mac) and the tests of core.
func fixture(t *testing.T, coreToolchain string) (*ir.Graph, engine.PlanRequest) {
	t.Helper()
	proj := map[string]json.RawMessage{"platform": json.RawMessage(platform())}
	tool := func(id string) *ir.Node {
		return &ir.Node{ID: id, Kind: "nixpkgs.tool", Label: id, Proj: proj, Nix: &ir.Nix{Attr: strings.TrimPrefix(id, "nixpkgs:")}}
	}
	crate := func(id, label string, s rsspec.Crate, deps ...string) *ir.Node {
		return &ir.Node{ID: id, Kind: rsspec.KindCrate, Label: label, Proj: proj,
			Deps: append([]string{rustcNode, "nixpkgs:gcc", "nixpkgs:binutils"}, deps...), Spec: rawJSON(t, s)}
	}
	base := func(name, typ string) rsspec.Crate {
		root := "src/lib.rs"
		if typ == rsspec.TypeBin {
			root = "src/main.rs"
		}
		return rsspec.Crate{Name: name, Type: typ, Edition: "2021", Root: root, Toolchain: 0, CC: 1, Binutils: 2, PkgConfig: -1,
			Deps: []rsspec.Dep{}, BuildDeps: []rsspec.Dep{}, DevDeps: []rsspec.Dep{}, Features: map[string][]string{},
			Default: []string{}, Enable: []string{}, Native: []int{}, Uses: []rsspec.Use{}}
	}
	core := base("core", rsspec.TypeLib)
	core.BuildScript = "build.rs"
	core.Features = map[string][]string{"fast": {}}
	core.Default = []string{"fast"}
	core.Deps = []rsspec.Dep{{Name: "itoa", Crate: "itoa", Req: "1", Local: -1, Features: []string{}}}
	mac := base("mac", rsspec.TypeProcMacro)
	mac.Deps = []rsspec.Dep{{Name: "proc-macro2", Crate: "proc-macro2", Req: "1", Local: -1, Features: []string{}}}
	app := base("app", rsspec.TypeBin)
	app.Deps = []rsspec.Dep{{Name: "core", Local: 3, Features: []string{}}, {Name: "mac", Local: 4, Features: []string{}}}
	coreNode := crate("core@"+platform(), "//core:core", core)
	if coreToolchain != "" {
		coreNode.Deps[0] = coreToolchain
	}
	g := &ir.Graph{Nodes: []*ir.Node{
		tool(rustcNode), tool("nixpkgs:rust_1_94"), tool("nixpkgs:gcc"), tool("nixpkgs:binutils"),
		coreNode,
		crate("mac@", "//mac:mac", mac),
		crate("app@", "//app:app", app, coreNode.ID, "mac@"),
		{ID: "core_test@", Kind: rsspec.KindTest, Label: "//core:core_test", Proj: proj, Deps: []string{coreNode.ID},
			Spec: rawJSON(t, rsspec.Test{Crate: 0, Args: []string{}, Services: nil})},
	}}
	sec := lock.RustSection{Inputs: strings.Repeat("0", 64), Index: "sparse+https://index.crates.io/", Crates: []lock.Crate{
		{Name: "itoa", Version: "1.0.15", Checksum: sri("itoa"), NarHash: sri("itoa-nar"), Meta: lock.CrateMeta{Edition: "2018"}},
		{Name: "proc-macro2", Version: "1.0.101", Checksum: sri("pm2"), NarHash: sri("pm2-nar"),
			Deps:     []lock.CrateDep{{Name: "unicode-ident", Crate: "unicode-ident@1.0.19"}},
			Features: map[string][]string{"default": {"proc-macro"}, "proc-macro": {}},
			Meta:     lock.CrateMeta{Edition: "2021", Build: "build.rs"}},
		{Name: "unicode-ident", Version: "1.0.19", Checksum: sri("ui"), NarHash: sri("ui-nar"), Meta: lock.CrateMeta{Edition: "2018"}},
	}}
	tools := map[string]engine.Resolved{
		rustcNode:           {Outputs: map[string]string{"out": rustcOut}},
		"nixpkgs:rust_1_94": {Outputs: map[string]string{"out": rustcOld}},
		"nixpkgs:gcc":       {Outputs: map[string]string{"out": gccOut}},
		"nixpkgs:binutils":  {Outputs: map[string]string{"out": binOut}},
		coreNode.ID:         {},
		"mac@":              {},
	}
	req := engine.PlanRequest{Graph: g, Lock: rawJSON(t, sec), Inputs: tools, Builder: builderP, Resolver: fakeResolver{},
		Sources: map[string]string{coreNode.ID: coreSrc, "mac@": coreSrc, "app@": appSrc, "core_test@": testSrc}}
	return g, req
}

func nodesOf(g *ir.Graph, ids ...string) []*ir.Node {
	var out []*ir.Node
	for _, n := range g.Nodes {
		for _, id := range ids {
			if n.ID == id {
				out = append(out, n)
			}
		}
	}
	return out
}

// strictDecode decodes the job of d into v, refusing unknown fields (as bob-builder does).
func strictDecode(t *testing.T, d *drv.Derivation, v any) bspec.Job {
	t.Helper()
	raw, err := json.Marshal(d.Structured["bobSpec"])
	if err != nil {
		t.Fatal(err)
	}
	var j bspec.Job
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("%s: job: %v", d.Name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(j.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %s args do not decode into bob-builder's type: %v", d.Name, j.Kind, err)
	}
	return j
}

func TestPlanCratesBuildScriptsProcMacrosAndTests(t *testing.T) {
	g, req := fixture(t, "")
	req.Nodes = nodesOf(g, "core@"+platform(), "mac@", "app@", "core_test@")
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"core@" + platform(), "mac@", "app@", "core_test@"} {
		if res.Nodes[id].Drv == "" {
			t.Errorf("node %s has no derivation", id)
		}
	}
	if res.Nodes["app@"].MainProgram != "bin/app" {
		t.Errorf("app main program = %q", res.Nodes["app@"].MainProgram)
	}
	kinds := map[string]int{}
	var names []string
	for _, d := range res.Drvs {
		names = append(names, d.Name)
		var j bspec.Job
		raw, _ := json.Marshal(d.Structured["bobSpec"])
		_ = json.Unmarshal(raw, &j)
		kinds[j.Kind]++
		switch j.Kind {
		case "rustc":
			var a bop.RustcArgs
			strictDecode(t, d, &a)
			if strings.Contains(strings.Join(d.InputSrcs, " "), "cargo") {
				t.Errorf("%s has cargo among its inputs", d.Name)
			}
			if a.Rustc != rustcOut+"/bin/rustc" {
				t.Errorf("%s: rustc %s", d.Name, a.Rustc)
			}
			if a.CrateName == "proc_macro2" && a.CrateType != "lib" {
				t.Errorf("proc-macro2 is a %s", a.CrateType)
			}
			if a.CrateName == "mac" && a.CrateType != "proc-macro" {
				t.Errorf("mac is a %s", a.CrateType)
			}
			if a.CrateName == "app" {
				if len(a.Externs) != 2 || !strings.HasSuffix(a.Externs[1].File, ".so") {
					t.Errorf("app externs = %+v, want core (rlib) and mac (proc-macro .so)", a.Externs)
				}
			}
			if a.CrateName == "core" && !a.Test && (strings.Join(a.Features, ",") != "default,fast" || a.BuildRun == "") {
				t.Errorf("core: features %v, build run %q", a.Features, a.BuildRun)
			}
		case "rust-build-run":
			var a bop.BuildRunArgs
			strictDecode(t, d, &a)
			if a.Env["TARGET"] == "" || a.Env["CARGO_CFG_TARGET_OS"] != "linux" || a.Env["CARGO_PKG_NAME"] == "" {
				t.Errorf("%s: build-script env lacks TARGET/CARGO_CFG_TARGET_OS/CARGO_PKG_NAME: %v", d.Name, a.Env)
			}
			if _, debug := a.Env["CARGO_CFG_DEBUG_ASSERTIONS"]; debug {
				t.Errorf("%s: release build scripts must not see debug_assertions", d.Name)
			}
		case "test-run":
			var a bspec.TestRunArgs
			strictDecode(t, d, &a)
		default:
			t.Errorf("%s: unexpected job kind %q", d.Name, j.Kind)
		}
	}
	// core: build script + run + compile (twice: as app's dependency and as root; the same
	// derivation), proc-macro2: script + run + compile (host), unicode-ident, itoa, mac, app,
	// core's test harness + run (+ its own build script for the test sources).
	if kinds["rust-build-run"] < 2 || kinds["test-run"] != 1 || kinds["rustc"] < 8 {
		t.Errorf("job kinds %v\n%v", kinds, names)
	}
	if len(res.Fetches) != 3 {
		t.Errorf("fetches = %d, want 3 registry sources", len(res.Fetches))
	}
	for _, f := range res.Fetches {
		p, err := drv.FixedOutputPath("sha256", hexOf(t, f.NarHash), true, f.Name)
		if err != nil || !strings.HasPrefix(f.Name, "crate-") {
			t.Errorf("fetch %s: %v", f.Name, err)
		}
		_ = p
	}

	// Planning is deterministic.
	res2, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Nodes["app@"].Drv != res.Nodes["app@"].Drv {
		t.Errorf("two plans of app differ")
	}
}

func hexOf(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "sha256-"))
	if err != nil {
		t.Fatal(err)
	}
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 64)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

func TestMixedToolchainsRejected(t *testing.T) {
	g, req := fixture(t, "nixpkgs:rust_1_94")
	req.Nodes = nodesOf(g, "app@")
	_, err := Ecosystem{}.Plan(context.Background(), req)
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1020 {
		t.Fatalf("err = %v, want BOB-E-1020", err)
	}
	want := "//app:app (rustc 1.95.0) depends on //core:core built with rustc 1.94.1; Rust crates linked together need the same toolchain"
	if !strings.Contains(d.Error(), want) {
		t.Errorf("error %q does not contain %q", d.Error(), want)
	}
}

func TestNativeCargoFiles(t *testing.T) {
	g, req := fixture(t, "")
	files, err := Ecosystem{}.Native(context.Background(), engine.PlanRequest{Graph: g, Lock: req.Lock})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = string(f.Content)
	}
	root := got["Cargo.toml"]
	if !strings.Contains(root, "resolver = \"3\"") || !strings.Contains(root, `"app",`) {
		t.Errorf("workspace Cargo.toml:\n%s", root)
	}
	app := got["app/Cargo.toml"]
	for _, want := range []string{"[[bin]]", `core = { path = "../core" }`, `mac = { path = "../mac" }`} {
		if !strings.Contains(app, want) {
			t.Errorf("app/Cargo.toml lacks %q:\n%s", want, app)
		}
	}
	if core := got["core/Cargo.toml"]; !strings.Contains(core, `itoa = { version = "=1.0.15" }`) || !strings.Contains(core, `build = "build.rs"`) {
		t.Errorf("core/Cargo.toml:\n%s", core)
	}
	if !strings.Contains(got["Cargo.lock"], "name = \"unicode-ident\"") {
		t.Errorf("Cargo.lock:\n%s", got["Cargo.lock"])
	}
	if !strings.Contains(got[".bob/vendor/proc-macro2-1.0.101/Cargo.toml"], "unicode-ident = { version = \"=1.0.19\" }") {
		t.Errorf("vendored proc-macro2 manifest:\n%s", got[".bob/vendor/proc-macro2-1.0.101/Cargo.toml"])
	}
}

func TestRustProject(t *testing.T) {
	g, req := fixture(t, "")
	ws, err := readWorkspace(g)
	if err != nil {
		t.Fatal(err)
	}
	v, err := lock.DecodeSection(lock.SectionRust, req.Lock)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rustProject(ws, v.(*lock.RustSection), "/repo", "/nix/store/6b8d1wz8yfh1b4hvyfj6x6lbm6pw2l3f-rust-lib-src")
	if err != nil {
		t.Fatal(err)
	}
	var pj struct {
		SysrootSrc string         `json:"sysroot_src"`
		Crates     []projectCrate `json:"crates"`
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		t.Fatal(err)
	}
	byName := map[string]projectCrate{}
	for _, c := range pj.Crates {
		byName[c.DisplayName] = c
	}
	app, ok := byName["app"]
	if !ok || app.RootModule != "/repo/app/src/main.rs" || !app.IsMember || len(app.Deps) != 2 {
		t.Errorf("app entry %+v", app)
	}
	if !byName["mac"].IsProcMacro || !strings.HasPrefix(byName["itoa"].RootModule, "/nix/store/") {
		t.Errorf("mac %+v itoa %+v", byName["mac"], byName["itoa"])
	}
	if len(pj.Crates) != 6 {
		t.Errorf("%d crates, want 6 (app, core, mac, itoa, proc-macro2, unicode-ident)", len(pj.Crates))
	}
}

func TestStale(t *testing.T) {
	g, req := fixture(t, "")
	lr := engine.LockRequest{Graph: g, Prev: req.Lock}
	what, _, err := Ecosystem{}.Stale(context.Background(), lr)
	if err != nil || what != "" {
		t.Fatalf("the fixture's lock is complete: %q %v", what, err)
	}
	// A crate that bob.lock does not hold.
	for _, n := range g.Nodes {
		if n.Label == "//core:core" {
			var s rsspec.Crate
			_ = json.Unmarshal(n.Spec, &s)
			s.Deps = append(s.Deps, rsspec.Dep{Name: "hex", Crate: "hex", Req: "0.4", Local: -1, Features: []string{}})
			n.Spec = rawJSON(t, s)
		}
	}
	what, at, err := Ecosystem{}.Stale(context.Background(), engine.LockRequest{Graph: g, Prev: req.Lock})
	if err != nil || what != "//core:core needs rust hex 0.4 (not pinned)" {
		t.Fatalf("stale = %q (%v) at %v", what, err, at)
	}
}
