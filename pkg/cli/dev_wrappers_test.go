package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/cli/devenv"
)

func TestToolWrapperMergesGoflags(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "real")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho \"$GOFLAGS|$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &devenv.File{Tools: map[string]devenv.Tool{
		"gopls": {Path: tool, Env: map[string]string{"GOFLAGS": "-tags=bob"}},
		"go":    {Path: tool},
	}}
	dir, err := writeToolWrappers(f, root)
	if err != nil || dir != toolWrapperDir(root) {
		t.Fatalf("writeToolWrappers = %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
		t.Error("a tool without variables got a wrapper")
	}
	c := exec.Command(filepath.Join(dir, "gopls"), "serve")
	c.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")
	out, err := c.Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != "-mod=readonly -tags=bob|serve" {
		t.Errorf("wrapper ran %q, %v; want the caller's GOFLAGS plus -tags=bob", got, err)
	}
	env := withPathFirst([]string{"A=1", "PATH=/usr/bin"}, dir)
	if env[1] != "PATH="+dir+":/usr/bin" {
		t.Errorf("PATH = %q", env[1])
	}
}

// TestDevWrapperGoplsSeesBobGo is the owner's case: gopls started from inside the dev shell
// (found on PATH, so the wrapper) loads a bob.go of another directory as package
// <module>/lib/asio, not as an ad hoc command-line-arguments file (A565). Plain gopls, without
// the tag, does not see the file at all. Skipped without gopls.
func TestDevWrapperGoplsSeesBobGo(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not found")
	}
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module app\n\ngo 1.21\n")
	write("lib/asio/bob.go", "//go:build bob\n\npackage asio\n\nvar Lib = 1\n")
	write("bob.go", "//go:build bob\n\npackage app\n\nimport \"app/lib/asio\"\n\nvar _ = asio.Lib\n")
	write("main.cpp", "int main() {}\n")

	f := &devenv.File{Tools: map[string]devenv.Tool{"gopls": {Path: gopls, Env: map[string]string{"GOFLAGS": "-tags=bob"}}}}
	dir, err := writeToolWrappers(f, root)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0")
	got := lspDefinition(t, filepath.Join(dir, "gopls"), root, env, "bob.go", "asio.Lib", "Lib")
	if !strings.HasSuffix(got, "lib/asio/bob.go") {
		t.Errorf("definition of asio.Lib with the wrapper = %q, want lib/asio/bob.go", got)
	}
	if plain := lspDefinition(t, gopls, root, env, "bob.go", "asio.Lib", "Lib"); plain != "" {
		t.Errorf("plain gopls (no -tags=bob) resolved %q: the test no longer shows why the wrapper exists", plain)
	}
}

// lspDefinition starts the language server exe in root, opens file and asks for the definition
// of the identifier ident (the first occurrence of text, at the offset of ident inside it).
func lspDefinition(t *testing.T, exe, root string, env []string, file, text, ident string) string {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Dir, cmd.Env = root, env
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rd := bufio.NewReader(out)
	read := func() map[string]any {
		n := 0
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return nil
			}
			if line = strings.TrimSpace(line); line == "" {
				break
			}
			if v, ok := strings.CutPrefix(strings.ToLower(line), "content-length:"); ok {
				fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
			}
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(rd, b); err != nil {
			return nil
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	send := func(m map[string]any) {
		m["jsonrpc"] = "2.0"
		b, _ := json.Marshal(m)
		fmt.Fprintf(in, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	call := func(id int, method string, params any) any {
		send(map[string]any{"id": id, "method": method, "params": params})
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			m := read()
			if m == nil {
				return nil
			}
			if _, isReq := m["method"]; isReq {
				if rid, ok := m["id"]; ok { // a request of the server: answer with nothing
					send(map[string]any{"id": rid, "result": nil})
				}
				continue
			}
			if m["id"] == float64(id) {
				return m["result"]
			}
		}
		t.Fatal("language server did not answer")
		return nil
	}
	call(1, "initialize", map[string]any{"processId": nil, "rootUri": "file://" + root, "capabilities": map[string]any{}})
	send(map[string]any{"method": "initialized", "params": map[string]any{}})
	src, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatal(err)
	}
	uri := "file://" + filepath.Join(root, file)
	send(map[string]any{"method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{
		"uri": uri, "languageId": "go", "version": 1, "text": string(src)}}})
	line, col := 0, 0
	for i, l := range strings.Split(string(src), "\n") {
		if j := strings.Index(l, text); j >= 0 {
			line, col = i, j+strings.Index(text, ident)
			break
		}
	}
	res, _ := call(2, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": line, "character": col}}).([]any)
	if len(res) == 0 {
		return ""
	}
	loc, _ := res[0].(map[string]any)
	s, _ := loc["uri"].(string)
	return strings.TrimPrefix(s, "file://")
}
