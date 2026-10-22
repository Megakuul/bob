package devenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// lspCheck is a scripted LSP client acting as an outside editor (SPEC §02.7.4 proof, "lspcheck"):
// it starts a shim in a root without /nix and records every frame the server side sends it.
type lspCheck struct {
	t      testing.TB
	root   string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	in     io.WriteCloser
	wmu    sync.Mutex

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	frames  [][]byte // raw bodies received from the server side
	stderr  bytes.Buffer
	done    chan struct{}
}

type lspResp struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Result json.RawMessage  `json:"result"`
	Error  json.RawMessage  `json:"error"`
}

// startLSP starts shim in a root without /nix, as the account, and initialises the session.
func startLSP(t testing.TB, w *world, root, shim string, initOpts any) *lspCheck {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	argv := w.acc.hideNix([]string{shim})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, append(w.env, shimEnv(t)...)...)
	w.acc.cred(cmd)
	c := &lspCheck{t: t, root: root, cmd: cmd, cancel: cancel, pending: map[int]chan json.RawMessage{}, done: make(chan struct{})}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = &lockedBuf{mu: &c.mu, b: &c.stderr}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: start %s: %v", shim, err)
	}
	c.in = in
	go c.read(out)
	t.Cleanup(c.close)
	res, err := c.call("initialize", map[string]any{
		"processId":             os.Getpid(),
		"rootUri":               "file://" + root,
		"workspaceFolders":      []map[string]string{{"uri": "file://" + root, "name": filepath.Base(root)}},
		"initializationOptions": initOpts,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition": map[string]any{"linkSupport": false},
				"hover":      map[string]any{"contentFormat": []string{"plaintext"}},
			},
			"workspace": map[string]any{"configuration": true},
		},
	}, 3*time.Minute)
	if err != nil {
		t.Fatalf("initialize through %s: %v\nstderr: %s", shim, err, c.errText())
	}
	_ = res
	c.notify("initialized", map[string]any{})
	return c
}

type lockedBuf struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (c *lspCheck) errText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func (c *lspCheck) send(m any) {
	b, _ := json.Marshal(m)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(b), b)
}

func (c *lspCheck) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *lspCheck) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case res := <-ch:
		var r lspResp
		_ = json.Unmarshal(res, &r)
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return nil, fmt.Errorf("%s: %s", method, r.Error)
		}
		return r.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("%s: the server side closed the connection", method)
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: no answer within %s", method, timeout)
	}
}

func (c *lspCheck) read(r io.Reader) {
	defer close(c.done)
	tp := textproto.NewReader(bufio.NewReader(r))
	for {
		h, err := tp.ReadMIMEHeader()
		if err != nil {
			return
		}
		n, err := strconv.Atoi(h.Get("Content-Length"))
		if err != nil {
			return
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(tp.R, body); err != nil {
			return
		}
		c.mu.Lock()
		c.frames = append(c.frames, body)
		c.mu.Unlock()
		var m lspResp
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method != "": // a request from the server: answer with an empty result
			var result any
			if m.Method == "workspace/configuration" {
				result = []any{}
			}
			c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		case m.ID != nil:
			var id int
			_ = json.Unmarshal(*m.ID, &id)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- body
			}
		}
	}
}

func (c *lspCheck) close() {
	_, _ = c.call("shutdown", nil, 10*time.Second)
	c.notify("exit", nil)
	_ = c.in.Close()
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
	}
	c.cancel()
	_ = c.cmd.Wait()
}

// didOpen opens a document by URI with the content of the file at path.
func (c *lspCheck) didOpen(uri, path, lang string) string {
	c.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		c.t.Fatalf("read %s: %v", path, err)
	}
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": uri, "languageId": lang, "version": 1, "text": string(b)}})
	return string(b)
}

// position returns the LSP position of offset delta into the first occurrence of needle in text.
func position(text, needle string, delta int) (map[string]int, error) {
	i := strings.Index(text, needle)
	if i < 0 {
		return nil, fmt.Errorf("%q not found", needle)
	}
	off := i + delta
	line := strings.Count(text[:off], "\n")
	// LSP columns are UTF-16 units; the fixtures are ASCII up to the needle.
	return map[string]int{"line": line, "character": off - (strings.LastIndex(text[:off], "\n") + 1)}, nil
}

// locations decodes the result of textDocument/definition (Location, Location[] or LocationLink[]).
func locations(res json.RawMessage) []string {
	var many []struct {
		URI       string `json:"uri"`
		TargetURI string `json:"targetUri"`
	}
	if json.Unmarshal(res, &many) != nil {
		var one struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(res, &one) == nil && one.URI != "" {
			return []string{one.URI}
		}
		return nil
	}
	var out []string
	for _, m := range many {
		if m.URI != "" {
			out = append(out, m.URI)
		} else {
			out = append(out, m.TargetURI)
		}
	}
	return out
}

// definition asks for the definition at pos of uri and returns the location URIs. A language
// server that is still indexing is asked again for up to 2 minutes.
func (c *lspCheck) definition(uri string, pos map[string]int) ([]string, error) {
	var last error
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		res, err := c.call("textDocument/definition", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos}, time.Minute)
		if err != nil {
			last = err
			continue
		}
		if l := locations(res); len(l) > 0 {
			return l, nil
		}
		last = fmt.Errorf("no definition yet: %s", res)
	}
	return nil, last
}

func (c *lspCheck) hover(uri string, pos map[string]int) error {
	var last error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		res, err := c.call("textDocument/hover", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos}, time.Minute)
		if err == nil && len(res) > 0 && string(res) != "null" {
			return nil
		}
		last = fmt.Errorf("hover answered %s (%v)", res, err)
	}
	return last
}

// rawStoreRefs counts the references to /nix/store that reached the client: occurrences that
// are not part of the host store path <state>/nix/store.
func (c *lspCheck) rawStoreRefs(stateHome string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	host := []string{stateHome + "/nix/store/", strings.ReplaceAll((&url.URL{Path: stateHome}).EscapedPath(), "/", "/") + "/nix/store/"}
	n := 0
	for _, f := range c.frames {
		s := strings.ReplaceAll(string(f), `\/`, "/")
		for _, h := range host {
			s = strings.ReplaceAll(s, h, "@host@/")
		}
		n += strings.Count(s, "/nix/store/")
	}
	return n
}

// hostURI checks that uri is a file URI below <state>/nix/store and returns the file path.
func hostPath(t testing.TB, uri, stateHome string) (string, bool) {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		t.Errorf("location %q is not a file URI", uri)
		return "", false
	}
	if !strings.HasPrefix(u.Path, stateHome+"/nix/store/") {
		t.Errorf("location %q is not below %s/nix/store/", uri, stateHome)
		return "", false
	}
	return u.Path, true
}

// lspCase is one language server of R-DEV-010.
type lspCase struct {
	name     string
	tool     string // shim name
	fixture  func(*world) string
	prepare  [][]string // bob commands run in the project first
	file     string     // document to open
	lang     string
	needle   string // the identifier to resolve is needle[at:at+1]
	at       int
	wantFile *regexp.Regexp // base name of the file the definition must point to
	initOpts any
}

var lspCases = []lspCase{
	{name: "gopls", tool: "gopls", fixture: func(w *world) string { return w.fixture("acme") },
		file: "cmd/api/main.go", lang: "go", needle: "fmt.Println", at: 5, wantFile: regexp.MustCompile(`/fmt/print\.go$`)},
	{name: "clangd", tool: "clangd", fixture: func(w *world) string { return w.fixture("cc") },
		prepare: [][]string{{"gen"}},
		file:    "app/main.c", lang: "c", needle: "<stdio.h>", at: 3, wantFile: regexp.MustCompile(`/include/stdio\.h$`)},
	{name: "rust-analyzer", tool: "rust-analyzer", fixture: func(w *world) string { return w.fixture("rust") },
		prepare: [][]string{{"gen"}},
		file:    "corelib/src/lib.rs", lang: "rust", needle: "-> String", at: 5, wantFile: regexp.MustCompile(`/string\.rs$`)},
	{name: "typescript-language-server", tool: "typescript-language-server", fixture: func(w *world) string { return w.local("vite-react") },
		prepare: [][]string{{"lock"}, {"gen"}, {"dev", "--no-servers", "--", "true"}},
		file:    "src/App.tsx", lang: "typescriptreact", needle: "useState(0)", at: 2, wantFile: regexp.MustCompile(`@types/react|types-react`)},
}

// TestRDEV010LSPLocations: through .bob/bin/<server>, started in a root without /nix, a
// go-to-definition into the toolchain or a dependency returns a host URI that exists, hover and
// definition work inside the opened file, and no raw /nix/store reference reaches the client.
func TestRDEV010LSPLocations(t *testing.T) {
	parity.Covers(t, "R-DEV-010")
	for _, c := range lspCases {
		t.Run(c.name, func(t *testing.T) { runLSPCase(t, c) })
	}
}

func runLSPCase(t *testing.T, c lspCase) {
	w := newWorld(t)
	dir := c.fixture(w)
	for _, args := range c.prepare {
		if r := w.bob(dir, args...); harness.Red(t, r) || !exitIs(t, r, 0) {
			return
		}
	}
	e, ok := prepare(t, w, dir)
	if !ok {
		return
	}
	if tool, has := e.Tools[c.tool]; !has || !tool.LSP {
		t.Fatalf("tools.%s = %+v in env.json, want an LSP tool", c.tool, tool)
	}
	shim := filepath.Join(dir, ".bob", "bin", c.tool)
	cl := startLSP(t, w, dir, shim, c.initOpts)
	state := w.stateHome()

	file := filepath.Join(dir, c.file)
	text := cl.didOpen("file://"+file, file, c.lang)
	pos, err := position(text, c.needle, c.at)
	if err != nil {
		t.Fatalf("%s: %v", c.file, err)
	}
	locs, err := cl.definition("file://"+file, pos)
	if err != nil {
		t.Fatalf("definition of %q: %v\nstderr: %s", c.needle, err, cl.errText())
	}
	var target, uri string
	for _, l := range locs {
		if p, ok := hostPath(t, l, state); ok && c.wantFile.MatchString(p) {
			target, uri = p, l
		}
	}
	if target == "" {
		t.Fatalf("definition of %q returned %v, want a file://%s/nix/store/… URI matching %s", c.needle, locs, state, c.wantFile)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the host path of the definition does not exist: %v", err)
	}

	// Inside the opened store file hover and definition succeed and answer with host URIs. The
	// S1 proof used print.go of the Go standard library.
	if c.name == "gopls" {
		inner := cl.didOpen(uri, target, c.lang)
		pos, err := position(inner, "func Fprintln(", 7)
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.hover(uri, pos); err != nil {
			t.Errorf("hover inside the opened std file: %v", err)
		}
		for _, call := range []string{"Fprintln(os.Stdout, a...)", "os.Stdout"} {
			cpos, err := position(inner, call, 3)
			if err != nil {
				t.Fatal(err)
			}
			dl, err := cl.definition(uri, cpos)
			if err != nil {
				t.Errorf("definition of %q inside the opened std file: %v", call, err)
				continue
			}
			for _, l := range dl {
				if p, ok := hostPath(t, l, state); ok {
					if _, err := os.Stat(p); err != nil {
						t.Errorf("definition of %q inside the std file: %v", call, err)
					}
				}
			}
		}
	}
	if n := cl.rawStoreRefs(state); n != 0 {
		t.Errorf("%d raw /nix/store references reached the client, want 0", n)
	}
	if t.Failed() {
		t.Logf("server stderr:\n%s", cl.errText())
	}
}

// TestRDEV010LSPLatency: a warm definition request through shim and proxy takes at most 2 ms on
// average [8c/15G] / 3 ms [2c/4G] (budget B-081).
func TestRDEV010LSPLatency(t *testing.T) {
	parity.Covers(t, "R-DEV-010")
	w := newWorld(t)
	dir := w.fixture("acme")
	if _, ok := prepare(t, w, dir); !ok {
		return
	}
	cl := startLSP(t, w, dir, filepath.Join(dir, ".bob", "bin", "gopls"), nil)
	file := filepath.Join(dir, "cmd", "api", "main.go")
	text := cl.didOpen("file://"+file, file, "go")
	pos, err := position(text, "fmt.Println", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.definition("file://"+file, pos); err != nil {
		t.Fatalf("definition: %v\nstderr: %s", err, cl.errText())
	}
	const runs = 20
	var total time.Duration
	for range runs {
		t0 := time.Now()
		res, err := cl.call("textDocument/definition", map[string]any{"textDocument": map[string]string{"uri": "file://" + file}, "position": pos}, time.Minute)
		total += time.Since(t0)
		if err != nil || len(locations(res)) == 0 {
			t.Fatalf("warm definition: %v %s", err, res)
		}
	}
	limit := time.Duration(profile(2, 3) * float64(time.Millisecond))
	if mean := total / runs; mean > limit {
		t.Errorf("warm definition through shim + proxy: mean %s, budget (B-081) %s", mean, limit)
	}
}
