package npm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
)

// lockedFixture copies repository fixture name, points bob at the registry mirror and runs
// `bob lock` in it. env is the environment for later runs.
func lockedFixture(t *testing.T, name, home string) (dir string, env []string, ok bool) {
	t.Helper()
	dir = harness.Fixture(t, name)
	env = ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return dir, env, false
	}
	return dir, env, true
}

// lockNpm is the "npm" section of bob.lock (SPEC §03.4.2) as far as the tests read it.
type lockNpm struct {
	Importers map[string]struct {
		Deps map[string]string `json:"deps"`
		Dev  []string          `json:"dev"`
	} `json:"importers"`
	Packages map[string]lockPkg `json:"packages"`
}

type lockPkg struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`
	Deps             map[string]string `json:"deps"`
	Peers            []string          `json:"peers"`
	Optional         bool              `json:"optional"`
	Bundled          []string          `json:"bundled"`
	HasInstallScript bool              `json:"hasInstallScript"`
}

func readLock(t *testing.T, dir string) lockNpm {
	t.Helper()
	var doc struct {
		Npm lockNpm `json:"npm"`
	}
	if err := json.Unmarshal([]byte(ecotest.ReadFile(t, filepath.Join(dir, "bob.lock"))), &doc); err != nil {
		t.Fatalf("bob.lock is not JSON: %v", err)
	}
	return doc.Npm
}

// nodeTool returns the path of a binary of the pinned nodejs (build host tool, HARNESS §1.4).
func nodeTool(t *testing.T, bin string) string {
	t.Helper()
	return filepath.Join(ecotest.Tool(t, "nodejs"), "bin", bin)
}

// runNode runs the pinned node in dir with an empty environment besides PATH and HOME.
func runNode(t *testing.T, dir string, args ...string) harness.Result {
	t.Helper()
	node := nodeTool(t, "node")
	return harness.RunPath(t, node, harness.Spec{Dir: dir}, args...)
}

// storeSymlinkTarget reports whether target points into the store (virtual /nix/store or the
// store directory of home).
func inStore(target, home string) bool {
	return strings.HasPrefix(target, "/nix/store/") || strings.HasPrefix(target, filepath.Join(home, "nix", "store")+"/")
}

func lstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Errorf("%s: %v", p, err)
		return nil
	}
	return fi
}

// harnessFixture copies fixture name without running bob.
func harnessFixture(t *testing.T, name string) string {
	t.Helper()
	return harness.Fixture(t, name)
}
