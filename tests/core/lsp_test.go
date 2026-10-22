package core

// A minimal LSP client for the S1 LSP script (bobtest/lsp/acme.steps, R-FW-001), modelled on
// spikes/s01-bobgo/cmd/lspc.

import (
	"bufio"
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
	"time"
)

type lspMsg struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   json.RawMessage  `json:"error,omitempty"`
}

type lspDiag struct {
	Severity int    `json:"severity"`
	Message  string `json:"message"`
	Range    struct {
		Start struct{ Line, Character int } `json:"start"`
	} `json:"range"`
}

type lspClient struct {
	root     string
	cmd      *exec.Cmd
	w        io.WriteCloser
	wmu      sync.Mutex
	mu       sync.Mutex
	nextID   int
	pending  map[int]chan lspMsg
	diags    map[string][]lspDiag
	diagAt   map[string]time.Time
	settings json.RawMessage
	buffers  map[string]string
	versions map[string]int
	stderr   strings.Builder
}

func startLSP(cmd *exec.Cmd, root string, settings any) (*lspClient, error) {
	s, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	c := &lspClient{root: root, cmd: cmd, pending: map[int]chan lspMsg{}, diags: map[string][]lspDiag{},
		diagAt: map[string]time.Time{}, settings: s, buffers: map[string]string{}, versions: map[string]int{}}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &lockedWriter{mu: &c.mu, b: &c.stderr}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.w = in
	go c.read(out)
	_, err = c.call("initialize", map[string]any{
		"processId":        os.Getpid(),
		"rootUri":          "file://" + root,
		"workspaceFolders": []map[string]string{{"uri": "file://" + root, "name": filepath.Base(root)}},
		"capabilities": map[string]any{
			"workspace": map[string]any{"configuration": true},
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{},
				"completion":         map[string]any{"completionItem": map[string]any{"snippetSupport": false}},
				"hover":              map[string]any{"contentFormat": []string{"plaintext"}},
			},
		},
		"initializationOptions": json.RawMessage(s),
	}, 2*time.Minute)
	if err != nil {
		c.close()
		return nil, err
	}
	c.notify("initialized", map[string]any{})
	c.notify("workspace/didChangeConfiguration", map[string]any{"settings": json.RawMessage(s)})
	return c, nil
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (c *lspClient) send(m any) {
	b, _ := json.Marshal(m)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(b), b)
}

func (c *lspClient) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *lspClient) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan lspMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if len(m.Error) > 0 && string(m.Error) != "null" {
			return nil, fmt.Errorf("%s: %s", method, m.Error)
		}
		return m.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: no answer within %s", method, timeout)
	}
}

func (c *lspClient) read(r io.Reader) {
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
		case m.ID != nil && m.Method != "":
			// A request from the server.
			var result any
			if m.Method == "workspace/configuration" {
				var p struct{ Items []json.RawMessage }
				_ = json.Unmarshal(m.Params, &p)
				res := make([]json.RawMessage, len(p.Items))
				for i := range res {
					res[i] = c.settings
				}
				result = res
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
				ch <- m
			}
		case m.Method == "textDocument/publishDiagnostics":
			var p struct {
				URI         string    `json:"uri"`
				Diagnostics []lspDiag `json:"diagnostics"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				c.mu.Lock()
				c.diags[p.URI] = p.Diagnostics
				c.diagAt[p.URI] = time.Now()
				c.mu.Unlock()
			}
		}
	}
}

func (c *lspClient) uri(file string) string { return "file://" + filepath.Join(c.root, file) }

func (c *lspClient) open(file string) error {
	b, err := os.ReadFile(filepath.Join(c.root, file))
	if err != nil {
		return err
	}
	c.buffers[file] = string(b)
	c.versions[file] = 1
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": c.uri(file), "languageId": "go", "version": 1, "text": string(b)}})
	return nil
}

func (c *lspClient) change(file, old, new string) error {
	buf := c.buffers[file]
	if !strings.Contains(buf, old) {
		return fmt.Errorf("change %s: %q not in buffer", file, old)
	}
	buf = strings.Replace(buf, old, new, 1)
	c.buffers[file] = buf
	c.versions[file]++
	c.mu.Lock()
	delete(c.diagAt, c.uri(file))
	c.mu.Unlock()
	c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": c.uri(file), "version": c.versions[file]},
		"contentChanges": []map[string]string{{"text": buf}},
	})
	return nil
}

// diagnostics waits until the diagnostics of file have settled (1.5 s without a new
// publication, at most 90 s) and returns them.
func (c *lspClient) diagnostics(file string) []lspDiag {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		at, ok := c.diagAt[c.uri(file)]
		d := c.diags[c.uri(file)]
		c.mu.Unlock()
		if ok && time.Since(at) > 1500*time.Millisecond {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diags[c.uri(file)]
}

// position returns the LSP position at the end of the first occurrence of needle, minus back
// characters.
func (c *lspClient) position(file, needle string, back int) (map[string]int, error) {
	buf := c.buffers[file]
	i := strings.Index(buf, needle)
	if i < 0 {
		return nil, fmt.Errorf("%s: %q not found", file, needle)
	}
	off := i + len(needle) - back
	line := strings.Count(buf[:off], "\n")
	col := off - (strings.LastIndex(buf[:off], "\n") + 1)
	return map[string]int{"line": line, "character": col}, nil
}

func (c *lspClient) at(method, file, needle string, back int) (json.RawMessage, error) {
	pos, err := c.position(file, needle, back)
	if err != nil {
		return nil, err
	}
	return c.call(method, map[string]any{"textDocument": map[string]string{"uri": c.uri(file)}, "position": pos}, time.Minute)
}

func (c *lspClient) close() {
	_, _ = c.call("shutdown", nil, 10*time.Second)
	c.notify("exit", nil)
	_ = c.w.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}

// lspStep is one line of a .steps file.
type lspStep struct {
	Op   string
	File string
	Args []string
}

func parseSteps(s string) ([]lspStep, error) {
	var steps []lspStep
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		op, rest, _ := strings.Cut(line, " ")
		file, rest, _ := strings.Cut(rest, " ")
		st := lspStep{Op: op, File: file}
		for rest = strings.TrimSpace(rest); rest != ""; rest = strings.TrimSpace(rest) {
			q, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", i+1, err)
			}
			u, _ := strconv.Unquote(q)
			st.Args = append(st.Args, u)
			rest = rest[len(q):]
		}
		steps = append(steps, st)
	}
	return steps, nil
}
