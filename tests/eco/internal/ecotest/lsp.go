package ecotest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// LSP is a scripted language-server client: it starts a server (a bob shim) on stdio and speaks
// JSON-RPC with Content-Length framing, like an editor would.
type LSP struct {
	t       testing.TB
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	in      io.WriteCloser
	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	stderr  bytes.Buffer
	done    chan struct{}
}

type lspMsg struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Result json.RawMessage  `json:"result"`
	Error  json.RawMessage  `json:"error"`
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

// StartLSP starts binary in root with env and initialises the session.
func StartLSP(t testing.TB, root, binary string, env []string) *LSP {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = root
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, env...)
	c := &LSP{t: t, cmd: cmd, cancel: cancel, pending: map[int]chan json.RawMessage{}, done: make(chan struct{})}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	cmd.Stderr = &lockedBuf{mu: &c.mu, b: &c.stderr}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: start %s: %v", binary, err)
	}
	c.in = in
	go c.read(out)
	t.Cleanup(c.close)
	_, err = c.call("initialize", map[string]any{
		"processId":        os.Getpid(),
		"rootUri":          "file://" + root,
		"workspaceFolders": []map[string]string{{"uri": "file://" + root, "name": filepath.Base(root)}},
		"capabilities": map[string]any{"textDocument": map[string]any{
			"definition": map[string]any{"linkSupport": false},
			"hover":      map[string]any{"contentFormat": []string{"plaintext"}},
		}},
	}, 3*time.Minute)
	if err != nil {
		t.Fatalf("initialize through %s: %v\nstderr: %s", binary, err, c.Stderr())
	}
	c.notify("initialized", map[string]any{})
	return c
}

// Stderr is what the server wrote to stderr so far.
func (c *LSP) Stderr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func (c *LSP) send(m any) {
	b, _ := json.Marshal(m)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(b), b)
}

func (c *LSP) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *LSP) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case res := <-ch:
		var r lspMsg
		_ = json.Unmarshal(res, &r)
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return nil, fmt.Errorf("%s: %s", method, r.Error)
		}
		return r.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("%s: the server closed the connection", method)
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: no answer within %s", method, timeout)
	}
}

func (c *LSP) read(r io.Reader) {
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
		var m lspMsg
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method != "": // a request of the server: answer with an empty result
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

func (c *LSP) close() {
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

// DidOpen opens the file at path as a document and returns its text.
func (c *LSP) DidOpen(path, lang string) (uri, text string) {
	c.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		c.t.Fatalf("read %s: %v", path, err)
	}
	uri = "file://" + path
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": uri, "languageId": lang, "version": 1, "text": string(b)}})
	return uri, string(b)
}

// Position returns the LSP position delta bytes into the first occurrence of needle in text
// (ASCII sources only).
func Position(text, needle string, delta int) (map[string]int, error) {
	i := strings.Index(text, needle)
	if i < 0 {
		return nil, fmt.Errorf("%q not found", needle)
	}
	off := i + delta
	return map[string]int{"line": strings.Count(text[:off], "\n"), "character": off - (strings.LastIndex(text[:off], "\n") + 1)}, nil
}

// Definition asks for the definition at pos and returns the location URIs; a server that is
// still indexing is asked again for up to 2 minutes.
func (c *LSP) Definition(uri string, pos map[string]int) ([]string, error) {
	var last error
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		res, err := c.call("textDocument/definition", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos}, time.Minute)
		if err != nil {
			last = err
			continue
		}
		var many []struct {
			URI       string `json:"uri"`
			TargetURI string `json:"targetUri"`
		}
		var one struct {
			URI string `json:"uri"`
		}
		var out []string
		if json.Unmarshal(res, &many) == nil {
			for _, m := range many {
				out = append(out, m.URI+m.TargetURI)
			}
		} else if json.Unmarshal(res, &one) == nil && one.URI != "" {
			out = []string{one.URI}
		}
		if len(out) > 0 {
			return out, nil
		}
		last = fmt.Errorf("no definition yet: %s", res)
	}
	return nil, last
}

// Hover returns the hover text at pos (asked again while the server is indexing, up to 1 min).
func (c *LSP) Hover(uri string, pos map[string]int) (string, error) {
	var last error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		res, err := c.call("textDocument/hover", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos}, time.Minute)
		if err == nil && len(res) > 0 && string(res) != "null" {
			return string(res), nil
		}
		last = fmt.Errorf("hover answered %s (%v)", res, err)
	}
	return "", last
}
