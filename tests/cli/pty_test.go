package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/megakuul/bob/tests/harness"
)

// runPTY runs bob with a pseudo-terminal as stdin, stdout and stderr (the "TTY" of SPEC §02.5)
// and returns everything bob wrote to it. The environment is the harness's minimal one plus env.
func runPTY(t testing.TB, dir string, env []string, args ...string) (out string, exit int) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("harness: open /dev/ptmx: %v: %v", err, errors.New("harness error"))
	}
	defer master.Close()
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlock))); e != 0 { // TIOCSPTLCK
		t.Fatalf("harness: unlock pty: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&n))); e != 0 { // TIOCGPTN
		t.Fatalf("harness: pty number: %v", e)
	}
	slave, err := os.OpenFile("/dev/pts/"+itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("harness: open pty slave: %v", err)
	}
	defer slave.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, harness.Bin(t, "bob"), args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TZ=UTC", "LC_ALL=C"}, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: start bob: %v", err)
	}
	slave.Close()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(&buf, master) // EIO when the last slave fd closes
	}()
	werr := cmd.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		master.Close()
		<-done
	}
	exit = 0
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		exit = ee.ExitCode()
	} else if werr != nil {
		exit = -1
	}
	return buf.String(), exit
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
