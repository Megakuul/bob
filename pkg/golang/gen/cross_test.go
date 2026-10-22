package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

func otherPlatformName() string {
	if hostPlatformName() == "linux-arm64" {
		return "linux-amd64"
	}
	return "linux-arm64"
}

// planWith plans testGraph with every binary and test node in the projection proj; a test node
// gets an emulator dep when emulated.
func planWith(t *testing.T, proj map[string]any, emulated bool) map[string]*drv.Derivation {
	t.Helper()
	root := testRepo(t)
	g := testGraph(t)
	p := map[string]json.RawMessage{}
	for k, v := range proj {
		p[k] = rawJSON(t, v)
	}
	for _, n := range g.Nodes {
		if n.Kind == gospec.KindModule {
			continue
		}
		n.Proj = p
		if n.Kind == gospec.KindTest && emulated {
			var s gospec.Test
			if err := json.Unmarshal(n.Spec, &s); err != nil {
				t.Fatal(err)
			}
			s.Emulator = len(n.Deps)
			n.Deps = append(n.Deps, "nixpkgs:qemu-user")
			n.Spec = rawJSON(t, s)
		}
	}
	req := testRequest(t, root, g, &fakeStore{})
	req.Inputs["nixpkgs:qemu-user"] = engine.Resolved{Drv: "/nix/store/6wpyrjh2q489wizpbsw8sylbgmqsrhv2-qemu-user-10.2.4.drv",
		Outputs: map[string]string{"out": "/nix/store/6wpyrjh2q489wizpbsw8sylbgmqsrhv2-qemu-user-10.2.4"}}
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*drv.Derivation{}
	for _, d := range res.Drvs {
		out[d.Name] = d
	}
	return out
}

func text(d *drv.Derivation) string { return fmt.Sprint(d.Env, d.Args, d.Structured) }

// TestCrossGoPlan: every Go derivation of a cross build has the host system, the target's
// GOARCH, the cross environment (GOOS, GOARCH, CC, CXX) and -extld (R-FW-033, R-PLAT-004); names
// carry the platform; the test runs under qemu (R-FW-039).
func TestCrossGoPlan(t *testing.T) {
	other := otherPlatformName()
	arch := strings.TrimPrefix(other, "linux-")
	triple := map[string]string{"amd64": "x86_64-unknown-linux-gnu", "arm64": "aarch64-unknown-linux-gnu"}[arch]
	drvs := planWith(t, map[string]any{"platform": other, "profile": "release", "sanitizers": []string{}, "race": false}, true)
	n := 0
	for name, d := range drvs {
		if d.System != engine.HostSystem() {
			t.Errorf("%s: system %s, want the host's", name, d.System)
		}
		s := text(d)
		if strings.HasPrefix(name, "go-tools") || strings.Contains(name, "-run-") {
			continue // cmd/pack runs on the host; the test run has no toolchain
		}
		n++
		for _, want := range []string{"GOARCH:" + arch, "GOOS:linux", "CC:" + triple + "-gcc", "CXX:" + triple + "-g++", "-extld=" + triple + "-gcc"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
		if !strings.Contains(name, other) {
			t.Errorf("%s: the name does not carry %s", name, other)
		}
	}
	if n == 0 {
		t.Fatal("no cross derivation")
	}
	run := drvs[drv.Name("go-test", "example.com/m/lib/a", "run", other)]
	if run == nil || !strings.Contains(text(run), "/bin/qemu-"+map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]) {
		t.Errorf("the cross test run does not use qemu: %v", run)
	}
}

// TestGoProjection: the Go rows of R-FW-038: debug, asan and race change std and pure packages
// (-N -l, -asan, -race), ubsan does not; names carry the tags.
func TestGoProjection(t *testing.T) {
	host := hostPlatformName()
	base := func(prof string, san []string, race bool) map[string]*drv.Derivation {
		return planWith(t, map[string]any{"platform": host, "profile": prof, "sanitizers": san, "race": race}, false)
	}
	paths := func(m map[string]*drv.Derivation, prefix string) map[string]string {
		out := map[string]string{}
		for name, d := range m {
			if strings.HasPrefix(name, prefix) {
				p, _ := d.Path()
				out[name] = p
			}
		}
		return out
	}
	rel := paths(base("release", []string{}, false), "go-example.com-m-lib-a")
	if len(rel) != 1 {
		t.Fatalf("release groups of lib/a: %v", rel)
	}
	for _, c := range []struct {
		name   string
		drvs   map[string]*drv.Derivation
		tag    string
		same   bool
		gcflag string
	}{
		{"debug", base("debug", []string{}, false), "-dbg", false, "-N"},
		{"asan", base("release", []string{"asan"}, false), "-address", false, "-asan"},
		{"race", base("release", []string{}, true), "-race", false, "-race"},
		{"ubsan", base("release", []string{"ubsan"}, false), "", true, ""},
	} {
		got := paths(c.drvs, "go-example.com-m-lib-a")
		if len(got) != 1 {
			t.Errorf("%s: groups of lib/a: %v", c.name, got)
			continue
		}
		for name, p := range got {
			same := false
			for _, rp := range rel {
				same = same || rp == p
			}
			if same != c.same || !strings.HasSuffix(name, c.tag) {
				t.Errorf("%s: %s (%s) same=%v, want same=%v and suffix %q", c.name, name, p, same, c.same, c.tag)
			}
			if c.gcflag != "" && !strings.Contains(text(c.drvs[name]), c.gcflag) {
				t.Errorf("%s: %s lacks %s", c.name, name, c.gcflag)
			}
		}
	}
}
