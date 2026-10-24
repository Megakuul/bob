package gen

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
)

// lockedFixture copies the codegen fixture and runs `bob lock` against the mirror.
func lockedFixture(t *testing.T, home string) (dir string, env []string, ok bool) {
	t.Helper()
	dir = harness.Fixture(t, "codegen")
	env = ecotest.Registry(t, home)
	if r := ecotest.BobEnv(t, dir, env, "lock"); !ecotest.OK(t, r) {
		return dir, env, false
	}
	return dir, env, true
}

// serve starts the fixture's Go server (the binary at binary) on a free port and waits until it
// accepts connections; it is stopped when the test ends. It returns the base URL.
func serve(t *testing.T, binary string) (url string, port int) {
	t.Helper()
	port, err := serverenv.FreePort()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	cmd := exec.Command(binary, "-addr", fmt.Sprintf("127.0.0.1:%d", port))
	cmd.Env = []string{}
	p, err := serverenv.StartProcess(cmd, filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	url = fmt.Sprintf("http://127.0.0.1:%d", port)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		select {
		case <-p.Exited():
			t.Fatalf("the server exited early: %v\n%s", p.Err(), ecotest.ReadFile(t, p.Log))
		default:
		}
		if _, err := httpGet(url + "/rest/items/probe"); err == nil {
			return url, port
		}
	}
	t.Fatalf("the server did not answer within 30 s\n%s", ecotest.ReadFile(t, p.Log))
	return url, port
}

// lineOf returns the 1-based line of the first occurrence of needle in file.
func lineOf(t *testing.T, file, needle string) int {
	t.Helper()
	s := ecotest.ReadFile(t, file)
	i := strings.Index(s, needle)
	if i < 0 {
		t.Fatalf("harness: %q not in %s", needle, file)
	}
	return strings.Count(s[:i], "\n") + 1
}

// noStack fails t when out shows a Node stack trace or a store path.
func noStack(t *testing.T, out string) {
	t.Helper()
	if regexp.MustCompile(`(?m)^\s+at .*\(.*\)|node:internal|/nix/store/`).MatchString(out) {
		t.Errorf("the error output shows a Node stack trace or a store path:\n%s", out)
	}
}

func fixtureFile(dir, rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)) }

// httpGet fetches url and returns the body.
func httpGet(url string) (string, error) {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// httpPost posts a JSON body.
func httpPost(url, body string) (string, error) {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}
