package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"testing"
	"time"
)

// corpus is testdata/opam-corpus.json: opam's own answers for the 20 graphs of R-ML-002.
type corpus struct {
	Commit string `json:"commit"`
	Graphs []struct {
		Name  string            `json:"name"`
		Roots []string          `json:"roots"`
		Want  map[string]string `json:"want"`
	} `json:"graphs"`
}

// s5Graphs are the 8 graphs of spike S5 (docs/spikes/s05-ocaml.md §3.2); bob must equal opam
// on each of them.
var s5Graphs = []string{"demo", "ppx", "cohttp", "core", "irmin", "eio", "dream", "lsp"}

// snapshot returns the unpacked opam-repository at the corpus commit, named by BOB_OPAM_REPO
// (e.g. spikes/s05-ocaml/tmp/repo); the corpus tests skip without it.
func snapshot(t *testing.T) *Repository {
	t.Helper()
	dir := os.Getenv("BOB_OPAM_REPO")
	if dir == "" {
		t.Skip("BOB_OPAM_REPO (an unpacked opam-repository at the corpus commit) is not set")
	}
	return NewRepository(os.DirFS(dir))
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile("testdata/opam-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func s5Toolchain() map[string]string { return Toolchain("5.4.1", "3.21.1", "1.9.8") }

// TestCorpusEqualsOpam compares Solve with opam's recorded answers: every one of the 8 S5
// graphs must be identical, and at least 14 of all 20 (R-ML-002's 70 %).
func TestCorpusEqualsOpam(t *testing.T) {
	repo := snapshot(t)
	c := loadCorpus(t)
	equal := 0
	for _, g := range c.Graphs {
		t.Run(g.Name, func(t *testing.T) {
			var reqs []Request
			for _, r := range g.Roots {
				reqs = append(reqs, Request{Name: r})
			}
			start := time.Now()
			sol, err := Solve(context.Background(), repo, Options{Arch: "arm64", Provided: s5Toolchain()}, reqs)
			if err != nil {
				t.Fatalf("Solve: %v", err)
			}
			validate(t, repo, "arm64", s5Toolchain(), reqs, sol)
			got := map[string]string{}
			for _, p := range sol.Packages {
				got[p.Name] = p.Version
			}
			d := diff(g.Want, got)
			t.Logf("%d packages, %d steps, optimal %v, %v", len(got), sol.Steps, sol.Optimal, time.Since(start).Round(time.Millisecond))
			if len(d) == 0 {
				equal++
				return
			}
			if slices.Contains(s5Graphs, g.Name) {
				t.Errorf("differs from opam (opam vs bob): %v", d)
			} else {
				t.Logf("differs from opam (opam vs bob): %v", d)
			}
		})
	}
	t.Logf("equal to opam on %d of %d graphs", equal, len(c.Graphs))
	if equal < 14 {
		t.Errorf("equal to opam on %d of %d graphs, want >= 14", equal, len(c.Graphs))
	}
}

func diff(want, got map[string]string) []string {
	var out []string
	for k, v := range want {
		if got[k] != v {
			out = append(out, fmt.Sprintf("%s: %s vs %s", k, v, orDash(got[k])))
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			out = append(out, fmt.Sprintf("%s: - vs %s", k, v))
		}
	}
	sort.Strings(out)
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// TestSnapshotParses parses every opam file of the snapshot and evaluates its dependency
// formulas (S5: 10 of 19 415 files were beyond the spike's parser).
func TestSnapshotParses(t *testing.T) {
	repo := snapshot(t)
	ents, err := fs.ReadDir(repo.fsys, "packages")
	if err != nil {
		t.Fatal(err)
	}
	files, bad := 0, 0
	env := func(n string) (string, bool) { v, ok := globals("arm64", "5.4.1")[n]; return v, ok }
	for _, e := range ents {
		vs, err := repo.Versions(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			files++
			err := v.ParseErr
			if err == nil {
				for _, f := range []string{"depends", "depopts", "conflicts"} {
					if _, e := Deps(v.File.Fields[f], env); e != nil && err == nil {
						err = e
					}
				}
			}
			if err != nil {
				bad++
				t.Logf("%s.%s: %v", v.Name, v.Version, err)
			}
		}
	}
	t.Logf("%d opam files, %d not usable", files, bad)
	if bad > 10 {
		t.Errorf("%d of %d opam files not usable, want <= 10", bad, files)
	}
}
