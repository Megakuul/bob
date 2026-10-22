package builder

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/builder/bspec"
)

// Limits of kind test-run.
const (
	maxServices   = 64        // 127.0.0.2 … 127.0.0.65
	maxCaseOutput = 64 << 10  // TestCase.Output (SPEC §03.0.5)
	serviceTail   = 20        // log lines shown when a service fails (R-FW-025)
	tailBytes     = 256 << 10 // how much of a service log is read for its tail
	stopGrace     = 5 * time.Second
)

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// testRunKind is kind "test-run" (SPEC §03.0.5, R-FW-024, R-FW-025): it starts the services in
// list order, waits for each one's probe, runs the test binary once with HOME=$TMPDIR, stops
// the services and writes $out/test-result.json and $out/test.log. A failing test fails the
// derivation after the result went to the build log, so failures are never cached.
func testRunKind(c *Ctx, raw json.RawMessage) error {
	var a bspec.TestRunArgs
	if err := decodeStrict(raw, &a); err != nil {
		return Invalidf("test-run args: %v", err)
	}
	if err := checkTestRun(a); err != nil {
		return err
	}
	out := c.Outputs["out"]
	if out == "" {
		return Invalidf("test-run declares no output \"out\"")
	}
	for _, cp := range a.Copy {
		if err := c.copyAny(cp.From, cp.To); err != nil {
			return err
		}
	}
	dir := c.BuildTop
	if a.Cwd != "" {
		cwd, err := c.Writable(a.Cwd)
		if err != nil {
			return err
		}
		dir = cwd
	}
	tmp, err := c.TmpDir()
	if err != nil {
		return err
	}
	prog, err := lookPath(a.Argv[0], a.Path)
	if err != nil {
		return err
	}
	if err := c.MkdirAll(out); err != nil {
		return err
	}

	// The duration covers the services' start too: it is what the unit costs (R-FW-025 bound).
	start := time.Now()
	svcs := &services{c: c, tmp: tmp}
	defer svcs.stop()
	testEnv := map[string]string{}
	for i, s := range a.Services {
		c.Phase("service " + s.Name)
		env, err := svcs.start(i, s)
		if err != nil {
			return err
		}
		maps.Copy(testEnv, env)
	}
	maps.Copy(testEnv, a.Env)

	c.Phase("test")
	logPath := filepath.Join(out, "test.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	env := c.baseEnv(a.Path, tmp)
	env["HOME"] = tmp
	maps.Copy(env, testEnv)
	cmd := &exec.Cmd{Path: prog, Args: a.Argv, Env: envList(env), Dir: dir,
		Stdout: io.MultiWriter(logFile, c.stderr), Stderr: io.MultiWriter(logFile, c.stderr),
		// A process the test left behind may hold the output pipe; do not wait for it forever.
		WaitDelay: 10 * time.Second}
	timeoutS := a.TimeoutS
	if timeoutS == 0 {
		timeoutS = bspec.DefaultTestTimeoutS
	}
	var timedOut atomic.Bool
	runErr := cmd.Start()
	if runErr == nil {
		proc := cmd.Process
		timer := time.AfterFunc(time.Duration(timeoutS)*time.Second, func() {
			timedOut.Store(true)
			_ = proc.Kill()
		})
		runErr = cmd.Wait()
		timer.Stop()
	}
	took := time.Since(start)
	if err := logFile.Close(); err != nil && runErr == nil {
		runErr = err
	}
	svcs.stop()

	res := bspec.TestResult{Version: 1, Label: c.Job.Label, Passed: runErr == nil, Duration: took.Seconds(), Cases: []bspec.TestCase{}}
	if a.Format == "go" {
		cases, err := goCases(logPath)
		if err != nil {
			return err
		}
		res.Cases = cases
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if runErr != nil {
		// The result goes to the build log; the derivation fails, nothing is cached.
		line, _ := json.Marshal(struct {
			Type   string           `json:"type"`
			Result bspec.TestResult `json:"result"`
		}{"test-result", res})
		fmt.Fprintf(c.stderr, "bob:: %s\n", line)
		if timedOut.Load() {
			c.Diag("error", "", 0, 0, fmt.Sprintf("%s timed out after %ds", c.Job.Label, timeoutS))
			return fmt.Errorf("timed out after %ds", timeoutS)
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return fmt.Errorf("test failed (%s)", ee.ProcessState.String())
		}
		return fmt.Errorf("test %s: %w", a.Argv[0], runErr)
	}
	return writeFile(filepath.Join(out, "test-result.json"), 0o644, strings.NewReader(string(b)+"\n"), int64(len(b)+1))
}

// checkTestRun validates the arguments before anything starts.
func checkTestRun(a bspec.TestRunArgs) error {
	if len(a.Argv) == 0 || a.Argv[0] == "" {
		return Invalidf("test-run without argv")
	}
	if a.Format != "" && a.Format != "go" {
		return Invalidf("test-run format %q is not \"\" or \"go\"", a.Format)
	}
	if a.TimeoutS < 0 {
		return Invalidf("test-run timeout %d is negative", a.TimeoutS)
	}
	if len(a.Services) > maxServices {
		return Invalidf("test-run has %d services, at most %d are allowed", len(a.Services), maxServices)
	}
	if err := checkStrings(a.Argv, a.Path, a.Env); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range a.Services {
		if !serviceName.MatchString(s.Name) {
			return Invalidf("service name %q is invalid", s.Name)
		}
		if seen[addrVar(s.Name)] {
			return Invalidf("two services export %s", addrVar(s.Name))
		}
		seen[addrVar(s.Name)] = true
		if len(s.Argv) == 0 || s.Argv[0] == "" {
			return Invalidf("service %s has no argv", s.Name)
		}
		if (s.Ready.Port == 0) == (len(s.Ready.Argv) == 0) {
			return Invalidf("service %s needs exactly one readiness probe (port or argv)", s.Name)
		}
		if s.Ready.Port < 0 || s.Ready.Port > 65535 {
			return Invalidf("service %s probes port %d", s.Name, s.Ready.Port)
		}
		if s.TimeoutMs <= 0 {
			return Invalidf("service %s has no readiness timeout", s.Name)
		}
		if err := checkStrings(append(slices.Clone(s.Argv), s.Ready.Argv...), s.Path, s.Env); err != nil {
			return err
		}
	}
	return nil
}

func checkStrings(argv, path []string, env map[string]string) error {
	for _, s := range append(slices.Clone(argv), path...) {
		if strings.ContainsRune(s, 0) {
			return Invalidf("argument with a NUL byte")
		}
	}
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return Invalidf("environment variable %q is invalid", k)
		}
	}
	return nil
}

// addrVar is the variable that carries a service's address: <NAME>_ADDR, upper-cased, every
// character outside [A-Z0-9_] replaced by '_'.
func addrVar(name string) string {
	b := []byte(strings.ToUpper(name))
	for i, ch := range b {
		if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			b[i] = '_'
		}
	}
	return string(b) + "_ADDR"
}

// baseEnv is the environment of R-NIX-016 for a program with the given PATH.
func (c *Ctx) baseEnv(path []string, tmp string) map[string]string {
	env := map[string]string{
		"PATH": strings.Join(path, ":"), "HOME": "/homeless-shelter", "TMPDIR": tmp,
		"NIX_BUILD_CORES": c.cores, "SOURCE_DATE_EPOCH": "1", "TZ": "UTC", "LC_ALL": "C",
	}
	for _, o := range c.Job.Outputs {
		env[o] = c.Outputs[o]
	}
	return env
}

func envList(env map[string]string) []string {
	list := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		list = append(list, k+"="+env[k])
	}
	return list
}

// services are the running services of one test, in start order.
type services struct {
	c       *Ctx
	tmp     string
	running []*os.Process
}

// start starts service i, waits until its probe passes and returns what it exports to the
// test. A service that exits or does not become ready in time fails the job with its name,
// the timeout and the last 20 lines of its log.
func (sv *services) start(i int, s bspec.Service) (map[string]string, error) {
	addr := fmt.Sprintf("127.0.0.%d", i+2)
	expand := func(v string) string { return strings.ReplaceAll(v, "$ADDR", addr) }
	home := filepath.Join(sv.tmp, "services", strconv.Itoa(i)+"-"+s.Name)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	prog, err := lookPath(s.Argv[0], s.Path)
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", s.Name, err)
	}
	env := sv.c.baseEnv(s.Path, home)
	env["HOME"] = home
	env["ADDR"] = addr
	logPath := home + ".log"
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	argv := make([]string, len(s.Argv))
	for j, a := range s.Argv {
		argv[j] = expand(a)
	}
	p, err := os.StartProcess(prog, argv, &os.ProcAttr{Dir: home, Env: envList(env),
		Files: []*os.File{nil, logFile, logFile}, Sys: &syscall.SysProcAttr{Setpgid: true}})
	logFile.Close()
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", s.Name, err)
	}
	sv.running = append(sv.running, p)

	timeout := time.Duration(s.TimeoutMs) * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		if st, exited := reap(p); exited {
			sv.running = slices.DeleteFunc(sv.running, func(q *os.Process) bool { return q == p })
			return nil, sv.failure(s.Name, fmt.Sprintf("exited before it became ready (%s)", st), logPath)
		}
		if probe(s.Ready, addr, env, deadline) {
			break
		}
		if !time.Now().Before(deadline) {
			return nil, sv.failure(s.Name, "did not become ready within "+timeout.String(), logPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := map[string]string{addrVar(s.Name): addr}
	for k, v := range s.Env {
		out[k] = expand(v)
	}
	return out, nil
}

// failure writes the last lines of the service's log to the build log, then a diagnostic
// with the whole message, and returns the error (its text ends the log, R-NIX-017).
func (sv *services) failure(name, what, logPath string) error {
	msg := "service " + name + " " + what
	tail := lastLines(logPath, serviceTail)
	for _, l := range tail {
		fmt.Fprintln(sv.c.stderr, l)
	}
	full := sv.c.Job.Label + ": " + msg
	if len(tail) > 0 {
		full += "\n" + strings.Join(tail, "\n")
	}
	sv.c.Diag("error", "", 0, 0, full)
	return errors.New(msg)
}

// probe runs one readiness check, bounded by the deadline.
func probe(p bspec.Probe, addr string, env map[string]string, deadline time.Time) bool {
	left := time.Until(deadline)
	if left <= 0 {
		return false
	}
	if p.Port != 0 {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(p.Port)), min(left, 250*time.Millisecond))
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
	prog, err := lookPath(p.Argv[0], strings.Split(env["PATH"], ":"))
	if err != nil {
		return false
	}
	cmd := &exec.Cmd{Path: prog, Args: p.Argv, Env: envList(env), Dir: env["HOME"],
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true}}
	if err := cmd.Start(); err != nil {
		return false
	}
	// The probe gets the time that is left; a hanging probe is killed with its group.
	timer := time.AfterFunc(left, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	err = cmd.Wait()
	timer.Stop()
	return err == nil
}

// reap reports whether p has exited, without blocking.
func reap(p *os.Process) (string, bool) {
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(p.Pid, &ws, syscall.WNOHANG, nil)
	if err != nil || pid != p.Pid {
		return "", err != nil
	}
	if ws.Signaled() {
		return "signal: " + ws.Signal().String(), true
	}
	return "exit status " + strconv.Itoa(ws.ExitStatus()), true
}

// stop ends the services in reverse order: SIGTERM to each process group, up to 5 s, then
// SIGKILL. The end of the build's pid namespace removes anything left (R-FW-025).
func (sv *services) stop() {
	if len(sv.running) == 0 {
		return
	}
	procs := slices.Clone(sv.running)
	slices.Reverse(procs)
	sv.running = nil
	for _, p := range procs {
		_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(stopGrace)
	for _, p := range procs {
		for {
			if _, exited := reap(p); exited || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		_, _ = reap(p)
	}
}

// lastLines returns the last n lines of the file (reading at most its last tailBytes).
func lastLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
		if _, err := f.Seek(fi.Size()-tailBytes, io.SeekStart); err != nil {
			return nil
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines[max(0, len(lines)-n):]
}

// goResult matches the result line of a Go test or subtest in -test.v output.
var goResult = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+) \(([0-9.]+)s\)`)

// goCases parses `go test -v` style output into cases. A failing case carries the output that
// followed its "=== RUN" line (capped at 64 KiB).
func goCases(logPath string) ([]bspec.TestCase, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cases := []bspec.TestCase{}
	outputs := map[string]*strings.Builder{}
	current := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(trimmed, "=== RUN"); ok {
			current = strings.TrimSpace(name)
			outputs[current] = &strings.Builder{}
			continue
		}
		if name, ok := strings.CutPrefix(trimmed, "=== CONT"); ok {
			current = strings.TrimSpace(name)
			continue
		}
		if m := goResult.FindStringSubmatch(line); m != nil {
			d, _ := strconv.ParseFloat(m[3], 64)
			tc := bspec.TestCase{Name: m[2], Result: strings.ToLower(m[1]), Duration: d}
			if tc.Result == "fail" && outputs[m[2]] != nil {
				tc.Output = outputs[m[2]].String()
			}
			cases = append(cases, tc)
			continue
		}
		if b := outputs[current]; b != nil && b.Len() < maxCaseOutput {
			b.WriteString(line[:min(len(line), maxCaseOutput-b.Len())])
			if b.Len() < maxCaseOutput {
				b.WriteByte('\n')
			}
		}
	}
	// A line longer than the scanner buffer ends parsing; the cases so far are kept.
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return nil, err
	}
	return cases, nil
}
