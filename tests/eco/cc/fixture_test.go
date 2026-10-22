package cc

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/harness"
)

// registryEnv is the environment of a bob run with its own bob home.
func registryEnv(home string, extra ...string) []string {
	return append([]string{"BOB_HOME=" + home}, extra...)
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
