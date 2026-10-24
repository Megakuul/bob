package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/megakuul/bob/pkg/engine/drv"
)

type zeroModulo struct{}

func (zeroModulo) Modulo(string) ([32]byte, error) { return [32]byte{1}, nil }

// jsonFixup is bspec.Fixup's JSON shape (pkg/engine cannot import the builder layer); the
// generators wrote their fixup contract either as this struct or as the equivalent map.
type jsonFixup struct {
	Strip       bool     `json:"strip"`
	DebugOut    bool     `json:"debugOut"`
	ShrinkRPath bool     `json:"shrinkRpath"`
	Forbidden   []string `json:"forbidden"`
	Tools       struct {
		Strip    string `json:"strip"`
		Objcopy  string `json:"objcopy"`
		Patchelf string `json:"patchelf"`
	} `json:"tools"`
}

// BuilderJob.Finalize gives the derivations the generators built by hand before (F2 of the
// M5-M18 steward review): same spec, inputs, disallowed references and attributes.
func TestBuilderJobMatchesHandWrittenJob(t *testing.T) {
	const builder = "/nix/store/00000000000000000000000000000000-bob-builder"
	const dep = "/nix/store/11111111111111111111111111111111-dep.drv"
	const src = "/nix/store/22222222222222222222222222222222-src"
	args := map[string]any{"x": 1}
	var full jsonFixup
	full.Strip, full.ShrinkRPath, full.Forbidden = true, true, []string{builder, src} // sorted, as the generators wrote it
	full.Tools.Patchelf = "/nix/store/33333333333333333333333333333333-patchelf/bin/patchelf"
	empty := map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
	for name, c := range map[string]struct {
		hand drv.Job
		job  BuilderJob
	}{
		"no fixup": {
			drv.Job{Spec: map[string]any{"v": 1, "kind": "k", "label": "//a:b", "outputs": []any{"out"}, "args": args, "fixup": empty}},
			BuilderJob{},
		},
		"fixup with forbidden paths": {
			drv.Job{Spec: map[string]any{"v": 1, "kind": "k", "label": "//a:b", "outputs": []any{"out"}, "args": args, "fixup": full},
				Disallowed: map[string][]string{"out": {builder, src}}},
			BuilderJob{Fixup: &Fixup{Strip: true, ShrinkRPath: true, Forbidden: []string{src, builder, src},
				Tools: FixupTools{Patchelf: full.Tools.Patchelf}}},
		},
		"discard and attributes": {
			drv.Job{Spec: map[string]any{"v": 1, "kind": "k", "label": "//a:b", "outputs": []any{"out"}, "args": args, "fixup": empty},
				DiscardRefs: map[string]bool{"out": true}},
			BuilderJob{Discard: true, Attrs: map[string]any{"exportReferencesGraph": map[string]any{"closure": []string{src}}}},
		},
	} {
		h := c.hand
		h.Name, h.System, h.Builder = "x", "aarch64-linux", builder
		h.InputDrvs = []drv.InputDrv{{Path: dep, Outputs: []string{"bin", "out"}}}
		h.InputSrcs = []string{builder, src}
		h.ExtraAttribute = map[string]any{"allowSubstitutes": false}
		for k, v := range c.job.Attrs {
			h.ExtraAttribute[k] = v
		}
		hd, err := h.Derivation()
		if err != nil {
			t.Fatal(err)
		}
		if err := hd.Finalize(zeroModulo{}); err != nil {
			t.Fatal(err)
		}
		want, err := hd.Path()
		if err != nil {
			t.Fatal(err)
		}

		in := NewInputs(builder)
		in.Add(Resolved{Drv: dep, Outputs: map[string]string{"out": "/nix/store/o", "bin": "/nix/store/b"}}, "out")
		in.AddDrv(dep, "bin")
		in.AddSrc(src)
		j := c.job
		j.Name, j.System, j.Builder, j.Kind, j.Label, j.Args, j.Inputs = "x", "aarch64-linux", builder, "k", "//a:b", args, in
		got, d, err := j.Finalize(zeroModulo{})
		if err != nil {
			t.Fatal(err)
		}
		if got != want || OutPath(d) != OutPath(hd) {
			t.Errorf("%s: path %s, want %s", name, got, want)
		}
	}
}

func TestInputs(t *testing.T) {
	in := NewInputs("/b")
	if p := in.Add(Resolved{Drv: "/d.drv", Outputs: map[string]string{"out": "/o"}}, "lib"); p != "" {
		t.Errorf("missing output = %q, want \"\"", p)
	}
	if p := in.Add(Resolved{Outputs: map[string]string{}}, "out"); p != "" {
		t.Errorf("missing source output = %q, want \"\"", p)
	}
	if p := in.AddCutoff(Resolved{Drv: "/g.drv", Outputs: map[string]string{"out": "/g"}, Cutoff: map[string]string{"out": "/c"}}, "out"); p != "/c" {
		t.Errorf("cutoff = %q, want /c", p)
	}
	other := NewInputs("/b")
	other.AddDrv("/a.drv", "out", "dev")
	other.AddSrc("/s")
	in.Merge(other)
	in.AddDrv("/z.drv")
	ds, ss := in.List()
	want := []drv.InputDrv{{Path: "/a.drv", Outputs: []string{"dev", "out"}}, {Path: "/z.drv"}}
	if !slices.EqualFunc(ds, want, func(a, b drv.InputDrv) bool { return a.Path == b.Path && slices.Equal(a.Outputs, b.Outputs) }) {
		t.Errorf("drvs = %v, want %v", ds, want)
	}
	if !slices.Equal(ss, []string{"/b", "/c", "/s"}) {
		t.Errorf("srcs = %v", ss)
	}
	if o := PickOutput(map[string]string{"lib": "", "dev": ""}, "bin", "lib"); o != "lib" {
		t.Errorf("PickOutput prefer = %q", o)
	}
	if o := PickOutput(map[string]string{"lib": "", "dev": ""}, "bin"); o != "dev" {
		t.Errorf("PickOutput first = %q", o)
	}
	e1, e2 := errors.New("a"), errors.New("b")
	if JoinErrors(nil) != nil || JoinErrors([]error{e1}) != e1 {
		t.Error("JoinErrors of none or one")
	}
	if j := JoinErrors([]error{e1, e2}); j.Error() != "a\nb" || !errors.Is(j, e2) {
		t.Errorf("JoinErrors = %v", j)
	}
}
