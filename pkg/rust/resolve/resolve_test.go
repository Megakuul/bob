package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotRegistry(t *testing.T) MemRegistry {
	t.Helper()
	reg := MemRegistry{}
	for rel, body := range readSnapshot(t, filepath.Join("testdata", "index.tar.gz")) {
		es, err := ParseIndexFile(filepath.Base(rel), body)
		if err != nil {
			t.Fatal(err)
		}
		reg[filepath.Base(rel)] = es
	}
	return reg
}

func snapshotMember(t *testing.T, name string) Member {
	t.Helper()
	for _, w := range cmpCorpus {
		if w.name == name {
			m, err := w.member()
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("no workspace %s", name)
	return Member{}
}

// TestRecords checks bob.lock's crate records of the tokio-full snapshot graph and that
// Cargo.lock rendered from the records alone equals the one from the resolution.
func TestRecords(t *testing.T) {
	reg := snapshotRegistry(t)
	m := snapshotMember(t, "s3-tokio-full")
	res, err := Resolve(context.Background(), reg, []Member{m}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := res.Records()
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Crate{}
	for i, c := range recs {
		byKey[c.Name+"@"+c.Version] = c
		if i > 0 && !(recs[i-1].Name < c.Name || recs[i-1].Name == c.Name) {
			t.Errorf("records not sorted at %s", c.Name)
		}
		if !strings.HasPrefix(c.Checksum, "sha256-") || len(c.Checksum) != 51 {
			t.Errorf("%s: checksum %q is not SRI sha256", c.Name, c.Checksum)
		}
		for _, d := range c.Deps {
			if d.Kind == "dev" {
				t.Errorf("%s: dev-dependency %s recorded", c.Name, d.Name)
			}
		}
	}
	for _, c := range recs {
		for _, d := range c.Deps {
			if _, ok := byKey[d.Crate]; !ok {
				t.Errorf("%s: edge to %s, which is not locked", c.Name, d.Crate)
			}
		}
	}
	// tokio's optional deps that tokio "full" never enables are left out (e.g. tracing)
	var tokio Crate
	for k, c := range byKey {
		if strings.HasPrefix(k, "tokio@") {
			tokio = c
		}
	}
	names := map[string]bool{}
	for _, d := range tokio.Deps {
		names[d.Name] = true
	}
	if !names["bytes"] || !names["tokio-macros"] || names["tracing"] {
		t.Errorf("tokio edges = %v", names)
	}
	if len(tokio.Features["full"]) == 0 {
		t.Errorf("tokio feature table missing: %v", tokio.Features)
	}
	want, err := res.CargoLock()
	if err != nil {
		t.Fatal(err)
	}
	got, err := CargoLockFromRecords([]Member{m}, recs)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Cargo.lock from records differs:\n%s", diffLines(got, want))
	}
	// determinism: a second resolution gives byte-identical records
	res2, err := Resolve(context.Background(), reg, []Member{m}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	recs2, err := res2.Records()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(recs)
	b, _ := json.Marshal(recs2)
	if string(a) != string(b) {
		t.Error("two resolutions gave different records")
	}
}

func TestResolveErrors(t *testing.T) {
	sc := func(index []pub, deps ...IndexDep) (MemRegistry, Member) {
		s := scenario{name: "x", index: index, deps: deps}
		reg, _ := s.registry(t)
		return reg, s.member(t)
	}
	ctx := context.Background()
	tests := []struct {
		name  string
		index []pub
		deps  []IndexDep
		opts  Options
		want  string
	}{
		{"unknown crate", []pub{{name: "a", vers: "1.0.0"}}, []IndexDep{dep("zzz", "1")}, Options{}, "zzz"},
		{"no matching version", []pub{{name: "a", vers: "1.0.0"}}, []IndexDep{dep("a", "2")}, Options{}, "no published version matches"},
		{"exact conflict", []pub{
			{name: "a", vers: "1.0.0"}, {name: "a", vers: "1.1.0"},
			{name: "b", vers: "1.0.0", deps: []IndexDep{dep("a", "=1.0.0")}},
		}, []IndexDep{dep("a", "=1.1.0"), dep("b", "1")}, Options{}, "depends on a"},
		{"links conflict", []pub{
			{name: "x", vers: "0.1.0", links: "z"}, {name: "y", vers: "0.1.0", links: "z"},
		}, []IndexDep{dep("x", "0.1"), dep("y", "0.1")}, Options{}, "cannot resolve"},
		{"missing feature", []pub{{name: "a", vers: "1.0.0"}}, []IndexDep{dep("a", "1", feats("nope"))}, Options{}, "no feature"},
		{"step limit", []pub{{name: "a", vers: "1.0.0"}, {name: "b", vers: "1.0.0"}}, []IndexDep{dep("a", "1"), dep("b", "1")}, Options{MaxSteps: 2}, "gave up"},
	}
	for _, tt := range tests {
		reg, m := sc(tt.index, tt.deps...)
		_, err := Resolve(ctx, reg, []Member{m}, tt.opts)
		if !errors.Is(err, ErrUnresolvable) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want ErrUnresolvable containing %q", tt.name, err, tt.want)
		}
	}
	reg, m := sc([]pub{{name: "a", vers: "1.0.0"}})
	if _, err := Resolve(ctx, reg, []Member{m, m}, Options{}); err == nil {
		t.Error("duplicate workspace crate accepted")
	}
	if _, err := Resolve(ctx, reg, []Member{m}, Options{RustVersion: "one"}); err == nil {
		t.Error("bad toolchain version accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	reg, m = sc([]pub{{name: "a", vers: "1.0.0"}}, dep("a", "1"))
	if _, err := Resolve(cctx, reg, []Member{m}, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

// TestWorkspacePathDeps resolves two members where one depends on the other by path.
func TestWorkspacePathDeps(t *testing.T) {
	s := scenario{name: "ws", index: []pub{{name: "a", vers: "1.0.0", features: map[string][]string{"x": {}}}}}
	reg, _ := s.registry(t)
	req, _ := ParseReq("1")
	lib := Member{Name: "lib", Version: "0.0.0", Deps: []Dep{{Name: "a", Package: "a", Req: req, Default: true, Optional: true}},
		Features: map[string][]string{"ax": {"a/x"}}}
	app := Member{Name: "app", Version: "0.0.0", Deps: []Dep{{Name: "lib", Package: "lib", Local: true, Default: true, Features: []string{"ax"}}}}
	res, err := Resolve(context.Background(), reg, []Member{app, lib}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := res.CargoLock()
	if err != nil {
		t.Fatal(err)
	}
	want := "# This file is automatically @generated by Cargo.\n# It is not intended for manual editing.\nversion = 4\n\n" +
		"[[package]]\nname = \"a\"\nversion = \"1.0.0\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\nchecksum = \"" + s.cksum("a", "1.0.0") + "\"\n\n" +
		"[[package]]\nname = \"app\"\nversion = \"0.0.0\"\ndependencies = [\n \"lib\",\n]\n\n" +
		"[[package]]\nname = \"lib\"\nversion = \"0.0.0\"\ndependencies = [\n \"a\",\n]\n"
	if got != want {
		t.Errorf("Cargo.lock:\n%s\nwant:\n%s", got, want)
	}
	app.Deps[0].Features = []string{"missing"}
	if _, err := Resolve(context.Background(), reg, []Member{app, lib}, Options{}); !errors.Is(err, ErrUnresolvable) {
		t.Errorf("missing feature of a workspace crate: %v", err)
	}
}
