package npm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
)

// devProc is a long-running `bob dev` whose output is collected; it runs as an unprivileged
// user in its own process group and is killed when the test ends.
type devProc struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
}

type lockedWriter struct{ p *devProc }

func (w lockedWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.out.Write(b)
}

func startDev(t *testing.T, uid, gid uint32, dir string, env []string, args ...string) *devProc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(ctx, harness.Bin(t, "bob"), args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if uid != uint32(syscall.Getuid()) {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
	p := &devProc{cmd: cmd, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = lockedWriter{p}, lockedWriter{p}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: start bob: %v", err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
		cancel()
	})
	return p
}

func (p *devProc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// waitOutput waits until the output matches re.
func (p *devProc) waitOutput(re *regexp.Regexp, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if re.MatchString(p.output()) {
			return true
		}
		select {
		case <-p.done:
			return re.MatchString(p.output())
		case <-time.After(20 * time.Millisecond):
		}
	}
	return false
}

// httpGet fetches path of the dev server.
func httpGet(port int, path string) (int, string, error) {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// waitGet polls until path answers 200 and (when want is set) the body matches want.
func waitGet(port int, path string, want *regexp.Regexp, timeout time.Duration) (string, error) {
	var last error
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		code, body, err := httpGet(port, path)
		switch {
		case err != nil || code != 200:
			last = fmt.Errorf("status %d, %v", code, err)
		case want != nil && !want.MatchString(body):
			last = fmt.Errorf("body does not match %s", want)
		default:
			return body, nil
		}
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
		_ = conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		_ = conn.Close()
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
		switch h[0] & 0x0f {
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
