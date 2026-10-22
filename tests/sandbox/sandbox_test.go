package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/tests/fixtures/evil"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// Limits of the suite (tests/fixtures/evil/evil.go): small enough to stop a bomb in seconds.
var limits = eval.Limits{
	WallClock:   5 * time.Second,
	IROutput:    16 << 20,
	Pids:        128,
	MemoryBytes: 512 << 20,
	WorkBytes:   256 << 20,
}

// bait is the host-side listener set a contained evaluator must never reach.
type bait struct {
	port     int
	sockPath string
	abstract string
	hits     atomic.Int64
}

func startBait(t *testing.T) *bait {
	t.Helper()
	b := &bait{
		sockPath: filepath.Join(t.TempDir(), "bait.sock"),
		abstract: fmt.Sprintf("bob-sandbox-bait-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	b.port = tcp.Addr().(*net.TCPAddr).Port
	unix, err := net.Listen("unix", b.sockPath)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	abs, err := net.Listen("unix", "@"+b.abstract)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for _, l := range []net.Listener{tcp, unix, abs} {
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				b.hits.Add(1)
				_ = c.Close()
			}
		}()
		t.Cleanup(func() { _ = l.Close() })
	}
	return b
}

// delegated reports whether this process may create cgroup v2 leaves (a delegated cgroup).
func delegated() bool {
	dir := ownCgroup()
	if dir == "" {
		return false
	}
	probe := filepath.Join(dir, fmt.Sprintf("bob-probe-%d", os.Getpid()))
	if err := os.Mkdir(probe, 0o755); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// ownCgroup is the cgroup v2 directory of this process ("" without cgroup v2).
func ownCgroup() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", p)
		}
	}
	return ""
}

// leftCgroups lists eval-* cgroup leaves below this process's cgroup.
func leftCgroups() []string {
	dir := ownCgroup()
	if dir == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasPrefix(d.Name(), "eval-") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// materialise writes the repository of case c into a fresh git-less directory.
func materialise(t *testing.T, c evil.Case, b *bait, home string) string {
	t.Helper()
	files := evil.Files(c.Name, evil.Params{Home: home, BaitPort: b.port, BaitUnix: b.sockPath, BaitAbstr: b.abstract})
	if files == nil {
		t.Fatalf("harness: no evil repository for %s", c.Name)
	}
	dir := filepath.Join(t.TempDir(), "evil")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("harness: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: git init: %v: %s", err, out)
	}
	return dir
}

// R-FW-045: each of the 15 S13 hostile cases is contained with its S13 error text, locally
// (the cgroup-only cases need a delegated cgroup and are skipped with a note without one),
// and all 15 with delegation. Under the layer-removal build tags (eval.DisabledLayer) the
// same expectations hold, because the remaining layers contain every case.
// Ruling G13: exactly the three pids cases (forkbomb-chain, forkbomb-tree, threadbomb) are
// cgroup-only skips. The membomb case is not skipped: the local default memory limit of 2 GiB is
// enforced always (RLIMIT_AS or a polling watchdog) and by the cgroup when one is delegated.
func TestRFW045HostileCases(t *testing.T) {
	parity.Covers(t, "R-FW-045")
	cases := evil.Cases()
	if len(cases) != 15 {
		t.Fatalf("the hostile suite has %d cases, want the 15 of S13", len(cases))
	}
	cgOnly := 0
	for _, c := range cases {
		if c.NeedsCG {
			cgOnly++
		}
	}
	if cgOnly != 3 {
		t.Fatalf("%d cases are marked cgroup-only, SPEC R-FW-045 allows exactly three skips", cgOnly)
	}
	cg := delegated()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.NeedsCG && !cg {
				t.Skip("no delegated cgroup: this case is only contained by pids.max (one of the three cgroup-only cases of SPEC R-FW-045)")
			}
			runCase(t, c)
		})
	}
	if left := leftCgroups(); len(left) != 0 {
		t.Errorf("evaluations left cgroups behind: %v", left)
	}
}

func runCase(t *testing.T, c evil.Case) {
	b := startBait(t)
	// A worker environment the evaluator must never see.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "bob-sandbox-secret")
	t.Setenv("BOB_WORKER_TOKEN", "bob-sandbox-secret")
	home := t.TempDir()
	creds := filepath.Join(home, ".config", "bob-worker", "credentials")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := os.WriteFile(creds, []byte("bob-sandbox-secret\n"), 0o600); err != nil {
		t.Fatalf("harness: %v", err)
	}
	dir := materialise(t, c, b, home)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	start := time.Now()
	res, err := eval.Evaluate(ctx, eval.Request{Root: dir, Patterns: []string{"//..."}, Limits: limits})
	if err != nil && strings.Contains(err.Error(), harness.NotImplemented) {
		harness.RedFeature(t, "eval.Evaluate (evaluator sandbox)")
		return
	}
	if time.Since(start) > 2*time.Minute {
		t.Errorf("case %s took %s: the limits did not stop it in time", c.Name, time.Since(start))
	}
	if n := b.hits.Load(); n != 0 {
		t.Errorf("the bait saw %d connection(s) from the evaluator", n)
	}
	if c.StopRe != "" {
		checkStopped(t, c, err)
		return
	}
	checkContained(t, c, res, err)
}

func checkStopped(t *testing.T, c evil.Case, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("case %s: the evaluation succeeded, want it stopped: %s", c.Name, c.StopRe)
	}
	text := err.Error()
	if !regexp.MustCompile(c.StopRe).MatchString(text) {
		t.Errorf("case %s: error text does not match %s:\n%s", c.Name, c.StopRe, text)
	}
	if c.HintRe != "" && !regexp.MustCompile(c.HintRe).MatchString(text) {
		t.Errorf("case %s: error text lacks the hint %s:\n%s", c.Name, c.HintRe, text)
	}
	if c.ExtraErrRe != "" && !regexp.MustCompile(c.ExtraErrRe).MatchString(text) {
		t.Errorf("case %s: error text does not show %s:\n%s", c.Name, c.ExtraErrRe, text)
	}
	if strings.Contains(text, "bob-sandbox-secret") {
		t.Errorf("case %s: the worker secret appears in the error", c.Name)
	}
}

func checkContained(t *testing.T, c evil.Case, res *eval.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("case %s: the attack was contained but the evaluation failed: %v", c.Name, err)
	}
	if len(res.IR) == 0 {
		t.Errorf("case %s: no IR written", c.Name)
	}
	stderr := string(res.Stderr)
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "LEAK ") {
			t.Errorf("case %s: attack succeeded: %s", c.Name, l)
		}
		if strings.Contains(l, "bob-sandbox-secret") {
			t.Errorf("case %s: the secret reached the evaluator: %s", c.Name, l)
		}
	}
	for _, want := range c.Contained {
		re := regexp.MustCompile(`(?m)^PASS ` + regexp.QuoteMeta(want.Name) + `: (.*)$`)
		m := re.FindStringSubmatch(stderr)
		if m == nil {
			t.Errorf("case %s: no PASS line for %q in the evaluator stderr:\n%s", c.Name, want.Name, stderr)
			continue
		}
		errRe := want.ErrRe
		if errRe != "" && strings.Contains(want.Name, "internet") {
			errRe = ""
		}
		if errRe != "" && !regexp.MustCompile(`(?i)`+errRe).MatchString(m[1]) {
			t.Errorf("case %s: %q was contained with %q, want %s", c.Name, want.Name, m[1], errRe)
		}
	}
}

// R-FW-045: the S13 syscall filter returns EPERM for ptrace, mount, unshare, io_uring and bpf
// and the Landlock layer refuses TCP with EACCES (without Landlock the netns gives
// ENETUNREACH). Only meaningful while the layer exists; the layer-removal builds assert the
// remaining layers instead.
func TestRFW045LayerErrnos(t *testing.T) {
	parity.Covers(t, "R-FW-045")
	var syscallCase, netCase evil.Case
	for _, c := range evil.Cases() {
		switch c.Name {
		case "syscalls":
			syscallCase = c
		case "network":
			netCase = c
		}
	}
	b := startBait(t)
	home := t.TempDir()
	run := func(c evil.Case) (string, bool) {
		dir := materialise(t, c, b, home)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		res, err := eval.Evaluate(ctx, eval.Request{Root: dir, Patterns: []string{"//..."}, Limits: limits})
		if err != nil && strings.Contains(err.Error(), harness.NotImplemented) {
			harness.RedFeature(t, "eval.Evaluate (evaluator sandbox)")
			return "", false
		}
		if err != nil {
			t.Fatalf("case %s: %v", c.Name, err)
		}
		return string(res.Stderr), true
	}
	out, ok := run(syscallCase)
	if !ok {
		return
	}
	if eval.DisabledLayer != "seccomp" {
		for _, name := range []string{"ptrace pid 1", "unshare(CLONE_NEWUSER)", "mount tmpfs", "io_uring_setup", "bpf()"} {
			if !regexp.MustCompile(`(?m)^PASS ` + regexp.QuoteMeta(name) + `: .*operation not permitted`).MatchString(out) {
				t.Errorf("%s is not refused with EPERM by seccomp:\n%s", name, out)
			}
		}
	}
	out, ok = run(netCase)
	if !ok {
		return
	}
	want := `permission denied`
	if eval.DisabledLayer == "landlock" {
		want = `network is unreachable`
	}
	for _, name := range []string{"internet tcp 1.1.1.1:443", "host loopback bait", "cloud metadata 169.254.169.254"} {
		if !regexp.MustCompile(`(?m)^PASS ` + regexp.QuoteMeta(name) + `: .*` + want).MatchString(out) {
			t.Errorf("%s is not refused with %q:\n%s", name, want, out)
		}
	}
}
