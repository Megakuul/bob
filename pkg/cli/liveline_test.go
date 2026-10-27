package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/megakuul/bob/pkg/bob/progress"
)

// fakeLine is a live line on a manual clock; the test calls redraw itself.
func fakeLine(buf *bytes.Buffer) (*liveLine, *time.Time) {
	now := time.Unix(1000, 0)
	l := &liveLine{raw: buf, now: func() time.Time { return now }, width: func() int { return 80 },
		delay: liveDelay, tick: liveTick, grace: liveGrace, quit: make(chan struct{}), done: make(chan struct{})}
	close(l.done)
	return l, &now
}

func (l *liveLine) tickOnce() {
	l.mu.Lock()
	l.redraw()
	l.mu.Unlock()
}

func TestLiveLineAppearsOnlyForSlowSteps(t *testing.T) {
	var buf bytes.Buffer
	l, now := fakeLine(&buf)
	end := l.Begin("evaluating bob.go files", false)
	*now = now.Add(200 * time.Millisecond)
	l.tickOnce()
	if buf.Len() != 0 {
		t.Fatalf("a step of 200 ms drew %q", buf.String())
	}
	*now = now.Add(300 * time.Millisecond)
	l.tickOnce()
	if got := buf.String(); got != "\r\x1b[K⠋ evaluating bob.go files" {
		t.Fatalf("after 500 ms drew %q", got)
	}
	buf.Reset()
	*now = now.Add(2 * time.Second)
	l.tickOnce()
	if got := buf.String(); got != "\r\x1b[K⠙ evaluating bob.go files · 2s" {
		t.Errorf("after 2.5 s drew %q", got)
	}
	buf.Reset()
	end()
	if buf.String() != "\r\x1b[K" {
		t.Errorf("ending the phase wrote %q, want the line erased", buf.String())
	}
	buf.Reset()
	l.tickOnce()
	if buf.Len() != 0 {
		t.Errorf("an idle line wrote %q", buf.String())
	}
}

func TestLiveLineNewestPhaseAndDetails(t *testing.T) {
	var buf bytes.Buffer
	l, now := fakeLine(&buf)
	endA := l.Begin("locking go dependencies", false)
	endD := l.Begin("fetching or building go-1.26.8", true)
	*now = now.Add(time.Second)
	l.tickOnce()
	if !strings.Contains(buf.String(), "locking go dependencies") || strings.Contains(buf.String(), "go-1.26.8") {
		t.Errorf("a detail hid its step: %q", buf.String())
	}
	endA()
	buf.Reset()
	l.tickOnce()
	if !strings.Contains(buf.String(), "fetching or building go-1.26.8") {
		t.Errorf("a detail alone is not shown: %q", buf.String())
	}
	endD()
}

func TestLiveLineOtherOutputErasesAndDelays(t *testing.T) {
	var buf, out bytes.Buffer
	l, now := fakeLine(&buf)
	w := &lineWriter{l: l, w: &out}
	end := l.Begin("planning go packages", false)
	defer end()
	*now = now.Add(time.Second)
	l.tickOnce()
	buf.Reset()
	if _, err := io.WriteString(w, "result\n"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "\r\x1b[K" || out.String() != "result\n" {
		t.Errorf("line %q, output %q: want the line erased before the text", buf.String(), out.String())
	}
	buf.Reset()
	*now = now.Add(100 * time.Millisecond)
	l.tickOnce()
	if buf.Len() != 0 {
		t.Errorf("the line came back at once after other output: %q", buf.String())
	}
	*now = now.Add(time.Second)
	l.tickOnce()
	if buf.Len() == 0 {
		t.Error("the line did not come back while the step goes on")
	}
}

func TestLiveLineShortStepsInARowCountAsOne(t *testing.T) {
	var buf bytes.Buffer
	l, now := fakeLine(&buf)
	for i := 0; i < 8; i++ {
		end := l.Begin("fetching x", false)
		*now = now.Add(100 * time.Millisecond)
		end()
		*now = now.Add(50 * time.Millisecond)
	}
	buf.Reset()
	end := l.Begin("fetching y", false)
	defer end()
	if !strings.Contains(buf.String(), "fetching y") {
		t.Errorf("a long run of short steps never showed the line: %q", buf.String())
	}
}

func TestLiveLineIsTruncatedToTheTerminal(t *testing.T) {
	var buf bytes.Buffer
	l, now := fakeLine(&buf)
	l.width = func() int { return 20 }
	end := l.Begin(strings.Repeat("x", 100), false)
	defer end()
	*now = now.Add(time.Second)
	l.tickOnce()
	line := strings.TrimPrefix(buf.String(), "\r\x1b[K")
	if n := len([]rune(line)); n > 19 {
		t.Errorf("line of %d columns on a terminal of 20: %q", n, line)
	}
}

func TestLiveWanted(t *testing.T) {
	tty, _ := openPty(t)
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for _, c := range []struct {
		name string
		o    *out
		env  func(string) string
		want bool
	}{
		{"terminal", &out{command: "lock", errw: tty}, env("TERM", "xterm"), true},
		{"piped", &out{command: "lock", errw: &bytes.Buffer{}}, env("TERM", "xterm"), false},
		{"dumb", &out{command: "lock", errw: tty}, env("TERM", "dumb"), false},
		{"json", &out{command: "lock", errw: tty, json: true}, env("TERM", "xterm"), false},
		{"quiet", &out{command: "lock", errw: tty, quiet: true}, env("TERM", "xterm"), false},
		{"debug", &out{command: "lock", errw: tty, verbose: 2}, env("TERM", "xterm"), false},
		{"ci", &out{command: "lock", errw: tty}, env("TERM", "xterm", "CI", "true"), false},
		{"opt out", &out{command: "lock", errw: tty}, env("TERM", "xterm", "BOB_PROGRESS", "0"), false},
		{"exec", &out{command: "exec", errw: tty}, env("TERM", "xterm"), false},
		{"hook", &out{command: "hook", errw: tty}, env("TERM", "xterm"), false},
	} {
		if got := liveWanted(c.o, c.env); got != c.want {
			t.Errorf("%s: liveWanted = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLiveLineOnATerminal runs a slow phase against a pseudo-terminal: the line appears, and
// it is erased before other output. With a pipe nothing is drawn and the output is unchanged.
func TestLiveLineOnATerminal(t *testing.T) {
	slave, master := openPty(t)
	env := func(k string) string {
		if k == "TERM" {
			return "xterm-256color"
		}
		return ""
	}
	o := &out{command: "lock", w: slave, errw: slave}
	ctx, stop := o.attachLive(context.Background(), env)
	end := progress.Phase(ctx, "locking go dependencies")
	time.Sleep(900 * time.Millisecond)
	end()
	io.WriteString(o.w, "bob.lock: no changes\n")
	stop()

	slave.Close()
	got := readAll(t, master)
	if !strings.Contains(got, "locking go dependencies") {
		t.Fatalf("no status line on a terminal: %q", got)
	}
	i := strings.Index(got, "bob.lock: no changes")
	if i < 0 || !strings.HasSuffix(got[:i], "\x1b[K") {
		t.Errorf("the status line was not erased before the output: %q", got)
	}
	if !strings.HasSuffix(got, "bob.lock: no changes\r\n") {
		t.Errorf("something was drawn after the output: %q", got)
	}

	var piped bytes.Buffer
	o = &out{command: "lock", w: &piped, errw: &piped}
	ctx, stop = o.attachLive(context.Background(), env)
	end = progress.Phase(ctx, "locking go dependencies")
	time.Sleep(600 * time.Millisecond)
	end()
	io.WriteString(o.w, "bob.lock: no changes\n")
	stop()
	if piped.String() != "bob.lock: no changes\n" {
		t.Errorf("piped output changed: %q", piped.String())
	}
}

// openPty returns the slave and the master of a new pseudo-terminal.
func openPty(t *testing.T) (slave, master *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skipf("unlockpt: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skipf("ptsname: %v", e)
	}
	s, err := os.OpenFile("/dev/pts/"+itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open slave: %v", err)
	}
	t.Cleanup(func() { s.Close(); m.Close() })
	return s, m
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// readAll reads what the slave side wrote, until the slave is closed (EIO) or nothing is left.
func readAll(t *testing.T, master *os.File) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	_ = master.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		n, err := master.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
