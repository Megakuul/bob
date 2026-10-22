package gen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/engine/drv"
)

// realisingStore is a fakeStore that "builds" while planning: the output of a split group gets
// one export file per package, with the content exports gives it (default: "export of <ip>").
type realisingStore struct {
	fakeStore
	host    string
	exports map[string]string
	fail    bool
	built   []string // derivation names, in build order
}

func (s *realisingStore) HostPath(p string) string { return filepath.Join(s.host, path.Base(p)) }

func (s *realisingStore) RealiseNow(_ context.Context, drvs map[string]*drv.Derivation) (map[string]map[string]string, error) {
	if s.fail {
		return nil, errors.New("build failed")
	}
	out := map[string]map[string]string{}
	for p, d := range drvs {
		host := s.HostPath(outPath(d))
		out[p] = map[string]string{"out": host}
		s.built = append(s.built, d.Name)
		b, err := json.Marshal(d.Structured["bobSpec"])
		if err != nil {
			return nil, err
		}
		var spec struct {
			Args struct {
				Packages []struct {
					ImportPath string `json:"importPath"`
					Export     string `json:"export"`
				} `json:"packages"`
			} `json:"args"`
		}
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, err
		}
		for _, pk := range spec.Args.Packages {
			if pk.Export == "" {
				continue
			}
			content, ok := s.exports[pk.ImportPath]
			if !ok {
				content = "export of " + pk.ImportPath
			}
			f := filepath.Join(host, "export", filepath.FromSlash(pk.ImportPath)+".a")
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// R-GO-005: in the cutoff mode the importers of a package compile against its export data by
// content, so a change that keeps the export data rebuilds the package and the link only; the
// link and the test binaries still take the object code of the group derivations.
func TestPlanCutoff(t *testing.T) {
	t.Setenv(cutoffEnv, "1")
	plan := func(root string, st *realisingStore) map[string]*drv.Derivation {
		st.host = t.TempDir()
		res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, testGraph(t), st))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		out := map[string]*drv.Derivation{}
		for _, d := range res.Drvs {
			out[d.Name] = d
		}
		return out
	}
	pathOf := func(d *drv.Derivation) string {
		p, err := d.Path()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	root := testRepo(t)
	st := &realisingStore{}
	before := plan(root, st)
	lib, main := before["go-example.com-m-lib-a"], before["go-example.com-m-cmd-x"]
	if lib == nil || main == nil || before["go-link-x"] == nil {
		t.Fatalf("derivations: %v", before)
	}
	if len(st.built) == 0 {
		t.Fatal("nothing was built while planning")
	}
	mainJob := jobJSON(t, main)
	if !strings.Contains(mainJob, "-goexport-example.com-m-lib-a/example.com/m/lib/a.a") {
		t.Errorf("the importer does not compile against the export data by content:\n%s", mainJob)
	}
	for _, in := range main.InputDrvs {
		if in.Path == pathOf(lib) {
			t.Errorf("the importer depends on the library's derivation, not on its export data")
		}
	}
	if libJob := jobJSON(t, lib); !strings.Contains(libJob, `"export":"`) {
		t.Errorf("the library is not compiled with the -linkobj split:\n%s", libJob)
	}
	if link := jobJSON(t, before["go-link-x"]); !strings.Contains(link, outPath(lib)+"/pkg/example.com/m/lib/a.a") {
		t.Errorf("the link does not take the library's object code:\n%s", link)
	}

	// A body change with the same export data: the library and the link change, the importer
	// does not.
	writeTree(t, root, map[string]string{"m/lib/a/a.go": "package a\n\nimport \"strings\"\n\nfunc Up(s string) string { return strings.ToLower(s) }\n"})
	after := plan(root, &realisingStore{})
	if pathOf(after["go-example.com-m-lib-a"]) == pathOf(lib) {
		t.Error("the changed library kept its derivation")
	}
	if pathOf(after["go-example.com-m-cmd-x"]) != pathOf(main) {
		t.Error("the importer's derivation changed although the export data did not")
	}
	if pathOf(after["go-link-x"]) == pathOf(before["go-link-x"]) {
		t.Error("the link did not change")
	}
	// Other export data: the importer changes too.
	other := plan(root, &realisingStore{exports: map[string]string{"example.com/m/lib/a": "new API"}})
	if pathOf(other["go-example.com-m-cmd-x"]) == pathOf(main) {
		t.Error("the importer kept its derivation although the export data changed")
	}

	// A failed build while planning: the plan is the one without the cutoff.
	failed := plan(root, &realisingStore{fail: true})
	t.Setenv(cutoffEnv, "")
	plain := plan(root, &realisingStore{})
	if len(failed) != len(plain) {
		t.Fatalf("fallback plan has %d derivations, the plain one %d", len(failed), len(plain))
	}
	for name, d := range plain {
		if failed[name] == nil || pathOf(failed[name]) != pathOf(d) {
			t.Errorf("fallback plan differs from the plain plan at %s", name)
		}
	}
}
