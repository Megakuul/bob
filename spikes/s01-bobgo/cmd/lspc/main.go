// Command lspc is a minimal LSP client (spike S1) that drives a real gopls over stdio JSON-RPC.
//
//	lspc -root DIR [-settings JSON] [-timeout 60s] -- <server command...>
//
// Steps are read from stdin, one per line:
//
//	open <file>                      didOpen
//	diag <file>                      print diagnostics once they settle (or after timeout)
//	complete <file> <needle>         completion at the end of the first occurrence of needle
//	def <file> <needle>              definition at the end of needle (minus 1 char)
//	hover <file> <needle>
//	change <file> <old> <new>        replace first occurrence of old in the buffer, didChange
//	sleep <ms>
//
// <needle>/<old>/<new> are Go-quoted strings. Output: one JSON object per step on stdout.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type client struct {
	w        io.Writer
	wmu      sync.Mutex
	nextID   int
	pending  map[int]chan msg
	mu       sync.Mutex
	diags    map[string]json.RawMessage
	diagAt   map[string]time.Time
	settings json.RawMessage
	logs     []string
}

func (c *client) send(m any) {
	b, _ := json.Marshal(m)
	c.wmu.Lock()
	fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(b), b)
	c.wmu.Unlock()
}

func (c *client) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan msg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if len(m.Error) > 0 {
			return nil, fmt.Errorf("%s", m.Error)
		}
		return m.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: timeout", method)
	}
}

func (c *client) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) readLoop(r *bufio.Reader) {
	for {
		n := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				n, _ = strconv.Atoi(v)
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		var m msg
		if json.Unmarshal(buf, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && m.ID != nil: // server -> client request
			var result any
			if m.Method == "workspace/configuration" {
				var p struct{ Items []any }
				json.Unmarshal(m.Params, &p)
				arr := make([]json.RawMessage, len(p.Items))
				for i := range arr {
					arr[i] = c.settings
				}
				result = arr
			}
			c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		case m.Method == "textDocument/publishDiagnostics":
			var p struct {
				URI         string          `json:"uri"`
				Diagnostics json.RawMessage `json:"diagnostics"`
			}
			json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			c.diags[p.URI] = p.Diagnostics
			c.diagAt[p.URI] = time.Now()
			c.mu.Unlock()
		case m.Method == "window/showMessage" || m.Method == "window/logMessage":
			var p struct{ Message string }
			json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			c.logs = append(c.logs, p.Message)
			c.mu.Unlock()
		case m.Method == "":
			var id int
			json.Unmarshal(*m.ID, &id)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

func uri(p string) string { return "file://" + p }

func pos(text, needle string, back int) (int, int, bool) {
	i := strings.Index(text, needle)
	if i < 0 {
		return 0, 0, false
	}
	off := i + len(needle) - back
	line := strings.Count(text[:off], "\n")
	col := off - (strings.LastIndex(text[:off], "\n") + 1)
	return line, col, true // ASCII only: UTF-16 == bytes
}

func main() {
	root := flag.String("root", ".", "workspace root")
	settings := flag.String("settings", "{}", "gopls settings (JSON object), used for initializationOptions and workspace/configuration")
	timeout := flag.Duration("timeout", 90*time.Second, "per-step timeout")
	flag.Parse()
	absRoot, _ := filepath.Abs(*root)
	start := time.Now()
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Dir = absRoot
	cmd.Stderr = os.Stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c := &client{w: stdin, pending: map[int]chan msg{}, diags: map[string]json.RawMessage{}, diagAt: map[string]time.Time{}, settings: json.RawMessage(*settings)}
	go c.readLoop(bufio.NewReaderSize(stdout, 1<<20))
	out := json.NewEncoder(os.Stdout)
	emit := func(m map[string]any) { m["t_ms"] = time.Since(start).Milliseconds(); out.Encode(m) }

	_, err := c.call("initialize", map[string]any{
		"processId": os.Getpid(), "rootUri": uri(absRoot),
		"workspaceFolders":      []any{map[string]string{"uri": uri(absRoot), "name": "ws"}},
		"initializationOptions": json.RawMessage(*settings),
		"capabilities": map[string]any{
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true},
			"textDocument": map[string]any{
				"completion":         map[string]any{"completionItem": map[string]any{"snippetSupport": false}},
				"publishDiagnostics": map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"plaintext"}},
			},
			"window": map[string]any{"workDoneProgress": false},
		},
	}, *timeout)
	if err != nil {
		emit(map[string]any{"step": "initialize", "error": err.Error()})
		os.Exit(1)
	}
	c.notify("initialized", map[string]any{})
	emit(map[string]any{"step": "initialize", "ok": true})

	texts := map[string]string{}
	versions := map[string]int{}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := splitArgs(line)
		file := ""
		if len(f) > 1 {
			file = filepath.Join(absRoot, f[1])
		}
		res := map[string]any{"step": line}
		switch f[0] {
		case "open":
			b, err := os.ReadFile(file)
			if err != nil {
				res["error"] = err.Error()
				break
			}
			texts[file] = string(b)
			versions[file] = 1
			c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri(file), "languageId": "go", "version": 1, "text": string(b)}})
			res["ok"] = true
		case "change":
			t := texts[file]
			if !strings.Contains(t, f[2]) {
				res["error"] = "old text not found"
				break
			}
			t = strings.Replace(t, f[2], f[3], 1)
			texts[file] = t
			versions[file]++
			c.mu.Lock()
			delete(c.diagAt, uri(file))
			c.mu.Unlock()
			c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri(file), "version": versions[file]}, "contentChanges": []any{map[string]string{"text": t}}})
			res["ok"] = true
		case "diag":
			// wait until diagnostics for the file arrived and nothing new came for 1.5 s
			deadline := time.Now().Add(*timeout)
			for time.Now().Before(deadline) {
				c.mu.Lock()
				at, ok := c.diagAt[uri(file)]
				c.mu.Unlock()
				if ok && time.Since(at) > 1500*time.Millisecond {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			c.mu.Lock()
			d, ok := c.diags[uri(file)]
			c.mu.Unlock()
			if !ok {
				res["error"] = "no diagnostics published"
				break
			}
			var ds []struct {
				Range struct {
					Start struct{ Line, Character int }
				}
				Severity int
				Message  string
				Source   string
			}
			json.Unmarshal(d, &ds)
			var list []string
			for _, x := range ds {
				list = append(list, fmt.Sprintf("%d:%d sev%d %s: %s", x.Range.Start.Line+1, x.Range.Start.Character+1, x.Severity, x.Source, x.Message))
			}
			res["diagnostics"] = list
		case "complete", "def", "hover":
			back := 0
			if f[0] != "complete" {
				back = 1
			}
			l, col, ok := pos(texts[file], f[2], back)
			if !ok {
				res["error"] = "needle not found"
				break
			}
			method := map[string]string{"complete": "textDocument/completion", "def": "textDocument/definition", "hover": "textDocument/hover"}[f[0]]
			t := time.Now()
			r, err := c.call(method, map[string]any{"textDocument": map[string]string{"uri": uri(file)}, "position": map[string]int{"line": l, "character": col}}, *timeout)
			res["latency_ms"] = time.Since(t).Milliseconds()
			if err != nil {
				res["error"] = err.Error()
				break
			}
			switch f[0] {
			case "complete":
				var cl struct {
					Items []struct{ Label, Detail string }
				}
				json.Unmarshal(r, &cl)
				var labels []string
				for i, it := range cl.Items {
					if i < 15 {
						labels = append(labels, it.Label)
					}
				}
				res["count"] = len(cl.Items)
				res["items"] = labels
			case "def":
				var locs []struct {
					URI   string `json:"uri"`
					Range struct{ Start struct{ Line int } }
				}
				json.Unmarshal(r, &locs)
				var s []string
				for _, x := range locs {
					s = append(s, fmt.Sprintf("%s:%d", strings.TrimPrefix(strings.TrimPrefix(x.URI, uri(absRoot)), "file://"), x.Range.Start.Line+1))
				}
				res["locations"] = s
			case "hover":
				var h struct{ Contents struct{ Value string } }
				json.Unmarshal(r, &h)
				v := h.Contents.Value
				if len(v) > 200 {
					v = v[:200]
				}
				res["hover"] = v
			}
		case "sleep":
			n, _ := strconv.Atoi(f[1])
			time.Sleep(time.Duration(n) * time.Millisecond)
			res["ok"] = true
		default:
			res["error"] = "unknown step"
		}
		emit(res)
	}
	c.mu.Lock()
	var warn []string
	for _, l := range c.logs {
		if strings.Contains(l, "rror") || strings.Contains(l, "arn") || strings.Contains(l, "No packages") || strings.Contains(l, "not included") {
			if len(l) > 300 {
				l = l[:300]
			}
			warn = append(warn, l)
		}
	}
	c.mu.Unlock()
	emit(map[string]any{"step": "server-messages", "messages": warn})
	c.call("shutdown", nil, 10*time.Second)
	c.notify("exit", nil)
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			emit(map[string]any{"step": "exit", "server_maxrss_mb": ru.Maxrss / 1024})
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
	}
}

func splitArgs(line string) []string {
	var out []string
	for line != "" {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			break
		}
		if line[0] == '"' {
			s, err := strconv.QuotedPrefix(line)
			if err == nil {
				u, _ := strconv.Unquote(s)
				out = append(out, u)
				line = line[len(s):]
				continue
			}
		}
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			out = append(out, line)
			break
		}
		out = append(out, line[:i])
		line = line[i:]
	}
	return out
}
