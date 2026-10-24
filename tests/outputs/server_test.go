package outputs

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv/clients"
)

// serverURLs starts the all-in-one dev stack (HARNESS.md §13.2) and returns the server's API
// URL and an admin token. While the server is a stub, the info line has neither and the calling
// test is red for "not implemented".
func serverURLs(t *testing.T) (url, token string, ok bool) {
	t.Helper()
	root := harness.Root(t)
	bin := filepath.Join(t.TempDir(), "devstack")
	build := exec.Command("go", "build", "-o", bin, "./tools/devstack")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("harness error: go build devstack: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	cmd := exec.CommandContext(ctx, bin, "up", "-dir", t.TempDir(), "-json")
	cmd.Dir = root
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: devstack: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cancel()
			<-done
		}
		cancel()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("harness error: devstack printed no info line: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("harness error: devstack info line: %v: %q", err, line)
	}
	url, _ = m["api_url"].(string)
	token, _ = m["admin_token"].(string)
	if url == "" || token == "" {
		harness.RedFeature(t, "bob-server orchestrator (the devstack info line has no api_url/admin_token)")
		return "", "", false
	}
	return url, token, true
}

// netrcFile writes a netrc that gives the nix client the token for the server (the cache
// accepts the token as the password, SPEC §6.8) and returns its path.
func netrcFile(t *testing.T, c *clients.Client, url, token string) string {
	t.Helper()
	host := url
	for _, p := range []string{"https://", "http://"} {
		if len(host) > len(p) && host[:len(p)] == p {
			host = host[len(p):]
		}
	}
	for i := range host {
		if host[i] == ':' || host[i] == '/' {
			host = host[:i]
			break
		}
	}
	p := filepath.Join(c.Dir("netrc"), "netrc")
	if err := os.WriteFile(p, []byte("machine "+host+" login bob password "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Own(p)
	return p
}
