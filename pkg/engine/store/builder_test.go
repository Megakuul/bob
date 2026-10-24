package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/nar"
)

// builderNAR must equal the NAR of the same tree on disk.
func TestBuilderNARMatchesTree(t *testing.T) {
	bin := []byte("\x7fELF not really a builder")
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "bob-builder"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := nar.Write(&want, tree, []string{"bin/bob-builder"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(builderNAR(bin), want.Bytes()) {
		t.Errorf("builderNAR differs from nar.Write of the tree")
	}
}

// The embedded binary has the recorded path; any other recorded path is BOB-E-9007.
func TestEmbeddedBuilderMatchesTable(t *testing.T) {
	want, err := BuilderPath(HostSystem())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embeddedBuilder(want); err != nil {
		t.Fatalf("embedded builder vs builders.go: %v (go generate ./pkg/engine/store)", err)
	}
	stale := "/nix/store/00000000000000000000000000000000-bob-builder-1"
	var de *diag.Error
	if _, err := embeddedBuilder(stale); !errors.As(err, &de) || de.Code != 9007 {
		t.Errorf("stale table entry: %v, want BOB-E-9007", err)
	}
	if _, err := BuilderPath("riscv64-linux"); err == nil {
		t.Errorf("BuilderPath of an unknown system succeeded")
	}
}

// Store.Builder imports the builder once; it is valid afterwards.
func TestStoreBuilderImports(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inv, err := s.Invalid(ctx, []string{p}); err != nil || len(inv) != 0 {
		t.Fatalf("builder %s not valid after Builder: %v %v", p, inv, err)
	}
	if p2, err := s.Builder(ctx); err != nil || p2 != p {
		t.Errorf("second Builder: %s %v", p2, err)
	}
}

// placeholder is Nix's hash placeholder of an output, which Nix replaces by the output path
// in the structured attributes before the builder starts.
func placeholder(out string) string {
	h := sha256.Sum256([]byte("nix-output:" + out))
	return "/" + base32(h[:])
}

// chain builds n derivations of kind run, each copying the previous output, with the real
// builder in the real store, and returns the time `nix-store --realise` of the last one took.
func chain(t *testing.T, s *Store, n int, salt string) time.Duration {
	t.Helper()
	ctx := context.Background()
	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := placeholder("out")
	r := modResolver{}
	var drvs []*drv.Derivation
	var prev *drv.Derivation
	for i := range n {
		steps := []map[string]any{
			{"op": "mkdir", "args": map[string]any{"path": out}},
			{"op": "write", "args": map[string]any{"path": out + "/n", "content": fmt.Sprintf("%s-%d", salt, i)}},
		}
		var inputs []drv.InputDrv
		if prev != nil {
			steps = append(steps, map[string]any{"op": "copy", "args": map[string]any{"from": prev.Outputs[0].Path + "/n", "to": out + "/prev"}})
			p, _ := prev.Path()
			inputs = []drv.InputDrv{{Path: p, Outputs: []string{"out"}}}
		}
		steps = append(steps, map[string]any{"op": "check-outputs", "args": map[string]any{}})
		spec := map[string]any{"v": 1, "kind": "run", "label": fmt.Sprintf("//chain:s%03d", i), "outputs": []any{"out"},
			"args": map[string]any{"steps": steps}, "fixup": map[string]any{}}
		d, err := drv.Job{Name: fmt.Sprintf("chain-%s-s%03d", salt, i), System: HostSystem(), Builder: builder, Spec: spec,
			InputDrvs: inputs, InputSrcs: []string{builder}}.Derivation()
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Finalize(r); err != nil {
			t.Fatal(err)
		}
		p, err := d.Path()
		if err != nil {
			t.Fatal(err)
		}
		r[p] = d
		drvs = append(drvs, d)
		prev = d
	}
	if _, err := s.ImportDrvs(ctx, drvs); err != nil {
		t.Fatal(err)
	}
	last, _ := prev.Path()
	var log bytes.Buffer
	// No substituter lookups: these outputs exist nowhere, and asking cache.nixos.org once per
	// derivation costs a network round trip each (~120 ms/drv measured), which is the build
	// pipeline's concern (plan/progress/I-M4a.md), not the builder's.
	c := s.Command(ctx, "nix-store", "--realise", last, "--option", "substituters", "")
	c.Stderr = &log
	start := time.Now()
	if err := c.Run(); err != nil {
		t.Fatalf("realise: %v\n%s", err, log.String())
	}
	took := time.Since(start)
	// Success is structural (R-NIX-018): the output is valid in the store.
	if inv, err := s.Invalid(ctx, []string{prev.Outputs[0].Path}); err != nil || len(inv) != 0 {
		t.Fatalf("output of the chain not valid: %v %v", inv, err)
	}
	b, err := os.ReadFile(filepath.Join(s.Home(), prev.Outputs[0].Path, "prev"))
	if want := fmt.Sprintf("%s-%d", salt, n-2); n > 1 && (err != nil || string(b) != want) {
		t.Errorf("last output's prev = %q %v, want %q", b, err, want)
	}
	return took
}

// The real builder runs in the real Nix sandbox: structured attributes, placeholders rewritten
// to output paths, inputs copied from the previous output.
func TestBuilderRunsInNix(t *testing.T) {
	s := openTest(t)
	chain(t, s, 3, "small")
}

// R-FW-020 at the store level: a cold chain of N=100 trivial run derivations costs one builder
// start each and stays within the 12 s budget (without bob's evaluation and planning).
func TestBuilderChain100Overhead(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 100 sequential Nix builds")
	}
	s := openTest(t)
	chain(t, s, 1, "warm") // imports the builder; not timed
	took := chain(t, s, 100, fmt.Sprint(time.Now().UnixNano()))
	t.Logf("cold chain of 100 run derivations: %s (%.0f ms/drv)", took.Round(time.Millisecond), float64(took.Milliseconds())/100)
	if took > 12*time.Second {
		t.Errorf("chain of 100 took %s, budget 12 s", took)
	}
}

// realiseOne builds one run derivation with the given steps and fixup and returns Nix's log.
func realiseOne(t *testing.T, s *Store, name string, steps []map[string]any, fixup map[string]any, disallowed []string) (string, string, error) {
	t.Helper()
	ctx := context.Background()
	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"v": 1, "kind": "run", "label": "//t:" + name, "outputs": []any{"out"},
		"args": map[string]any{"steps": steps}, "fixup": fixup}
	j := drv.Job{Name: name + "-" + fmt.Sprint(time.Now().UnixNano()), System: HostSystem(), Builder: builder, Spec: spec, InputSrcs: []string{builder}}
	if disallowed != nil {
		j.Disallowed = map[string][]string{"out": disallowed}
	}
	d, err := j.Derivation()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Finalize(modResolver{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportDrvs(ctx, []*drv.Derivation{d}); err != nil {
		t.Fatal(err)
	}
	p, _ := d.Path()
	var log bytes.Buffer
	c := s.Command(ctx, "nix-store", "--realise", p, "--option", "substituters", "")
	c.Stderr = &log
	err = c.Run()
	return d.Outputs[0].Path, log.String(), err
}

// In a real build a forbidden reference fails the build (builder exit 3, R-NIX-019) and a
// missing output fails it with the output invalid (R-NIX-017/018).
func TestBuilderFailuresInNix(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := placeholder("out")
	for _, c := range []struct {
		name, want string
		steps      []map[string]any
		fixup      map[string]any
		disallowed []string
	}{
		{"forbidden-ref", "refers to " + builder,
			[]map[string]any{{"op": "mkdir", "args": map[string]any{"path": out}}, {"op": "write", "args": map[string]any{"path": out + "/f", "content": builder + "/bin/bob-builder"}}},
			map[string]any{"forbidden": []any{builder}}, []string{builder}},
		{"missing-output", "declared output out was not produced",
			[]map[string]any{{"op": "write", "args": map[string]any{"path": "/build/elsewhere", "content": "x"}}}, map[string]any{}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			outPath, log, err := realiseOne(t, s, c.name, c.steps, c.fixup, c.disallowed)
			if err == nil {
				t.Fatalf("the build succeeded:\n%s", log)
			}
			if !strings.Contains(log, c.want) {
				t.Errorf("log lacks %q:\n%s", c.want, log)
			}
			if inv, err := s.Invalid(ctx, []string{outPath}); err != nil || len(inv) != 1 {
				t.Errorf("the failed output is valid: %v %v", inv, err)
			}
		})
	}
}

// A build cannot change its inputs (R-FLEET-008, A370): Nix binds the input paths writable and
// in bob's store the build's uid owns them, so bob-builder remounts them read-only before any
// job code runs. testdata/inputprobe (an input of the derivation) tries chmod and a file write
// on every store path it sees, also through /proc/1/root, and the store is checked afterwards.
func TestBuilderInputsReadOnlyInNix(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	builder, err := s.Builder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "inputprobe")
	gb := exec.Command("go", "build", "-trimpath", "-o", bin, "./testdata/inputprobe")
	gb.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := gb.CombinedOutput(); err != nil {
		t.Fatalf("go build inputprobe: %v\n%s", err, b)
	}
	code, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	probeNAR := builderNAR(code)
	probe, err := drv.MakeStorePath("source", sha256.Sum256(probeNAR), "bob-input-probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import(ctx, []ExportItem{{Path: probe, NAR: probeNAR}}); err != nil {
		t.Fatal(err)
	}
	inputs := []string{builder, probe}
	before := map[string]os.FileMode{}
	for _, p := range inputs {
		fi, err := os.Stat(filepath.Join(s.Home(), p))
		if err != nil {
			t.Fatal(err)
		}
		before[p] = fi.Mode()
	}
	spec := map[string]any{"v": 1, "kind": "run", "label": "//t:inputs-ro", "outputs": []any{"out"}, "fixup": map[string]any{},
		"args": map[string]any{"steps": []map[string]any{{"op": "exec", "args": map[string]any{"argv": []any{probe + "/bin/bob-builder", "/nix/store"}}}}}}
	d, err := drv.Job{Name: fmt.Sprint("inputs-ro-", time.Now().UnixNano()), System: HostSystem(), Builder: builder, Spec: spec, InputSrcs: inputs}.Derivation()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Finalize(modResolver{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportDrvs(ctx, []*drv.Derivation{d}); err != nil {
		t.Fatal(err)
	}
	p, _ := d.Path()
	var log bytes.Buffer
	c := s.Command(ctx, "nix-store", "--realise", p, "--option", "substituters", "")
	c.Stderr = &log
	if err := c.Run(); err != nil {
		t.Errorf("the probe could change an input or failed: %v\n%s", err, log.String())
	}
	for _, p := range inputs {
		host := filepath.Join(s.Home(), p)
		if fi, err := os.Stat(host); err != nil || fi.Mode() != before[p] {
			t.Errorf("input %s changed mode: %v -> %v (%v)", p, before[p], fi.Mode(), err)
		}
		if _, err := os.Lstat(filepath.Join(host, ".probe")); err == nil {
			t.Errorf("the build wrote into input %s", p)
		}
	}
}
