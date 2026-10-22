package build

import (
	"encoding/json"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/store"
)

func otherPlatform() bob.Platform {
	if engine.HostPlatform() == bob.LinuxARM64 {
		return bob.LinuxAMD64
	}
	return bob.LinuxARM64
}

func projNode(id string, p bob.Platform, extra map[string]string) *ir.Node {
	proj := map[string]json.RawMessage{"platform": json.RawMessage(`"` + string(p) + `"`)}
	for k, v := range extra {
		proj[k] = json.RawMessage(v)
	}
	return &ir.Node{ID: id, Kind: "go.test", Label: "//a:" + id, Proj: proj}
}

func TestEntryKeyAndCrossTargets(t *testing.T) {
	if entryKey(store.HostSystem(), "openssl") != "openssl" || entryKey("x86_64-linux", "openssl") == "openssl" && store.HostSystem() != "x86_64-linux" {
		t.Errorf("entryKey: host attrs keep their plain key, other systems are prefixed")
	}
	g := &ir.Graph{Nodes: []*ir.Node{projNode("a", engine.HostPlatform(), nil), projNode("b", otherPlatform(), nil), {ID: "c", Kind: "bob.file"}}}
	if ts := crossTargets(g); len(ts) != 1 || ts[0] != otherPlatform() {
		t.Errorf("crossTargets = %v", ts)
	}
}

// TestSkipReason: a sanitised or race test for another platform is not run here (R-FW-039); a
// plain foreign test and any native test are.
func TestSkipReason(t *testing.T) {
	o := otherPlatform()
	nodes := []*ir.Node{
		projNode("plain", o, nil),
		projNode("asan", o, map[string]string{"sanitizers": `["asan"]`}),
		projNode("race", o, map[string]string{"race": `true`}),
		projNode("native", engine.HostPlatform(), map[string]string{"sanitizers": `["asan"]`}),
	}
	g := &ir.Graph{Nodes: nodes}
	p := &planner{drvNode: map[string]string{}}
	for _, n := range nodes {
		p.drvNode["/nix/store/"+n.ID+".drv"] = n.ID
	}
	want := map[string]string{"plain": "", "asan": "not run: needs a native " + string(o) + " machine",
		"race": "not run: needs a native " + string(o) + " machine", "native": ""}
	for id, w := range want {
		if got := skipReason(g, p, testrun.Unit{DrvPath: "/nix/store/" + id + ".drv"}); got != w {
			t.Errorf("%s: %q, want %q", id, got, w)
		}
	}
}

func TestUnitRunner(t *testing.T) {
	d := &drv.Derivation{Structured: map[string]any{"bobSpec": map[string]any{"args": map[string]any{
		"argv": []string{"/nix/store/6wpyrjh2q489wizpbsw8sylbgmqsrhv2-qemu-user-10.2.4/bin/qemu-x86_64", "/nix/store/x-t/bin/t"}}}}}
	if r := unitRunner(testrun.Unit{Drv: d}); r != "qemu-x86_64" {
		t.Errorf("runner %q", r)
	}
	d.Structured["bobSpec"] = map[string]any{"args": map[string]any{"argv": []string{"/nix/store/x-t/bin/t"}}}
	if r := unitRunner(testrun.Unit{Drv: d}); r != "" {
		t.Errorf("native runner %q", r)
	}
}

// TestNoteDropped: a requested Rust crate in an asan configuration gets the note of R-FW-038;
// a package whose projection holds the sanitisers does not.
func TestNoteDropped(t *testing.T) {
	h := engine.HostPlatform()
	crate := &ir.Node{ID: "//crates/rcore:rcore@" + string(h), Kind: "rust.crate", Label: "//crates/rcore:rcore",
		Proj: map[string]json.RawMessage{"platform": json.RawMessage(`"` + string(h) + `"`)}}
	lib := projNode("foo", h, map[string]string{"sanitizers": `["asan"]`})
	g := &ir.Graph{Nodes: []*ir.Node{crate, lib}, Roots: []string{crate.ID, lib.ID},
		Configs: []ir.Config{{Platform: string(h), Sanitizers: []string{"asan"}}}}
	var notes []string
	r := &runner{o: Options{Events: func(e Event) {
		if e.Note != nil {
			notes = append(notes, e.Note.Text)
		}
	}}}
	r.noteDropped(g)
	if len(notes) != 1 || notes[0] != "//crates/rcore:rcore is built without asan (not supported for Rust in v1)" {
		t.Errorf("notes = %q", notes)
	}
}
