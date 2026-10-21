package chaos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/chaos"
)

type proc struct {
	cmd  *exec.Cmd
	done chan error
}

func spawn(t *testing.T) *proc {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
		p.done <- nil
	})
	return p
}

func targets(t *testing.T, n int) ([]chaos.Target, []*proc) {
	var ts []chaos.Target
	var ps []*proc
	for i := range n {
		p := spawn(t)
		ps = append(ps, p)
		ts = append(ts, chaos.Target{Name: fmt.Sprintf("worker-%d", i), Pid: func() int { return p.cmd.Process.Pid }})
	}
	return ts, ps
}

func TestKillFractionIsSeededAndSIGKILLs(t *testing.T) {
	names := func(seed uint64) []string {
		ts, ps := targets(t, 10)
		var log bytes.Buffer
		d := chaos.New(seed, &log)
		killed, err := d.KillFraction(context.Background(), ts, 0.2)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, k := range killed {
			out = append(out, k.Name)
			var i int
			fmt.Sscanf(k.Name, "worker-%d", &i)
			select {
			case err := <-ps[i].done:
				ps[i].done <- err
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Errorf("%s: %v, want SIGKILL", k.Name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s still running", k.Name)
			}
		}
		if !strings.Contains(log.String(), fmt.Sprintf(`"chaos_seed":%d`, seed)) || strings.Count(log.String(), `"action":"kill"`) != 2 {
			t.Errorf("kill log:\n%s", log.String())
		}
		return out
	}
	a, b := names(7), names(7)
	if len(a) != 2 || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("seed 7 chose %v then %v", a, b)
	}
}

func state(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "gone"
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return f[0]
}

func TestPauseResumes(t *testing.T) {
	ts, _ := targets(t, 1)
	d := chaos.New(1, nil)
	errc := make(chan error, 1)
	go func() { errc <- d.Pause(context.Background(), ts[0], 300*time.Millisecond) }()
	time.Sleep(100 * time.Millisecond)
	if s := state(ts[0].Pid()); s != "T" {
		t.Errorf("during pause: state %s, want T", s)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if s := state(ts[0].Pid()); s != "S" && s != "R" {
		t.Errorf("after pause: state %s", s)
	}
}

func TestKillRestarts(t *testing.T) {
	cur := spawn(t)
	restarts := 0
	tg := chaos.Target{Name: "orch-0", Group: true, Pid: func() int { return cur.cmd.Process.Pid },
		Restart: func(context.Context) error { restarts++; cur = spawn(t); return nil }}
	d := chaos.New(3, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	d.Every(ctx, 100*time.Millisecond, func() []chaos.Target { return []chaos.Target{tg} })
	if restarts < 2 || len(d.Events()) != restarts {
		t.Fatalf("restarts %d, events %+v", restarts, d.Events())
	}
	for _, e := range d.Events() {
		if e.Err != "" {
			t.Errorf("event %+v", e)
		}
	}
}
