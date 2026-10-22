package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/cli/devenv"
)

// TestDevServerLifecycle: a dev server's output goes to its log (and prefixed to the foreground
// writer), readiness by log pattern and by TCP port is reported once, and stop ends the whole
// process group.
func TestDevServerLifecycle(t *testing.T) {
	root := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	var fg bytes.Buffer
	var mu sync.Mutex
	ready := map[string]int{}
	note := func(d *devServer) { mu.Lock(); ready[d.spec.Name]++; mu.Unlock() }
	env := []string{"PATH=/usr/bin:/bin"}
	logSrv, err := startServer(root, env, devenv.Server{Name: "logger", Cmd: []string{"/bin/sh", "-c", "echo starting; echo listening now; sleep 60 & wait"},
		Ready: "log", Pattern: "listening"}, &lockedWriter{w: &fg}, note)
	if err != nil {
		t.Fatal(err)
	}
	// A TCP server written in sh is not portable; a Go listener stands in, opened after start.
	tcpSrv, err := startServer(root, env, devenv.Server{Name: "tcp", Cmd: []string{"/bin/sh", "-c", "sleep 60"}, Ready: "http", Port: port}, nil, note)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := ready["logger"] + ready["tcp"]
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready["logger"] != 1 || ready["tcp"] != 1 {
		t.Fatalf("ready = %v", ready)
	}
	if u := tcpSrv.url(); u != "http://localhost:"+strconv.Itoa(port) {
		t.Errorf("url = %q", u)
	}
	pid := logSrv.cmd.Process.Pid
	logSrv.stop()
	tcpSrv.stop()
	if gone, _ := logSrv.exited(); gone {
		t.Error("a stopped server counts as exited on its own")
	}
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Error("the server's process group survived stop")
	}
	b, _ := os.ReadFile(filepath.Join(root, ".bob", "dev", "logger.log"))
	if string(b) != "starting\nlistening now\n" {
		t.Errorf("log = %q", b)
	}
	if !strings.Contains(fg.String(), "[logger] listening now\n") {
		t.Errorf("foreground output = %q", fg.String())
	}
}

// TestDevServerExitsOnItsOwn: a server that ends by itself is reported (BOB-E-2012 status).
func TestDevServerExitsOnItsOwn(t *testing.T) {
	d, err := startServer(t.TempDir(), []string{"PATH=/usr/bin:/bin"}, devenv.Server{Name: "quits", Cmd: []string{"/bin/sh", "-c", "exit 3"}}, nil, func(*devServer) {})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.done:
	case <-time.After(20 * time.Second):
		t.Fatal("the server did not end")
	}
	if gone, status := d.exited(); !gone || status != "3" {
		t.Errorf("exited = %v %q", gone, status)
	}
}
