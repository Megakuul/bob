package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/output/tarball/bop"
	"github.com/megakuul/bob/pkg/output/tarball/internal/tarspec"
)

const (
	progPath  = "/nix/store/pppppppppppppppppppppppppppppppp-api"
	glibcPath = "/nix/store/gggggggggggggggggggggggggggggggg-glibc-2.42-47"
	sslPath   = "/nix/store/ssssssssssssssssssssssssssssssss-openssl-3.6.5"
)

type fakeStore struct{ refs map[string][]string }

func (s fakeStore) HostPath(p string) string { return filepath.Join("/nonexistent", filepath.Base(p)) }
func (s fakeStore) ImportSources(context.Context, []srcs.Source) ([]string, error) {
	return nil, nil
}
func (s fakeStore) Ensure(context.Context, fetch.Request) (string, error) { return "", nil }
func (s fakeStore) Closure(_ context.Context, outs []engine.PlanOutput) (map[string][]string, error) {
	var roots []string
	for _, o := range outs {
		roots = append(roots, o.Path)
	}
	res := map[string][]string{}
	for _, p := range engine.ClosureOf(s.refs, roots...) {
		res[p] = s.refs[p]
	}
	return res, nil
}

func plan1(t *testing.T, kind string, refs map[string][]string) (engine.PlanResult, error) {
	t.Helper()
	raw, _ := json.Marshal(tarspec.Tarball{Name: "api-tar", Kind: kind, Platform: "linux-arm64"})
	n := &ir.Node{ID: "t", Kind: tarspec.KindTarball, Label: "//deploy:api-tar", Spec: raw, Deps: []string{"api"}}
	api := &ir.Node{ID: "api", Kind: "go.binary", Label: "//cmd/api:api"}
	req := engine.PlanRequest{Graph: &ir.Graph{Nodes: []*ir.Node{n, api}}, Nodes: []*ir.Node{n},
		Inputs:  map[string]engine.Resolved{"api": {Outputs: map[string]string{"out": progPath}, MainProgram: "bin/api"}},
		Builder: "/nix/store/0000000000000000000000000000000b-bob-builder", Store: fakeStore{refs: refs}}
	return Ecosystem{}.Plan(context.Background(), req)
}

// R-OUT-004: a Static tarball of a program with run-time dependencies is a definition error
// naming them; a Closure tarball lists the closure and the roots.
func TestTarballs(t *testing.T) {
	refs := map[string][]string{progPath: {glibcPath, sslPath}, sslPath: {glibcPath}}
	_, err := plan1(t, tarspec.Static, refs)
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1022 ||
		d.Error() != "//cmd/api:api is not a static binary (it needs glibc-2.42, openssl-3.6.5)" {
		t.Fatalf("static of a dynamic program: %v", err)
	}
	res, err := plan1(t, tarspec.Closure, refs)
	if err != nil || len(res.Drvs) != 1 {
		t.Fatalf("closure: %v %v", res, err)
	}
	spec, _ := res.Drvs[0].Structured["bobSpec"].(map[string]any)
	b, _ := json.Marshal(spec["args"])
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var a bop.Args
	if err := dec.Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.File != "api-tar-linux-arm64.tar.gz" || len(a.Paths) != 3 || len(a.Roots) != 1 || a.Roots[0] != progPath {
		t.Errorf("args %+v", a)
	}
	res, err = plan1(t, tarspec.Static, map[string][]string{progPath: nil})
	if err != nil || len(res.Drvs) != 1 {
		t.Fatalf("static: %v", err)
	}
}
