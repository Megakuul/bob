package rust

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// registryEnv points bob's resolver at the offline registry mirror (tools/regmirror, HARNESS
// §4.3) when the harness started one: BOB_TEST_CRATES_INDEX is the base URL of its sparse
// crates.io index (crate files are served next to it). The run then needs no network.
func registryEnv(home string, extra ...string) []string {
	env := []string{"BOB_HOME=" + home}
	if p := os.Getenv("BOB_TEST_CRATES_INDEX"); p != "" {
		env = append(env, "BOB_CRATES_INDEX="+p)
	}
	return append(env, extra...)
}

// lockedFixture copies repository fixture name and runs `bob lock` in it.
func lockedFixture(t *testing.T, name, home string) (string, bool) {
	t.Helper()
	dir := harness.Fixture(t, name)
	if r := bobEnv(t, dir, registryEnv(home), "lock"); !ok(t, r) {
		return dir, false
	}
	return dir, true
}

// devEnv returns the process environment `bob env --format json` prescribes for dir: the
// outside variables and PATH (the shims first).
func devEnv(t *testing.T, dir, home string) ([]string, bool) {
	t.Helper()
	r := bobEnv(t, dir, registryEnv(home), "env", "--format", "json")
	if !ok(t, r) {
		return nil, false
	}
	var data struct {
		Outside struct {
			Set     map[string]string   `json:"set"`
			Prepend map[string][]string `json:"prepend"`
		} `json:"outside"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &data); err != nil {
		t.Errorf("bob env --format json: not a JSON object: %v\n%s", err, r.Stdout)
		return nil, false
	}
	var env []string
	for k, v := range data.Outside.Set {
		env = append(env, k+"="+v)
	}
	path := "/usr/bin:/bin"
	if p := data.Outside.Prepend["PATH"]; len(p) > 0 {
		path = strings.Join(p, ":") + ":" + path
	}
	sort.Strings(env)
	return append(env, "PATH="+path), true
}
