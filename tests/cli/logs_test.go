package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// TestRCLI008LocalLog: after fault 6 (C++ compile error in the polyglot fixture), `bob log
// //libs/foo:foo` prints the full gcc output and its last lines match the error block's excerpt.
func TestRCLI008LocalLog(t *testing.T) {
	parity.Covers(t, "R-CLI-008")
	dir := harness.Fixture(t, "polyglot")
	env := home(t)
	cpp := filepath.Join(dir, "libs", "foo", "src", "foo.cpp")
	src := read(t, cpp)
	if !strings.Contains(src, "    return 0;\n}") {
		t.Fatal("harness: fixture foo.cpp changed; update the fault injection")
	}
	if err := os.WriteFile(cpp, []byte(strings.Replace(src, "    return 0;\n}", "    return 0\n}", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bob(t, dir, env, "build", "//libs/foo:foo")
	if harness.Red(t, b) {
		return
	}
	if b.Exit != 1 || !strings.Contains(b.Combined(), "BOB-E-2001") {
		t.Fatalf("fault 6: bob build exit %d, want 1 with BOB-E-2001\n%s", b.Exit, b.Combined())
	}
	var excerpt []string
	for _, l := range lines(b.Combined()) {
		if strings.HasPrefix(l, "   | ") {
			excerpt = append(excerpt, strings.TrimPrefix(l, "   | "))
		}
	}
	if len(excerpt) == 0 {
		t.Fatalf("the error block has no excerpt:\n%s", b.Combined())
	}
	l := bob(t, dir, env, "log", "//libs/foo:foo")
	if l.Exit != 0 {
		t.Fatalf("bob log //libs/foo:foo: exit %d\n%s", l.Exit, l.Combined())
	}
	// the log is exactly as written (raw gcc output), so it names the file and the error.
	if !strings.Contains(l.Stdout, "foo.cpp") || !strings.Contains(l.Stdout, "error") {
		t.Errorf("the log lacks the gcc diagnostic:\n%s", l.Stdout)
	}
	ll := lines(l.Stdout)
	tail := ll[max(0, len(ll)-12):]
	for _, e := range excerpt {
		if strings.Contains(e, "…/") {
			continue // store paths are rewritten in the excerpt, raw in the log
		}
		found := false
		for _, x := range tail {
			found = found || strings.TrimRight(x, " ") == strings.TrimRight(e, " ") || strings.HasSuffix(x, e)
		}
		if !found {
			t.Errorf("excerpt line %q is not among the last lines of the log:\n%s", e, strings.Join(tail, "\n"))
		}
	}
	if b2 := bob(t, dir, env, "log", "//libs/foo:nosuch"); b2.Exit == 0 {
		t.Errorf("bob log of a label that never built: exit 0")
	}
}

// serverURLs starts the all-in-one stack (the devstack tool, HARNESS §13.2) and returns the API
// URL and an admin token. The stack's info line must publish them as `api_url` and
// `admin_token` ("URLs, ports, credentials … an admin user and a token"); while the server is a
// stub they are absent and the test is red as "not implemented".
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
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(ctx, bin, "up", "-dir", dir, "-json")
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

// stream is a bob process whose stdout lines are collected as they arrive.
type stream struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	lines []string
	done  chan struct{}
}

func startBob(t *testing.T, dir string, env []string, args ...string) *stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, harness.Bin(t, "bob"), args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &stream{cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			s.mu.Lock()
			s.lines = append(s.lines, sc.Text())
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() { cancel(); <-s.done; _ = cmd.Wait() })
	return s
}

func (s *stream) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.lines...)
}

// waitFor returns the first collected line for which match is true.
func (s *stream) waitFor(timeout time.Duration, match func(string) bool) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, l := range s.snapshot() {
			if match(l) {
				return l, true
			}
		}
		select {
		case <-s.done:
			for _, l := range s.snapshot() {
				if match(l) {
					return l, true
				}
			}
			return "", false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "", false
}

func numbered(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, strconv.Itoa(i))
	}
	return out
}

func submit(t *testing.T, dir string, env []string, label string) string {
	t.Helper()
	r := bob(t, dir, env, "build", "--remote", "--json", label)
	if harness.Red(t, r) {
		return ""
	}
	for _, e := range events(t, r.Stdout) {
		if e["event"] == "remote.submitted" {
			id, _ := e["buildId"].(string)
			return id
		}
	}
	t.Fatalf("no remote.submitted event (exit %d):\n%s", r.Exit, r.Combined())
	return ""
}

// TestRCLI008ServerLog: for a server build of a fixture that prints 1,000 numbered lines, `bob log
// --build <id> //x:y` prints all 1,000 lines in order with no duplicates, and `bob log -f` started
// mid-build ends with line 1000, no gaps and no duplicates.
func TestRCLI008ServerLog(t *testing.T) {
	parity.Covers(t, "R-CLI-008")
	url, token, ok := serverURLs(t)
	if !ok {
		return
	}
	dir := repo(t, "lines")
	env := home(t)
	r := harness.Run(t, "bob", harness.Spec{Dir: dir, Env: env, Stdin: token + "\n"}, "login", "--token-stdin", url)
	if harness.Red(t, r) {
		return
	}
	if r.Exit != 0 {
		t.Fatalf("bob login: exit %d\n%s", r.Exit, r.Combined())
	}
	id := submit(t, dir, env, "//x:y")
	if id == "" {
		return
	}
	r = bob(t, dir, env, "log", "--build", id, "//x:y")
	if r.Exit != 0 {
		t.Fatalf("bob log --build %s //x:y: exit %d\n%s", id, r.Exit, r.Combined())
	}
	got := logLines(r.Stdout)
	if fmt.Sprint(got) != fmt.Sprint(numbered(1000)) {
		t.Errorf("bob log --build: want the lines 1..1000 in order exactly once; got %d lines (first %v, last %v)", len(got), head(got), tailOf(got))
	}

	// -f started mid-build.
	slow := startBob(t, dir, env, "build", "--remote", "--json", "//x:slow")
	line, found := slow.waitFor(2*time.Minute, func(l string) bool { return strings.Contains(l, `"remote.submitted"`) })
	if !found {
		t.Fatalf("no remote.submitted from the slow build")
	}
	var sub map[string]any
	_ = json.Unmarshal([]byte(line), &sub)
	sid, _ := sub["buildId"].(string)
	time.Sleep(5 * time.Second) // mid-build: about a quarter of the lines are out
	f := bob(t, dir, env, "log", "-f", "--build", sid, "//x:slow")
	if f.Exit != 0 {
		t.Fatalf("bob log -f: exit %d\n%s", f.Exit, f.Combined())
	}
	fl := logLines(f.Stdout)
	if len(fl) == 0 || fl[len(fl)-1] != "1000" {
		t.Errorf("bob log -f does not end with line 1000: last %v", tailOf(fl))
	}
	// started mid-build: it starts at 1 (full log) and has no gaps or duplicates.
	if fmt.Sprint(fl) != fmt.Sprint(numbered(1000)) {
		t.Errorf("bob log -f: want 1..1000 without gaps or duplicates; got %d lines (first %v, last %v)", len(fl), head(fl), tailOf(fl))
	}
}

// logLines keeps the numeric lines of a log (the fixture prints nothing else but `ok` and bob's
// own header lines).
func logLines(s string) []string {
	var out []string
	for _, l := range lines(s) {
		if _, err := strconv.Atoi(strings.TrimSpace(l)); err == nil {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func head(s []string) []string   { return s[:min(3, len(s))] }
func tailOf(s []string) []string { return s[max(0, len(s)-3):] }
