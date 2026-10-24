package builder

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// helperBin is the service, probe and test program of these tests (testdata/helper), built
// once per test binary.
var helperBin = sync.OnceValues(func() (string, error) {
	if helperDir == "" {
		return "", fmt.Errorf("no directory for the helper binary")
	}
	bin := filepath.Join(helperDir, "helper")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/helper")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build helper: %v\n%s", err, out)
	}
	return bin, nil
})

// helperDir holds the helper binary; TestMain removes it after the tests.
var helperDir string

// TestMain returns m.Run's result through the test wrapper.
func TestMain(m *testing.M) {
	if dir, err := os.MkdirTemp("", "bob-builder-helper"); err == nil {
		helperDir = dir
		defer os.RemoveAll(dir)
	}
	m.Run()
}

func helper(t *testing.T, mode string, args ...string) []string {
	t.Helper()
	bin, err := helperBin()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{bin, mode}, args...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testRunJob(t *testing.T, a bspec.TestRunArgs) bspec.Job {
	return bspec.Job{V: 1, Kind: "test-run", Label: "//it:it", Outputs: []string{"out"}, Args: mustJSON(t, a)}
}

// gone reports whether the service whose pid file is in the build's services dir has exited.
func gone(t *testing.T, e *env, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.build, "tmp", "services", dir, "pid"))
	if err != nil {
		t.Fatalf("service pid file: %v", err)
	}
	pid, _ := strconv.Atoi(string(b))
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("service process %d still runs after the job", pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// R-FW-025: two services start in order on 127.0.0.2 and .3, the test sees <NAME>_ADDR and the
// expanded Env, the services are stopped, and a passing result with cases is written.
func TestTestRunServices(t *testing.T) {
	e := newEnv(t, "out")
	p1, p2 := strconv.Itoa(freePort(t)), strconv.Itoa(freePort(t))
	job := testRunJob(t, bspec.TestRunArgs{
		Argv:   helper(t, "test", "API_SERVER_ADDR", p2, p1),
		Format: "go",
		Services: []bspec.Service{
			{Name: "db", Argv: helper(t, "listen", p1, "--listen", "$ADDR"), Ready: bspec.Probe{Port: atoi(p1)}, TimeoutMs: 10000,
				Env: map[string]string{"DSN": "db://$ADDR:" + p1}},
			{Name: "api-server", Argv: helper(t, "listen", p2), Ready: bspec.Probe{Argv: helper(t, "probe", p2)}, TimeoutMs: 10000},
		},
	})
	code, log := e.run(t, "test-run", job)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	svcLog, _ := os.ReadFile(filepath.Join(e.build, "tmp", "services", "0-db.log"))
	if !strings.Contains(string(svcLog), "listening on 127.0.0.2 argv "+p1+" --listen 127.0.0.2") {
		t.Errorf("service 0 log %q: want address 127.0.0.2 and $ADDR expanded", svcLog)
	}
	svcLog, _ = os.ReadFile(filepath.Join(e.build, "tmp", "services", "1-api-server.log"))
	if !strings.Contains(string(svcLog), "listening on 127.0.0.3") {
		t.Errorf("service 1 log %q: want address 127.0.0.3", svcLog)
	}
	var res bspec.TestResult
	b, err := os.ReadFile(filepath.Join(e.outs["out"], "test-result.json"))
	if err != nil || json.Unmarshal(b, &res) != nil {
		t.Fatalf("test-result.json: %v %s", err, b)
	}
	if res.Version != 1 || !res.Passed || res.Label != "//it:it" || res.Duration <= 0 || len(res.Cases) != 2 ||
		res.Cases[0] != (bspec.TestCase{Name: "TestDial", Result: "pass", Duration: 0.01}) {
		t.Errorf("result %+v", res)
	}
	tl, err := os.ReadFile(filepath.Join(e.outs["out"], "test.log"))
	if err != nil || !strings.Contains(string(tl), "--- PASS: TestEnv") {
		t.Errorf("test.log: %v %q", err, tl)
	}
	gone(t, e, "0-db")
	gone(t, e, "1-api-server")
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// R-FW-025 (b): a never-ready service fails the job with "<label>: service <name> did not
// become ready within <timeout>" and the last 20 lines of its log; the service is stopped.
func TestTestRunNeverReady(t *testing.T) {
	e := newEnv(t, "out")
	job := testRunJob(t, bspec.TestRunArgs{Argv: helper(t, "test", "X", "1", "1"),
		Services: []bspec.Service{{Name: "sleeper", Argv: helper(t, "sleeper"), Ready: bspec.Probe{Port: freePort(t)}, TimeoutMs: 400}}})
	start := time.Now()
	code, log := e.run(t, "test-run", job)
	if code != ExitStep {
		t.Fatalf("exit %d, want %d:\n%s", code, ExitStep, log)
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Errorf("failed after %s, before the timeout", d)
	}
	if !strings.HasSuffix(log, "bob-builder: //it:it: service sleeper did not become ready within 400ms\n") {
		t.Errorf("last line wrong:\n%s", log)
	}
	if !strings.Contains(log, `"msg":"//it:it: service sleeper did not become ready within 400ms\nsleeper line 11\n`) {
		t.Errorf("no diagnostic with the message and the tail:\n%s", log)
	}
	for n := 11; n <= 30; n++ {
		if !strings.Contains(log, fmt.Sprintf("\nsleeper line %d\n", n)) {
			t.Errorf("log misses sleeper line %d", n)
		}
	}
	if strings.Contains(log, "sleeper line 10\n") {
		t.Errorf("more than the last 20 lines shown:\n%s", log)
	}
	if _, err := os.Stat(e.outs["out"] + "/test-result.json"); err == nil {
		t.Errorf("a result was written although the test did not run")
	}
	gone(t, e, "0-sleeper")
}

// A service that exits before it is ready fails at once with its exit status.
func TestTestRunServiceExits(t *testing.T) {
	e := newEnv(t, "out")
	job := testRunJob(t, bspec.TestRunArgs{Argv: helper(t, "test", "X", "1", "1"),
		Services: []bspec.Service{{Name: "crash", Argv: helper(t, "crash"), Ready: bspec.Probe{Port: freePort(t)}, TimeoutMs: 30000}}})
	start := time.Now()
	code, log := e.run(t, "test-run", job)
	if code != ExitStep || !strings.Contains(log, "service crash exited before it became ready (exit status 3)") ||
		!strings.Contains(log, "cannot start: config missing") {
		t.Errorf("exit %d:\n%s", code, log)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("waited for the timeout although the service had exited")
	}
}

// A failing test fails the job (never cached) after writing its result to the log.
func TestTestRunFailingTest(t *testing.T) {
	e := newEnv(t, "out")
	code, log := e.run(t, "test-run", testRunJob(t, bspec.TestRunArgs{Argv: helper(t, "fail"), Format: "go"}))
	if code != ExitStep || !strings.HasSuffix(log, "bob-builder: //it:it: test failed (exit status 1)\n") {
		t.Fatalf("exit %d:\n%s", code, log)
	}
	var line string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, `bob:: {"type":"test-result"`) {
			line = strings.TrimPrefix(l, "bob:: ")
		}
	}
	var ev struct {
		Result bspec.TestResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("no test-result line: %v\n%s", err, log)
	}
	r := ev.Result
	if r.Passed || len(r.Cases) != 2 || r.Cases[1].Result != "fail" || r.Cases[1].Output != "    b_test.go:3: fails on purpose\n" || r.Cases[0].Output != "" {
		t.Errorf("result %+v", r)
	}
}

func TestTestRunInvalid(t *testing.T) {
	svc := func(name string, p bspec.Probe, timeout int64) bspec.Service {
		return bspec.Service{Name: name, Argv: []string{"x"}, Ready: p, TimeoutMs: timeout}
	}
	for name, a := range map[string]bspec.TestRunArgs{
		"no argv":    {},
		"format":     {Argv: []string{"t"}, Format: "junit"},
		"two probes": {Argv: []string{"t"}, Services: []bspec.Service{svc("a", bspec.Probe{Port: 1, Argv: []string{"p"}}, 1)}},
		"no probe":   {Argv: []string{"t"}, Services: []bspec.Service{svc("a", bspec.Probe{}, 1)}},
		"no timeout": {Argv: []string{"t"}, Services: []bspec.Service{svc("a", bspec.Probe{Port: 1}, 0)}},
		"bad name":   {Argv: []string{"t"}, Services: []bspec.Service{svc("../a", bspec.Probe{Port: 1}, 1)}},
		"same addr":  {Argv: []string{"t"}, Services: []bspec.Service{svc("a-b", bspec.Probe{Port: 1}, 1), svc("a.b", bspec.Probe{Port: 1}, 1)}},
		"nul":        {Argv: []string{"t\x00"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "out")
			if code, log := e.run(t, "test-run", testRunJob(t, a)); code != ExitInvalid {
				t.Errorf("exit %d, want %d:\n%s", code, ExitInvalid, log)
			}
		})
	}
}

func TestAddrVar(t *testing.T) {
	for in, want := range map[string]string{"postgres": "POSTGRES_ADDR", "api-server": "API_SERVER_ADDR", "a.b_c": "A_B_C_ADDR"} {
		if got := addrVar(in); got != want {
			t.Errorf("addrVar(%q) = %q, want %q", in, got, want)
		}
	}
}
