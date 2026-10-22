package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
)

func other() bob.Platform {
	if HostPlatform() == bob.LinuxARM64 {
		return bob.LinuxAMD64
	}
	return bob.LinuxARM64
}

func nixNode(kind, set string, p bob.Platform) *ir.Node {
	return &ir.Node{ID: "n", Kind: kind, Label: "nixpkgs:openssl", Nix: &ir.Nix{Attr: "openssl", Set: set},
		Proj: map[string]json.RawMessage{"platform": json.RawMessage(`"` + string(p) + `"`)}}
}

// TestNixpkgsSource: tools from the host set, libraries from the target system's set or, with
// Cross(), from the host's pkgsCross set; natively always the host's attribute (R-FW-032/036).
func TestNixpkgsSource(t *testing.T) {
	host, o := HostPlatform(), other()
	for _, c := range []struct {
		n         *ir.Node
		sys, attr string
	}{
		{nixNode("nixpkgs.tool", "host", host), host.NixSystem(), "openssl"},
		{nixNode("nixpkgs.lib", "target", host), host.NixSystem(), "openssl"},
		{nixNode("nixpkgs.lib", "cross", host), host.NixSystem(), "openssl"},
		{nixNode("nixpkgs.lib", "target", o), o.NixSystem(), "openssl"},
		{nixNode("nixpkgs.pkg", "cross", o), host.NixSystem(), "pkgsCross." + o.CrossSet() + ".openssl"},
	} {
		sys, attr, err := NixpkgsSource(c.n)
		if err != nil || sys != c.sys || attr != c.attr {
			t.Errorf("%s/%s@%s: %s %s %v, want %s %s", c.n.Kind, c.n.Nix.Set, c.n.Proj["platform"], sys, attr, err, c.sys, c.attr)
		}
	}
	if _, _, err := NixpkgsSource(nixNode("nixpkgs.lib", "target", "linux-x86")); err == nil {
		t.Error("an unknown platform was accepted")
	}
}

// TestGoAndRustTargets are the Go and Rust rows of the R-FW-033 table.
func TestGoAndRustTargets(t *testing.T) {
	o := other()
	g := GoFor(HostPlatform(), o)
	tr := o.Triple()
	if e := g.Env(); !g.Cross || e["GOOS"] != "linux" || e["GOARCH"] != o.GOARCH() || e["CC"] != tr+"-gcc" || e["CXX"] != tr+"-g++" {
		t.Errorf("GoFor cross: %+v %v", g, g.Env())
	}
	if n := GoFor(HostPlatform(), HostPlatform()); n.Cross || n.Env() != nil || n.CC != "gcc" {
		t.Errorf("GoFor native: %+v", n)
	}
	r := RustFor(HostPlatform(), o)
	if !r.Cross || r.Triple != tr || r.Linker != tr+"-cc" || r.StdAttr != "rustc.unwrapped" || r.StdSystem != o.NixSystem() || r.StdName != "rust-std-"+tr {
		t.Errorf("RustFor cross: %+v", r)
	}
	if n := RustFor(HostPlatform(), HostPlatform()); n.Cross || n.StdAttr != "" || n.Linker != "cc" {
		t.Errorf("RustFor native: %+v", n)
	}
}

// TestCheckVersions is the R-PLAT-007 pre-check.
func TestCheckVersions(t *testing.T) {
	o := other()
	same := Versions{Glibc: "2.42", Rustc: "1.95.0"}
	if err := CheckVersions(o, same, same, true, true); err != nil {
		t.Errorf("equal versions: %v", err)
	}
	err := CheckVersions(o, same, Versions{Glibc: "2.43", Rustc: "1.95.0"}, true, true)
	var d *diag.Error
	if !errors.As(err, &d) || !strings.Contains(d.Error(), "target libraries for "+string(o)+" use glibc 2.42 but the cross toolchain uses glibc 2.43") ||
		!strings.Contains(d.Hint(), "cc.CrossLibs") || d.Code != 1026 {
		t.Errorf("glibc mismatch: %v", err)
	}
	if err := CheckVersions(o, same, Versions{Glibc: "2.43"}, false, true); err != nil {
		t.Errorf("glibc is not compared without target libraries: %v", err)
	}
	err = CheckVersions(o, same, Versions{Glibc: "2.42", Rustc: "1.96.0"}, true, true)
	if err == nil || !strings.Contains(err.Error(), "rustc 1.95.0") || !strings.Contains(err.Error(), "rustc 1.96.0") {
		t.Errorf("rustc mismatch: %v", err)
	}
}

func TestTestVersions(t *testing.T) {
	t.Setenv(TestIndexEnv, "")
	if _, _, ok, err := TestVersions(); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	f := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(f, []byte(`{"native":{"glibc":"2.42","rustc":"1.95.0"},"cross":{"glibc":"2.43","rustc":"1.95.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TestIndexEnv, f)
	n, c, ok, err := TestVersions()
	if !ok || err != nil || n.Glibc != "2.42" || c.Glibc != "2.43" || c.Rustc != "1.95.0" {
		t.Errorf("TestVersions = %+v %+v %v %v", n, c, ok, err)
	}
}

// TestCrossRefused: OCaml is native-only in v1 (R-PLAT-005).
func TestCrossRefused(t *testing.T) {
	n := &ir.Node{Kind: "ocaml.exe", Label: "//app:demo", Proj: map[string]json.RawMessage{"platform": json.RawMessage(`"` + string(other()) + `"`)}}
	err := CrossRefused(n)
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 11 || !strings.Contains(d.Error(), "//app:demo: OCaml packages cannot be cross-compiled yet (requested "+string(other())+" on a "+string(HostPlatform())+" host)") {
		t.Errorf("CrossRefused = %v", err)
	}
	n.Proj["platform"] = json.RawMessage(`"` + string(HostPlatform()) + `"`)
	if err := CrossRefused(n); err != nil {
		t.Errorf("native OCaml refused: %v", err)
	}
	n.Kind = "go.binary"
	n.Proj["platform"] = json.RawMessage(`"` + string(other()) + `"`)
	if err := CrossRefused(n); err != nil {
		t.Errorf("cross Go refused: %v", err)
	}
}
