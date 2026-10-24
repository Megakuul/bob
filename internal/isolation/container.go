package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ChildArg is argv[1] of the re-executed bob-server that sets up a job container
// (`/proc/self/exe <ChildArg> <json>`). main dispatches it to Child before anything else.
const ChildArg = "bob-isolation:container"

// Bind is a host path made visible inside the container, read-only unless Writable.
type Bind struct {
	Host     string `json:"host"`
	Path     string `json:"path"` // absolute path inside the container
	Writable bool   `json:"rw,omitempty"`
}

// Limits are a job's cgroup limits (the slot config).
type Limits struct {
	MemoryBytes int64 // memory.max; 0 = max
	Pids        int   // pids.max; 0 = max
	CPUs        int   // cpu.max in whole CPUs; 0 = max
}

// Spec is one job container.
type Spec struct {
	JobID string
	// RootDir is an empty host directory the container's tmpfs root is mounted on (it stays
	// an empty directory on the host; Run removes it afterwards).
	RootDir string
	// Store is the worker's persistent root-owned store directory, bound read-write at /nix.
	Store string
	Binds []Bind
	// Args[0] is an absolute path inside the container; Env is the complete environment (the
	// worker's own environment never reaches the container).
	Args     []string
	Env      []string
	Dir      string // working directory inside; default /
	Hostname string // default "bob-job"
	Limits   Limits
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
}

// childConfig is what Run tells Child (argv, never the environment).
type childConfig struct {
	Root     string   `json:"root"`
	Store    string   `json:"store"`
	Binds    []Bind   `json:"binds"`
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	Dir      string   `json:"dir"`
	Hostname string   `json:"hostname"`
}

var jobIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Exit statuses of a container that never reached the job's program.
const (
	ExitSetup = 125 // Child could not set up the container
)

// Run runs s in a fresh job container and returns the exit status of its program (a signal
// gives 128 + signal). After the program ends, every process left in the job's cgroup is
// killed and the cgroup removed, so nothing of the job outlives Run.
func Run(ctx context.Context, s Spec) (exit int, err error) {
	defer func() {
		if r := recover(); r != nil {
			exit, err = -1, fmt.Errorf("isolation: job container crashed: %v", r)
		}
	}()
	if !jobIDRE.MatchString(s.JobID) {
		return -1, fmt.Errorf("isolation: job id %q is not a valid cgroup name", s.JobID)
	}
	if len(s.Args) == 0 || !filepath.IsAbs(s.Args[0]) {
		return -1, errors.New("isolation: the container program must be an absolute path")
	}
	if s.RootDir == "" || s.Store == "" {
		return -1, errors.New("isolation: the container needs a root directory and the store")
	}
	if err := os.MkdirAll(s.RootDir, 0o700); err != nil {
		return -1, fmt.Errorf("isolation: %w", err)
	}
	defer os.Remove(s.RootDir)
	cg, err := newJobCgroup(filepath.Join(CgroupRoot, Slice), s.JobID, s.Limits)
	if err != nil {
		return -1, err
	}
	defer cg.remove()

	cfg := childConfig{Root: s.RootDir, Store: s.Store, Binds: s.Binds, Args: s.Args, Env: s.Env, Dir: s.Dir, Hostname: s.Hostname}
	if cfg.Env == nil {
		cfg.Env = []string{}
	}
	cj, err := json.Marshal(cfg)
	if err != nil {
		return -1, err
	}
	cmd := exec.Command("/proc/self/exe", ChildArg, string(cj))
	cmd.Env = []string{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.Stdin, s.Stdout, s.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWCGROUP,
		Pdeathsig:  syscall.SIGKILL,
		Setsid:     true,
		// Started straight into the job's leaf (clone3 CLONE_INTO_CGROUP): no instant of the
		// job runs outside its limits.
		UseCgroupFD: true,
		CgroupFD:    cg.fd(),
	}
	// Pdeathsig fires when the starting thread exits; keep it until the start is done.
	runtime.LockOSThread()
	err = cmd.Start()
	runtime.UnlockOSThread()
	if err != nil {
		return -1, fmt.Errorf("isolation: start job container: %w", err)
	}
	stop := context.AfterFunc(ctx, cg.kill)
	werr := cmd.Wait()
	stop()
	cg.kill() // whatever the program left behind (R-FLEET-008: 0 processes after the job)
	if werr != nil {
		var ee *exec.ExitError
		if !errors.As(werr, &ee) {
			return -1, fmt.Errorf("isolation: job container: %w", werr)
		}
	}
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	switch {
	case ws.Signaled():
		exit = 128 + int(ws.Signal())
	default:
		exit = ws.ExitStatus()
	}
	if ctx.Err() != nil {
		return exit, ctx.Err()
	}
	return exit, nil
}

// jobCgroup is the leaf bob-worker.slice/job-<id> of one container.
type jobCgroup struct {
	dir string
	f   *os.File
}

func newJobCgroup(slice, jobID string, l Limits) (*jobCgroup, error) {
	dir := filepath.Join(slice, "job-"+jobID)
	if _, err := os.Stat(dir); err == nil {
		// A leftover of a worker that died mid-job: nothing of it may keep running.
		(&jobCgroup{dir: dir}).remove()
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("isolation: create cgroup %s: %w", dir, err)
	}
	c := &jobCgroup{dir: dir}
	limit := func(v int64) string {
		if v <= 0 {
			return "max"
		}
		return strconv.FormatInt(v, 10)
	}
	cpu := "max 100000"
	if l.CPUs > 0 {
		cpu = fmt.Sprintf("%d 100000", l.CPUs*100000)
	}
	for _, s := range []struct{ file, value string }{
		{"memory.max", limit(l.MemoryBytes)},
		{"memory.oom.group", "1"},
		{"pids.max", limit(int64(l.Pids))},
		{"cpu.max", cpu},
	} {
		if err := os.WriteFile(filepath.Join(dir, s.file), []byte(s.value), 0); err != nil {
			c.remove()
			return nil, fmt.Errorf("isolation: cgroup %s: set %s: %w", dir, s.file, err)
		}
	}
	f, err := os.Open(dir)
	if err != nil {
		c.remove()
		return nil, fmt.Errorf("isolation: %w", err)
	}
	c.f = f
	return c, nil
}

func (c *jobCgroup) fd() int { return int(c.f.Fd()) }

func (c *jobCgroup) kill() { _ = os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0) }

// remove kills everything in the leaf (and in cgroups Nix created below it), waits until it
// is empty and removes it, deepest first. cgroup.kill is asynchronous, so it retries.
func (c *jobCgroup) remove() {
	if c.f != nil {
		_ = c.f.Close()
		c.f = nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		c.kill()
		if removeTree(c.dir) || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// removeTree removes dir and its child cgroups; it reports whether dir is gone.
func removeTree(dir string) bool {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	for _, e := range ents {
		if e.IsDir() {
			removeTree(filepath.Join(dir, e.Name()))
		}
	}
	err = os.Remove(dir)
	return err == nil || errors.Is(err, os.ErrNotExist)
}

// Procs returns the pids in the job leaf of jobID (tests and diagnostics).
func Procs(jobID string) []string {
	b, _ := os.ReadFile(filepath.Join(CgroupRoot, Slice, "job-"+jobID, "cgroup.procs"))
	return strings.Fields(string(b))
}
