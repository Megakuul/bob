// Package evalbox is the evaluator sandbox of SPEC R-FW-045 (S13 evalbox, ADR 0015): it runs
// the compile and run steps of a bob.go evaluation, locally and on workers, inside
//
//  1. a delegated cgroup v2 leaf when one is available (memory.max, memory.swap.max=0,
//     pids.max, cpu.max; cgroup.kill), otherwise RLIMIT_NPROC and a memory watchdog;
//  2. user, mount, net, pid, ipc, uts and cgroup namespaces with a tmpfs root that holds only
//     read-only binds (toolchain, source tree, framework, module cache), a size-capped /work,
//     a few /dev nodes, a fresh /proc (hidepid=2) and a generated /etc (stage1, pid 1);
//  3. a nested user namespace (uid 1000, no capabilities), no_new_privs, Landlock and seccomp
//     (stage2), which then runs the steps.
//
// The last step writes graph IR to fd 3; the parent caps it, and caps stderr. The wall clock,
// the output caps and the memory limit always apply; the cgroup only adds precision.
package evalbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Limits bound one sandbox run. Zero fields take the local defaults of SPEC R-FW-045.
type Limits struct {
	WallClock   time.Duration // default 120 s
	IROutput    int64         // bytes on fd 3, default 64 MiB
	StderrKept  int64         // default 1 MiB (the tail is kept)
	Pids        int           // processes + threads, default 512
	MemoryBytes int64         // incl. the /work tmpfs, default 2 GiB
	WorkBytes   int64         // /work tmpfs size, default 1 GiB
	CPUs        int           // cpu.max (cgroup only), default 2
}

// WithDefaults fills the zero fields.
func (l Limits) WithDefaults() Limits {
	if l.WallClock <= 0 {
		l.WallClock = 120 * time.Second
	}
	if l.IROutput <= 0 {
		l.IROutput = 64 << 20
	}
	if l.StderrKept <= 0 {
		l.StderrKept = 1 << 20
	}
	if l.Pids <= 0 {
		l.Pids = 512
	}
	if l.MemoryBytes <= 0 {
		l.MemoryBytes = 2 << 30
	}
	if l.WorkBytes <= 0 {
		l.WorkBytes = 1 << 30
	}
	if l.CPUs <= 0 {
		l.CPUs = 2
	}
	return l
}

// Bind is a host directory made visible inside the sandbox, read-only unless Writable.
type Bind struct {
	Host    string // host path
	Sandbox string // absolute path inside the sandbox, never below /work
	Exec    bool   // Landlock allows executing files below it (toolchains)
	// Writable makes the bind read-write (an output directory of a trusted step, such as the
	// evaluator binary go build writes). Never give it to a step that runs untrusted code.
	Writable bool
}

// Step is one command inside the sandbox. Args[0] is an absolute path inside the sandbox; Env
// is the complete environment.
type Step struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

// Spec is one sandbox run.
type Spec struct {
	Binds  []Bind // read-only; /gomod is an empty directory unless bound
	Seed   string // host directory of the trusted GOCACHE seed, mounted at /work/gocache
	SeedRW bool   // trusted seed build only: Seed is mounted read-write instead of as overlay lower layer
	// CacheDir, with a read-only Seed, is a host directory holding the overlay upper layer of
	// /work/gocache (CacheDir/up, CacheDir/wk), so compile results outlive the run. It belongs
	// to one repository and is given only to the go build step, which runs no user code.
	CacheDir string
	Dir      string // working directory of the steps
	Steps    []Step // run in order; the last one writes graph IR to fd 3
	Limits   Limits
	// Strict is the worker mode (S13-3): refuse to run without a delegated cgroup and Landlock
	// ABI >= 4 instead of falling back.
	Strict bool
}

// Outcome is a finished run.
type Outcome struct {
	Graph  []byte // what the last step wrote to fd 3
	Stderr []byte // the tail of stdout+stderr, at most Limits.StderrKept
	Cgroup bool   // limits were enforced by a delegated cgroup
}

// Error is an evaluation the sandbox stopped or that failed; Msg and Hint are the S13 §5.2
// texts, for the caller to render as a bob diagnostic.
type Error struct {
	Msg    string
	Hint   string
	Detail string // evaluator output shown below the message ("" when not relevant)
	Exit   int
}

func (e *Error) Error() string {
	s := e.Msg
	if e.Hint != "" {
		s += "\n  hint: " + e.Hint
	}
	if e.Detail != "" {
		s += "\n" + e.Detail
	}
	return s
}

// ErrNoIsolation is returned in strict mode when the host cannot isolate evaluations.
var ErrNoIsolation = errors.New("worker cannot isolate evaluations")

// Run runs s in a fresh sandbox.
func Run(ctx context.Context, s Spec) (out *Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("internal error: the evaluation sandbox crashed: %v", r)
		}
	}()
	if len(s.Steps) == 0 {
		return nil, errors.New("evalbox: no steps")
	}
	l := s.Limits.WithDefaults()
	cg, cgErr := newCgroup(l)
	if s.Strict {
		if cgErr != nil {
			return nil, noIsolation(cgErr.Error())
		}
		if abi := LandlockABI(); abi < 4 {
			cg.remove()
			return nil, noIsolation(fmt.Sprintf("Landlock ABI %d, need 4 or newer", abi))
		}
	}
	if cg != nil {
		defer cg.remove()
	}
	r, err := start(s, l, cg)
	if err != nil {
		return nil, err
	}
	return r.wait(ctx)
}

func noIsolation(reason string) error {
	return fmt.Errorf("%w: %s\n  hint: run the worker under systemd with Delegate=yes (e.g. systemd-run --user -p Delegate=yes ..., or a unit with Delegate=yes)", ErrNoIsolation, reason)
}

// run is one started sandbox.
type run struct {
	l      Limits
	cg     *cgroup
	cmd    *exec.Cmd
	graph  *os.File // read ends
	stderr *os.File

	killOnce   sync.Once
	timedOut   atomic.Bool
	tooBig     atomic.Bool
	memKilled  atomic.Bool
	memPeak    atomic.Uint64
	tasksPeak  atomic.Int64
	graphBytes []byte
	tail       *tailBuffer
}

func start(s Spec, l Limits, cg *cgroup) (*run, error) {
	uid, gid, err := rootIDs()
	if err != nil {
		return nil, err
	}
	cfg := stageConfig{WorkBytes: l.WorkBytes, Dir: s.Dir, Steps: s.Steps, Strict: s.Strict}
	if cg == nil {
		cfg.NProc = l.Pids
	}
	graphR, graphW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	files := []*os.File{graphW} // ExtraFiles: fd 3 graph, then detached bind sources
	fail := func(err error) (*run, error) {
		for _, f := range files {
			_ = f.Close()
		}
		_ = graphR.Close()
		return nil, err
	}
	rules := []landlockRule{
		{"/etc", llRead}, {"/proc", llRead}, {"/gomod", llRead},
		{"/work", llExecute | llRead | llWrite | llRefer | llTruncate},
		{"/dev/null", llReadFile | llWriteFile}, {"/dev/full", llReadFile | llWriteFile},
		{"/dev/zero", llReadFile}, {"/dev/random", llReadFile}, {"/dev/urandom", llReadFile},
	}
	for _, b := range s.Binds {
		if b.Sandbox == "/work" || strings.HasPrefix(b.Sandbox, "/work/") {
			return fail(fmt.Errorf("evaluation sandbox: bind target %s is below /work", b.Sandbox))
		}
		if b.Writable && asNobody() {
			if err := os.Chown(b.Host, uid, gid); err != nil {
				return fail(err)
			}
		}
		sb, err := bindSource(b.Host, b.Sandbox, b.Writable, &files)
		if err != nil {
			return fail(err)
		}
		cfg.Binds = append(cfg.Binds, sb)
		acc := uint64(llRead)
		if b.Exec {
			acc |= llExecute
		}
		if b.Writable {
			acc |= llWrite | llRefer | llTruncate
		}
		rules = append(rules, landlockRule{b.Sandbox, acc})
	}
	if s.Seed != "" {
		if s.SeedRW && asNobody() {
			if err := os.Chown(s.Seed, uid, gid); err != nil {
				return fail(err)
			}
		}
		sb, err := bindSource(s.Seed, "/work/gocache", s.SeedRW, &files)
		if err != nil {
			return fail(err)
		}
		cfg.Seed = &sb
		if s.CacheDir != "" && !s.SeedRW {
			for _, d := range []string{"up", "wk"} {
				if err := os.MkdirAll(s.CacheDir+"/"+d, 0o755); err != nil {
					return fail(err)
				}
				if asNobody() {
					if err := os.Chown(s.CacheDir+"/"+d, uid, gid); err != nil {
						return fail(err)
					}
				}
			}
			cb, err := bindSource(s.CacheDir, cacheTarget, true, &files)
			if err != nil {
				return fail(err)
			}
			cfg.Cache = &cb
		}
	}
	cfg.Rules = rules
	cj, err := json.Marshal(cfg)
	if err != nil {
		return fail(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	cmd := exec.Command("/proc/self/exe", stage1Arg, string(cj))
	cmd.Stdout, cmd.Stderr = errW, errW
	cmd.ExtraFiles = files
	cmd.Env = []string{} // nothing of the host environment
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID |
			syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWCGROUP,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
		Setsid:      true,
	}
	if os.Getuid() == 0 {
		// The mapping alone leaves the child at kernel uid 0, which is unmapped inside; become
		// the namespace's root, i.e. the unprivileged host uid.
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: true}
	}
	if cg != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = cg.fd()
	}
	// Pdeathsig fires when the starting thread exits; keep it alive until the start is done
	// (Go only ends threads of goroutines that exit while locked).
	runtime.LockOSThread()
	err = cmd.Start()
	runtime.UnlockOSThread()
	for _, f := range files {
		_ = f.Close()
	}
	_ = errW.Close()
	if err != nil {
		_ = graphR.Close()
		_ = errR.Close()
		return nil, fmt.Errorf("evaluation sandbox could not be started: %w%s", err, userNSHint(err))
	}
	return &run{l: l, cg: cg, cmd: cmd, graph: graphR, stderr: errR, tail: &tailBuffer{max: int(l.StderrKept)}}, nil
}

func userNSHint(err error) string {
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EACCES) {
		return " (unprivileged user namespaces may be blocked on this system)"
	}
	return ""
}

func (r *run) kill() {
	r.killOnce.Do(func() {
		if r.cg != nil {
			r.cg.kill()
		}
		// stage1 is pid 1 of the sandbox: when it dies the kernel kills every process in it.
		_ = r.cmd.Process.Kill()
	})
}

// guard runs f in a goroutine with a recover boundary (AGENTS.md §5.3).
func guard(wg *sync.WaitGroup, f func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		f()
	}()
}

func (r *run) wait(ctx context.Context) (*Outcome, error) {
	var readers, watchers sync.WaitGroup
	guard(&readers, func() {
		b, _ := io.ReadAll(io.LimitReader(r.graph, r.l.IROutput+1))
		if int64(len(b)) > r.l.IROutput {
			r.tooBig.Store(true)
			r.kill() // a writer would otherwise block on the full pipe until the timeout
			b = nil
		}
		r.graphBytes = b
		_, _ = io.Copy(io.Discard, r.graph)
	})
	guard(&readers, func() { _, _ = io.Copy(r.tail, r.stderr) })
	stop := make(chan struct{})
	guard(&watchers, func() { r.watch(ctx, stop) })
	waitErr := r.cmd.Wait()
	close(stop)
	watchers.Wait()
	r.kill() // backstop: nothing of the sandbox outlives stage1
	readers.Wait()
	_ = r.graph.Close()
	_ = r.stderr.Close()

	var st cgroupStats
	if r.cg != nil {
		st = r.cg.stats()
	}
	out := &Outcome{Graph: r.graphBytes, Stderr: r.tail.bytes(), Cgroup: r.cg != nil}
	if ctx.Err() != nil && !r.timedOut.Load() {
		return nil, ctx.Err()
	}
	return out, r.classify(exitCode(waitErr), st, out.Stderr)
}

// watch enforces the wall clock and, without a cgroup, the memory limit; it also records the
// peak task count.
func (r *run) watch(ctx context.Context, stop <-chan struct{}) {
	deadline := time.NewTimer(r.l.WallClock)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			r.kill()
			return
		case <-deadline.C:
			r.timedOut.Store(true)
			r.kill()
			return
		case <-tick.C:
			if r.cg != nil {
				continue
			}
			u := sandboxUsage(r.cmd.Process.Pid)
			if int64(u.tasks) > r.tasksPeak.Load() {
				r.tasksPeak.Store(int64(u.tasks))
			}
			if u.memory > r.memPeak.Load() {
				r.memPeak.Store(u.memory)
			}
			if int64(u.memory) > r.l.MemoryBytes {
				r.memKilled.Store(true)
				r.kill()
			}
		}
	}
}

// eagain matches what a program prints when fork or thread creation fails with EAGAIN, which
// without a cgroup means RLIMIT_NPROC (the pids limit) was hit.
var eagain = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`resource temporarily unavailable|failed to create new OS thread|errno=11\b`)
})

// classify maps the end of a run to the S13 §5.2 errors, in the S13 order: timeout, memory,
// output cap, pids, sandbox setup, other.
func (r *run) classify(code int, st cgroupStats, stderr []byte) error {
	l := r.l
	switch {
	case r.timedOut.Load():
		return &Error{Exit: code, Msg: fmt.Sprintf("evaluating bob.go took longer than %s and was stopped", seconds(l.WallClock)),
			Hint: "bob.go must only describe the build; long work belongs in a build step (bob.Run)"}
	case st.oomKill > 0 || r.memKilled.Load():
		peak := st.memPeak
		if r.cg == nil {
			peak = r.memPeak.Load()
		}
		return &Error{Exit: code, Msg: fmt.Sprintf("evaluating bob.go used more than %s of memory and was stopped (peak %d MiB)", size(l.MemoryBytes), peak>>20),
			Hint: "a build definition should need far less; look for unbounded loops or huge in-memory data"}
	case r.tooBig.Load():
		return &Error{Exit: code, Msg: fmt.Sprintf("evaluating bob.go produced more than %s of graph output and was stopped", mib(l.IROutput))}
	case code != 0 && (st.pidsMax > 0 || r.cg == nil && (r.tasksPeak.Load() >= int64(l.Pids) || eagain().Match(stderr))):
		return &Error{Exit: code, Msg: fmt.Sprintf("evaluating bob.go failed after trying to start more than %d processes or threads (exit %d)", l.Pids, code),
			Hint: "bob.go must not start processes"}
	case code == exitStage1 || code == exitStage2:
		return &Error{Exit: code, Msg: fmt.Sprintf("internal error: the evaluation sandbox could not be set up (exit %d); this is a bob bug", code),
			Detail: lastLines(stderr, 20)}
	case code != 0:
		return &Error{Exit: code, Msg: fmt.Sprintf("evaluating bob.go failed (exit %d)", code), Detail: lastLines(stderr, 40)}
	}
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

// seconds renders a wall clock as "5s" or "120s" (S13 texts), not "2m0s".
func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// size renders a memory limit as in memory.max notation: "512M", "2G".
func size(b int64) string {
	switch {
	case b%(1<<30) == 0:
		return fmt.Sprintf("%dG", b>>30)
	case b%(1<<20) == 0:
		return fmt.Sprintf("%dM", b>>20)
	}
	return fmt.Sprintf("%d bytes", b)
}

func mib(b int64) string {
	if b%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", b>>20)
	}
	return fmt.Sprintf("%d bytes", b)
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > t.max {
		return append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return append([]byte(nil), t.buf...)
}

// nobody is the id the sandbox runs as when bob runs as root.
const nobody = 65534

// asNobody reports whether the sandbox runs as nobody: bob runs as uid 0 and nobody (uid and
// gid 65534) is mapped in bob's user namespace, so files can be handed to it and the sandbox's
// own namespace can map to it. That holds for host root and for root inside bob's namespace
// entered by host root (store.Enter maps 65534 next to 0 then). Root of a namespace without
// nobody (bob's namespace of an unprivileged user: its uid 0 is that user) runs the sandbox as
// itself, see rootIDs for the case where that would be host root.
func asNobody() bool {
	return os.Getuid() == 0 && idMapped("/proc/self/uid_map", nobody) && idMapped("/proc/self/gid_map", nobody)
}

// rootIDs returns the uid and gid the sandbox's own namespace maps to. The sandbox never runs
// as host root (an escape would land as root, and RLIMIT_NPROC does not apply to root): uid 0
// that maps to the parent namespace's uid 0 without nobody being mapped is refused.
func rootIDs() (int, int, error) {
	if asNobody() {
		return nobody, nobody, nil
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 && mapsToParent("/proc/self/uid_map", 0, 0) {
		return 0, 0, fmt.Errorf("evaluation sandbox: bob runs as root in a user namespace without the unprivileged id %d; refusing to evaluate as root", nobody)
	}
	return uid, gid, nil
}

// idMapped reports whether id is inside one of the ranges of the id map file (an absent or
// unreadable map counts as not mapped).
func idMapped(file string, id uint64) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		in, e1 := strconv.ParseUint(f[0], 10, 32)
		n, e2 := strconv.ParseUint(f[2], 10, 33)
		if e1 == nil && e2 == nil && id >= in && id < in+n {
			return true
		}
	}
	return false
}

// mapsToParent reports whether id maps to parent in the parent namespace.
func mapsToParent(file string, id, parent uint64) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		in, e1 := strconv.ParseUint(f[0], 10, 32)
		out, e2 := strconv.ParseUint(f[1], 10, 32)
		n, e3 := strconv.ParseUint(f[2], 10, 33)
		if e1 == nil && e2 == nil && e3 == nil && id >= in && id < in+n {
			return out+(id-in) == parent
		}
	}
	return false
}
