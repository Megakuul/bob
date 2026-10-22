package ocaml

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
)

// lockOpam is the "opam" section of bob.lock (SPEC §03.5.2) as far as the tests read it.
type lockOpam struct {
	Packages map[string]struct {
		Version string   `json:"version"`
		Deps    []string `json:"deps"`
	} `json:"packages"`
}

func readOpam(t *testing.T, dir string) lockOpam {
	t.Helper()
	var doc struct {
		Opam lockOpam `json:"opam"`
	}
	if err := json.Unmarshal([]byte(ecotest.ReadFile(t, filepath.Join(dir, "bob.lock"))), &doc); err != nil {
		t.Fatalf("bob.lock is not JSON: %v", err)
	}
	return doc.Opam
}

// lockedFixture copies the ocaml fixture and runs `bob lock` against the mirror.
func lockedFixture(t *testing.T, home string) (dir string, env []string, ok bool) {
	t.Helper()
	dir = harness.Fixture(t, "ocaml")
	env = ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return dir, env, false
	}
	return dir, env, true
}

// storeDrvs lists the .drv names in the store of home.
func storeDrvs(t *testing.T, home string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(home, "nix", "store"))
	if err != nil {
		t.Fatalf("bob home has no store: %v", err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".drv") {
			out = append(out, e.Name())
		}
	}
	return out
}
