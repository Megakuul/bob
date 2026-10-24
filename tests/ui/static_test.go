package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestStaticBuild is the file half of R-UI-001: web/build holds 200.html and no server output,
// and web/svelte.config.js selects adapter-static.
func TestStaticBuild(t *testing.T) {
	parity.Covers(t, "R-UI-001")
	root := harness.Root(t)
	build := filepath.Join(root, "web", "build")
	if _, err := os.Stat(filepath.Join(build, "200.html")); err != nil {
		t.Errorf("R-UI-001: web/build/200.html: %v", err)
	}
	for _, bad := range []string{"server", "index.js", "handler.js", "env.js"} {
		if _, err := os.Stat(filepath.Join(build, bad)); err == nil {
			t.Errorf("R-UI-001: web/build/%s exists: the build must be static, with no server output", bad)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(root, "web", "svelte.config.js"))
	if err != nil {
		t.Errorf("red: not implemented yet: web/svelte.config.js: %v", err)
		return
	}
	text := string(cfg)
	if !strings.Contains(text, "@sveltejs/adapter-static") {
		t.Errorf("R-UI-001: web/svelte.config.js does not use @sveltejs/adapter-static")
	}
	for _, other := range []string{"adapter-auto", "adapter-node", "adapter-vercel", "adapter-netlify", "adapter-cloudflare"} {
		if strings.Contains(text, "@sveltejs/"+other) {
			t.Errorf("R-UI-001: web/svelte.config.js references @sveltejs/%s", other)
		}
	}
	if !strings.Contains(text, "200.html") {
		t.Errorf("R-UI-001: web/svelte.config.js has no fallback: '200.html' (SPA fallback of the static adapter)")
	}
}

// TestServedHeaders is the server half of R-UI-001: against a running bob-server, an unknown
// route (GET /p/x/builds/y) returns 200.html with Cache-Control: no-cache and everything under
// /_app/immutable/ is served immutable.
func TestServedHeaders(t *testing.T) {
	parity.Covers(t, "R-UI-001")
	root := harness.Root(t)
	api := devstackAPI(t, root)
	if api == "" {
		return
	}
	want, err := os.ReadFile(filepath.Join(root, "web", "build", "200.html"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	resp := get(t, api+"/p/x/builds/y")
	if resp.status != http.StatusOK || string(resp.body) != string(want) {
		t.Errorf("R-UI-001: GET /p/x/builds/y: status %d, body is 200.html: %v", resp.status, string(resp.body) == string(want))
	}
	if cc := resp.header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("R-UI-001: GET /p/x/builds/y: Cache-Control %q, want no-cache", cc)
	}

	var asset string
	err = filepath.WalkDir(filepath.Join(root, "web", "build", "_app", "immutable"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && asset == "" {
			asset, _ = filepath.Rel(filepath.Join(root, "web", "build"), p)
		}
		return err
	})
	if err != nil || asset == "" {
		t.Fatalf("harness: web/build has no _app/immutable file: %v", err)
	}
	resp = get(t, api+"/"+filepath.ToSlash(asset))
	if cc := resp.header.Get("Cache-Control"); resp.status != http.StatusOK || !strings.Contains(cc, "immutable") {
		t.Errorf("R-UI-001: GET /%s: status %d, Cache-Control %q, want immutable", asset, resp.status, cc)
	}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func get(t *testing.T, url string) reply {
	t.Helper()
	c := &http.Client{Timeout: 10 * time.Second}
	r, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return reply{r.StatusCode, r.Header, b}
}

// devstackAPI starts the all-in-one stack (HARNESS §13.2) and returns the orchestrator's base
// URL from the `api_url` field of the info line. While the server is a stub the field is
// absent: the test is red as "not implemented" and the return value is empty.
func devstackAPI(t *testing.T, root string) string {
	t.Helper()
	if r := harness.RunPath(t, harness.Bin(t, "bob-server"), harness.Spec{Timeout: time.Minute}, "orchestrator"); r.IsNotImplemented() {
		harness.RedFeature(t, "bob-server orchestrator: "+strings.TrimSpace(strings.SplitN(r.Combined(), "\n", 2)[0]))
		return ""
	}
	bin := filepath.Join(t.TempDir(), "devstack")
	build := exec.Command("go", "build", "-o", bin, "./tools/devstack")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("harness error: go build devstack: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	api, _ := m["api_url"].(string)
	if api == "" {
		harness.RedFeature(t, "bob-server orchestrator (the devstack info line has no api_url)")
	}
	return api
}

// allowedDeps is the R-UI-005 allowlist; "test tooling" is the playwright-free set below
// (the browser tests are Go, tests/ui), so only vitest-family packages are admitted.
var allowedDeps = map[string]bool{
	"svelte": true, "@sveltejs/kit": true, "@sveltejs/adapter-static": true,
	"@sveltejs/vite-plugin-svelte": true, "vite": true, "tailwindcss": true, "@tailwindcss/vite": true,
	"@connectrpc/connect": true, "@connectrpc/connect-web": true, "@bufbuild/protobuf": true,
	"typescript": true, "svelte-check": true, "vitest": true, "@testing-library/svelte": true,
	"jsdom": true, "@vitest/coverage-v8": true,
}

// TestDependencyAllowlist is R-UI-005: every package in web/package.json (dependencies and
// devDependencies, plus optional and peer ones) is on the allowlist; a class-composition or
// component library fails the test.
func TestDependencyAllowlist(t *testing.T) {
	parity.Covers(t, "R-UI-005")
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "web", "package.json"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatalf("harness: web/package.json: %v", err)
	}
	for _, field := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		var deps map[string]string
		if raw, ok := pkg[field]; ok {
			if err := json.Unmarshal(raw, &deps); err != nil {
				t.Fatalf("harness: web/package.json %s: %v", field, err)
			}
		}
		for name := range deps {
			if !allowedDeps[name] {
				t.Errorf("R-UI-005: web/package.json %s: %q is not on the allowlist", field, name)
			}
		}
	}
	// The styling stack is required, not just permitted: Tailwind v4 through its vite plugin.
	var dev map[string]string
	_ = json.Unmarshal(pkg["devDependencies"], &dev)
	var dep map[string]string
	_ = json.Unmarshal(pkg["dependencies"], &dep)
	for _, need := range []string{"tailwindcss", "@tailwindcss/vite"} {
		if dev[need] == "" && dep[need] == "" {
			t.Errorf("red: not implemented yet: web/package.json does not list %s (R-UI-005, D21)", need)
		}
	}
}
