package devenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
)

// session of a long-running bob process whose output is collected.
type bobProc struct {
	t    testing.TB
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
	err  error
}

// startBob starts bob as the account in its own process group; it is killed when the test ends.
func startBob(t testing.TB, w *world, dir string, args ...string) *bobProc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(ctx, bobBin(t), args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, w.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	w.acc.cred(cmd)
	p := &bobProc{t: t, cmd: cmd, done: make(chan struct{})}
	pw := &lockedBuf{mu: &p.mu, b: &p.out}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness error: start bob: %v", err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
		cancel()
	})
	return p
}

func (p *bobProc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// waitOutput waits until the output matches re and returns the first match.
func (p *bobProc) waitOutput(re *regexp.Regexp, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := re.FindString(p.output()); m != "" {
			return m, true
		}
		select {
		case <-p.done:
			if m := re.FindString(p.output()); m != "" {
				return m, true
			}
			return "", false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return "", false
}

// descendants returns the pids below root.
func descendants(root int) map[int]string {
	ents, _ := os.ReadDir("/proc")
	parent := map[int]int{}
	cmdline := map[int]string{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		s := string(st)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
		if len(f) < 2 {
			continue
		}
		parent[pid], _ = strconv.Atoi(f[1])
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		cmdline[pid] = strings.ReplaceAll(string(b), "\x00", " ")
	}
	out := map[int]string{}
	for pid := range parent {
		for p := pid; p > 1; p = parent[p] {
			if p == root && pid != root {
				out[pid] = cmdline[pid]
				break
			}
		}
	}
	return out
}

// get fetches path from the dev server.
func get(port int, path string) (int, string, error) {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// waitGet polls until path answers 200.
func waitGet(port int, path string, timeout time.Duration) (string, error) {
	var last error
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		code, body, err := get(port, path)
		if err == nil && code == 200 {
			return body, nil
		}
		last = fmt.Errorf("status %d, %v", code, err)
	}
	return "", last
}

// wsClient is a minimal RFC 6455 client for Vite's HMR socket (subprotocol vite-hmr).
type wsClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func wsDial(port int, token string) (*wsClient, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	fmt.Fprintf(conn, "GET /?token=%s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nOrigin: http://127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: vite-hmr\r\n\r\n", token, port, port, k)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: status %d", resp.StatusCode)
	}
	return &wsClient{conn: conn, r: r}, nil
}

// next returns the next text message (control frames are skipped).
func (c *wsClient) next(timeout time.Duration) (string, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return "", err
		}
		n := int(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return "", err
			}
			n = int(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return "", err
			}
			n = int(binary.BigEndian.Uint64(b[:]))
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return "", err
		}
		switch op := h[0] & 0x0f; op {
		case 0x8:
			return "", io.EOF
		case 0x1, 0x0:
			msg = append(msg, payload...)
			if h[0]&0x80 != 0 {
				return string(msg), nil
			}
		}
	}
}

var wsToken = regexp.MustCompile(`wsToken\s*=\s*"([^"]+)"`)

// TestRDEV002ViteLiveReload: bob dev --servers-only serves the vite-react app; saves from outside
// reach the HMR socket within 50 ms (p95); adding an npm dependency in bob.go materialises it
// within 10 s and the server answers again; exit leaves no process of the session.
func TestRDEV002ViteLiveReload(t *testing.T) {
	parity.Covers(t, "R-DEV-002")
	w := newWorld(t)
	dir := w.local("vite-react")
	port, err := serverenv.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	bobgo := filepath.Join(dir, "bob.go")
	src := readFile(t, bobgo)
	writeFile(t, bobgo, strings.Replace(src, "npm.ViteOpts{Deps:", fmt.Sprintf("npm.ViteOpts{Dev: npm.DevServer{Port: %d}, Deps:", port), 1))
	w.acc.chownTree(t, dir)
	if r := w.bob(dir, "lock"); harness.Red(t, r) || !exitIs(t, r, 0) {
		return
	}

	p := startBob(t, w, dir, "dev", "--servers-only")
	ready, ok := p.waitOutput(regexp.MustCompile(`ready at http://[^\s]+`), 3*time.Minute)
	if !ok {
		out := p.output()
		if strings.Contains(out, harness.NotImplemented) {
			harness.RedFeature(t, "bob dev --servers-only -> "+strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
			return
		}
		t.Fatalf("bob dev --servers-only printed no readiness line:\n%s", out)
	}
	t.Logf("bob: %s", ready)
	if !regexp.MustCompile(`(?m)^\[[^\]]*vite[^\]]*\] `).MatchString(p.output()) {
		t.Errorf("--servers-only output lines have no [server] prefix:\n%s", p.output())
	}
	root, err := waitGet(port, "/", time.Second)
	if err != nil {
		t.Fatalf("GET / is not 200 within 1 s of the readiness line (budget B-015): %v\n%s", err, p.output())
	}
	if !strings.Contains(root, `id="root"`) {
		t.Errorf("GET / is not the app's index.html: %.200s", root)
	}

	// The HMR socket. Vite only pushes updates for modules the client has loaded.
	client, err := waitGet(port, "/@vite/client", 5*time.Second)
	if err != nil {
		t.Fatalf("GET /@vite/client: %v", err)
	}
	m := wsToken.FindStringSubmatch(client)
	if m == nil {
		t.Fatalf("/@vite/client has no wsToken")
	}
	ws, err := wsDial(port, m[1])
	if err != nil {
		t.Fatalf("HMR websocket: %v", err)
	}
	defer ws.conn.Close()
	if first, err := ws.next(10 * time.Second); err != nil || !strings.Contains(first, `"connected"`) {
		t.Fatalf("first HMR message = %q (%v), want type connected", first, err)
	}
	for _, mod := range []string{"/src/main.tsx", "/src/App.tsx"} {
		if _, err := waitGet(port, mod, 10*time.Second); err != nil {
			t.Fatalf("GET %s: %v", mod, err)
		}
	}
	app := filepath.Join(dir, "src", "App.tsx")
	base := readFile(t, app)
	var ds []time.Duration
	for i := 2; i < 22; i++ {
		next := strings.Replace(base, "marker-v1", "marker-v"+strconv.Itoa(i), 1)
		t0 := time.Now()
		if err := os.WriteFile(app, []byte(next), 0o644); err != nil { // a save from outside the namespace
			t.Fatal(err)
		}
		for {
			msg, err := ws.next(5 * time.Second)
			if err != nil {
				t.Fatalf("save %d: no HMR message: %v", i-1, err)
			}
			var u struct {
				Type    string `json:"type"`
				Updates []struct {
					Path string `json:"path"`
				} `json:"updates"`
			}
			if json.Unmarshal([]byte(msg), &u) != nil || u.Type != "update" {
				continue
			}
			ds = append(ds, time.Since(t0))
			if len(u.Updates) == 0 || !strings.HasSuffix(u.Updates[0].Path, "/src/App.tsx") {
				t.Errorf("save %d: update for %v, want /src/App.tsx", i-1, u.Updates)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(ds) == 20 {
		if got := p95(ds); got > 50*time.Millisecond {
			t.Errorf("HMR update p95 = %s, budget (B-082) 50 ms", got)
		}
	}

	// A dependency added in bob.go is relocked, materialised and the server answers again.
	cur := readFile(t, bobgo)
	writeFile(t, bobgo, strings.Replace(cur, `npm.Dep("react", "^19.1.0"),`, `npm.Dep("react", "^19.1.0"),
		npm.Dep("nanoid", "^5.1.0"),`, 1))
	t0 := time.Now()
	modDir := filepath.Join(dir, "node_modules", "nanoid")
	for !exists(modDir) && time.Since(t0) < 10*time.Second {
		time.Sleep(100 * time.Millisecond)
	}
	if !exists(modDir) {
		t.Errorf("node_modules/nanoid does not exist 10 s after the change (budget B-082)")
	}
	if _, err := waitGet(port, "/", max(time.Second, time.Until(t0.Add(10*time.Second)))); err != nil {
		t.Errorf("the server does not answer within 10 s of the dependency change: %v", err)
	}

	// Exit: SIGTERM to bob; no process of the session is left.
	session := descendants(p.cmd.Process.Pid)
	if len(session) == 0 {
		t.Errorf("bob dev has no child processes while serving (where does vite run?)")
	}
	if err := syscall.Kill(p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("bob dev did not exit within 20 s of SIGTERM")
	}
	for pid, cmdline := range session {
		for range 50 {
			if !pidAlive(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if pidAlive(pid) {
			t.Errorf("process %d (%s) of the session is still running after bob dev exited", pid, cmdline)
		}
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
		conn.Close()
		t.Errorf("port %d still accepts connections after bob dev exited", port)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, ".bob", "dev", "*.log"))
	if len(logs) == 0 {
		t.Errorf(".bob/dev/<server>.log does not exist")
	}
}
