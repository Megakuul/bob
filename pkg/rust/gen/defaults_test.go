package gen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/rust/bop"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// §03.0.6 admission rule: every native default has a justification, a CI corpus crate, a
// valid range and nixpkgs attributes.
func TestNativeDefaultsAdmission(t *testing.T) {
	for _, d := range nativeDefaults {
		if d.Why == "" || d.Test == "" || len(d.Libs) == 0 || !resolve.ValidName(d.Crate) {
			t.Errorf("entry %+v lacks Why, Test, Libs or a valid crate name", d)
		}
		if _, err := resolve.ParseReq(d.Range); err != nil {
			t.Errorf("%s: range %q: %v", d.Crate, d.Range, err)
		}
	}
	if nativeDefault("openssl-sys", "0.9.109") == nil || nativeDefault("openssl-sys", "0.8.0") != nil || nativeDefault("zstd-sys", "2.0.0") != nil {
		t.Errorf("nativeDefault does not follow the table")
	}
}

type fakeNix struct{ asked [][]string }

func (f *fakeNix) Lookup(_ context.Context, attrs []string) (map[string]nixidx.Entry, error) {
	f.asked = append(f.asked, attrs)
	out := map[string]nixidx.Entry{}
	for _, a := range attrs {
		h := strings.Repeat(string("abcdefghij"[len(a)%10]), 32)
		out[a] = nixidx.Entry{Version: "1", DrvPath: "/nix/store/" + h + "-" + a + ".drv",
			Outputs: map[string]string{"out": "/nix/store/" + h + "-" + a, "dev": "/nix/store/" + h + "-" + a + "-dev"}}
	}
	return out, nil
}

// openssl-sys without rust.Uses gets openssl and pkg-config from the table (§03.3.6); with a
// rust.Uses for it the table is not consulted.
func TestNativeDefaultsPlanned(t *testing.T) {
	for _, withUses := range []bool{false, true} {
		g, req := fixture(t, "")
		var sec lock.RustSection
		if err := json.Unmarshal(req.Lock, &sec); err != nil {
			t.Fatal(err)
		}
		sec.Crates = append(sec.Crates, lock.Crate{Name: "openssl-sys", Version: "0.9.109", Checksum: sri("ossl"), NarHash: sri("ossl-nar"),
			Links: "openssl", Meta: lock.CrateMeta{Edition: "2021", Build: "build/main.rs"}})
		req.Lock = rawJSON(t, sec)
		core := nodesOf(g, "core@"+platform())[0]
		var s rsspec.Crate
		if err := json.Unmarshal(core.Spec, &s); err != nil {
			t.Fatal(err)
		}
		s.Deps = append(s.Deps, rsspec.Dep{Name: "openssl-sys", Crate: "openssl-sys", Req: "0.9", Local: -1, Features: []string{}})
		if withUses {
			s.Uses = []rsspec.Use{{Crate: "openssl-sys", Libs: []int{}}}
		}
		core.Spec = rawJSON(t, s)
		req.Nodes = []*ir.Node{core}
		pl, err := newPlanner(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		fake := &fakeNix{}
		pl.nix = fake
		res, err := pl.plan()
		if err != nil {
			t.Fatal(err)
		}
		var run *drv.Derivation
		for _, d := range res.Drvs {
			if d.Name == "openssl-sys-0.9.109-build-run" {
				run = d
			}
		}
		if run == nil {
			t.Fatalf("no build run of openssl-sys")
		}
		var a bop.BuildRunArgs
		strictDecode(t, run, &a)
		pc := a.Env["PKG_CONFIG_PATH"]
		if withUses {
			if len(fake.asked) != 0 || strings.Contains(pc, "openssl") {
				t.Errorf("with rust.Uses the table was used: asked %v, PKG_CONFIG_PATH %q", fake.asked, pc)
			}
			continue
		}
		if len(fake.asked) != 1 || strings.Join(fake.asked[0], ",") != "pkg-config,openssl" {
			t.Errorf("lookups %v", fake.asked)
		}
		if !strings.Contains(pc, "-openssl-dev/lib/pkgconfig") || !strings.Contains(strings.Join(a.Path, ":"), "-pkg-config/bin") {
			t.Errorf("PKG_CONFIG_PATH %q, PATH %v", pc, a.Path)
		}
		inputs := map[string]bool{}
		for _, in := range run.InputDrvs {
			inputs[in.Path] = true
		}
		for _, e := range []string{"pkg-config", "openssl"} {
			found := false
			for p := range inputs {
				if strings.HasSuffix(p, "-"+e+".drv") {
					found = true
				}
			}
			if !found {
				t.Errorf("the build run does not depend on %s: %v", e, run.InputDrvs)
			}
		}
	}
}
