package evalbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// The sandbox runs as two re-executions of the bob binary (/proc/self/exe), dispatched by
// init before main: stage1 builds the filesystem view and stays as pid 1, stage2 drops into
// the nested user namespace, locks itself down and runs the steps. Their configuration comes
// as JSON in argv, never through the environment, so nothing of the host's environment can
// reach the sandbox.

const (
	stage1Arg = "bob-evalbox:stage1"
	stage2Arg = "bob-evalbox:stage2"

	exitStage1 = 120 // stage1 could not set up the sandbox
	exitStage2 = 121 // stage2 could not set up the sandbox

	evalUID = 1000 // the evaluator's uid inside the nested user namespace

	graphFD = 3 // the evaluator writes graph IR here

	cacheTarget = "/.gocache-upper" // mount point of Spec.CacheDir
)

// stageConfig is what the parent tells stage1 and stage2.
type stageConfig struct {
	Binds     []stageBind    `json:"binds"`     // read-only binds, sources are inherited fds
	Seed      *stageBind     `json:"seed"`      // GOCACHE lower layer, or the cache itself when Seed.RW
	Cache     *stageBind     `json:"cache"`     // persistent overlay upper/work dirs of the GOCACHE (Spec.CacheDir)
	WorkBytes int64          `json:"workBytes"` // size of the /work tmpfs
	Dir       string         `json:"dir"`       // working directory of the steps
	Steps     []Step         `json:"steps"`
	NProc     int            `json:"nproc"` // RLIMIT_NPROC of the evaluator (0 = cgroup pids.max applies)
	Strict    bool           `json:"strict"`
	Rules     []landlockRule `json:"rules"`
}

// stageBind is one bind of stage1: a detached mount tree inherited as FD (bob runs as root),
// or a host Path (FD -1).
type stageBind struct {
	FD     int    `json:"fd"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target"`
	RW     bool   `json:"rw,omitempty"`
}

func (r landlockRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path   string `json:"path"`
		Access uint64 `json:"access"`
	}{r.path, r.access})
}

func (r *landlockRule) UnmarshalJSON(b []byte) error {
	var v struct {
		Path   string `json:"path"`
		Access uint64 `json:"access"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	r.path, r.access = v.Path, v.Access
	return nil
}

func init() {
	if len(os.Args) < 3 || (os.Args[1] != stage1Arg && os.Args[1] != stage2Arg) {
		return
	}
	// This process is a sandbox stage, not bob: it must never return into bob's main.
	// syscall.Exit (not os.Exit) because the stage is a separate program with nothing to clean
	// up; deferred cleanup of the bob process is unaffected.
	var cfg stageConfig
	err := json.Unmarshal([]byte(os.Args[2]), &cfg)
	code := exitStage1
	if os.Args[1] == stage2Arg {
		code = exitStage2
	}
	if err == nil {
		if os.Args[1] == stage1Arg {
			code, err = stage1(&cfg)
		} else {
			code, err = stage2(&cfg)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bob evaluation sandbox (%s): %v\n", os.Args[1][len("bob-evalbox:"):], err)
	}
	syscall.Exit(code)
}

func wrap(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// Mount flags (syscall has them; the ST_* statfs flags are spelled out).
const (
	stNosuid     = 0x2
	stNodev      = 0x4
	stNoexec     = 0x8
	stNoatime    = 0x400
	stNodiratime = 0x800
	stRelatime   = 0x1000
	msRelatime   = 1 << 21
)

// bindRO bind-mounts src at dst and makes it read-only. Read-only is load-bearing: in a
// rootless store the evaluator's kernel uid owns the toolchain files.
func bindRO(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := mountpoint(dst, fi.IsDir()); err != nil {
		return err
	}
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s: %w", dst, err)
	}
	return remountRO(dst)
}

func remountRO(dst string) error {
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
	// Flags of the original mount are locked in a user namespace and must be kept.
	var st syscall.Statfs_t
	if syscall.Statfs(dst, &st) == nil {
		for _, m := range []struct{ st, ms int64 }{
			{stNoexec, syscall.MS_NOEXEC}, {stNoatime, syscall.MS_NOATIME},
			{stNodiratime, syscall.MS_NODIRATIME}, {stRelatime, msRelatime},
		} {
			if st.Flags&m.st != 0 {
				flags |= uintptr(m.ms)
			}
		}
	}
	return wrap(syscall.Mount("", dst, "", flags, ""), "remount read-only "+dst)
}

func mountpoint(dst string, dir bool) error {
	if dir {
		return os.MkdirAll(dst, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	return f.Close()
}

// stage1 runs as root of the new user namespace (mapped to an unprivileged host uid) in fresh
// mount, net, pid, ipc, uts and cgroup namespaces.
func stage1(cfg *stageConfig) (int, error) {
	_, _, _ = syscall.RawSyscall(sysPrctl, syscall.PR_SET_DUMPABLE, 0, 0)
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return exitStage1, wrap(err, "make / private")
	}
	r := "/mnt"
	if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
		r = "/tmp"
	}
	if err := syscall.Mount("tmpfs", r, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=16m,mode=755"); err != nil {
		return exitStage1, wrap(err, "root tmpfs")
	}
	for _, b := range cfg.Binds {
		if err := b.attach(r + b.Target); err != nil {
			return exitStage1, err
		}
	}
	if err := os.MkdirAll(r+"/gomod", 0o755); err != nil {
		return exitStage1, err
	}
	work := r + "/work"
	if err := os.MkdirAll(work, 0o755); err != nil {
		return exitStage1, err
	}
	if err := syscall.Mount("tmpfs", work, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV,
		"size="+strconv.FormatInt(cfg.WorkBytes, 10)+",mode=755"); err != nil {
		return exitStage1, wrap(err, "work tmpfs")
	}
	for _, d := range []string{"home", "tmp", "gocache", ".ov/up", ".ov/wk"} {
		if err := os.MkdirAll(work+"/"+d, 0o755); err != nil {
			return exitStage1, err
		}
	}
	if err := gocache(cfg, r); err != nil {
		return exitStage1, err
	}
	if err := devAndEtc(r); err != nil {
		return exitStage1, err
	}
	if err := pivot(r); err != nil {
		return exitStage1, err
	}
	_ = syscall.Sethostname([]byte("bob-eval"))
	// Only one nested user namespace may ever exist below this one: stage2's. Kernel-enforced,
	// independent of seccomp.
	if err := os.WriteFile("/proc/sys/user/max_user_namespaces", []byte("1"), 0); err != nil {
		return exitStage1, wrap(err, "cap nested user namespaces")
	}
	return runStage2(cfg)
}

// gocache mounts /work/gocache: the trusted seed as overlay lower layer under a per-evaluation
// upper layer on /work, so one evaluation can never poison the cache of another.
func gocache(cfg *stageConfig, r string) error {
	if cfg.Seed == nil {
		return nil
	}
	if cfg.Seed.RW {
		return cfg.Seed.attach(r + "/work/gocache")
	}
	lower := r + "/.seed"
	if err := cfg.Seed.attach(lower); err != nil {
		return err
	}
	ov := r + "/work/.ov"
	if cfg.Cache != nil {
		// Outside /work and without a Landlock rule: only the overlay reaches it.
		ov = r + cacheTarget
		if err := cfg.Cache.attach(ov); err != nil {
			return err
		}
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, ov+"/up", ov+"/wk")
	return wrap(syscall.Mount("overlay", r+"/work/gocache", "overlay", syscall.MS_NOSUID|syscall.MS_NODEV, opts), "gocache overlay")
}

func devAndEtc(r string) error {
	if err := os.MkdirAll(r+"/dev", 0o755); err != nil {
		return err
	}
	if err := syscall.Mount("tmpfs", r+"/dev", "tmpfs", syscall.MS_NOSUID|syscall.MS_NOEXEC, "size=64k,mode=755"); err != nil {
		return wrap(err, "dev tmpfs")
	}
	for _, d := range []string{"null", "zero", "full", "random", "urandom"} {
		if err := mountpoint(r+"/dev/"+d, false); err != nil {
			return err
		}
		if err := syscall.Mount("/dev/"+d, r+"/dev/"+d, "", syscall.MS_BIND, ""); err != nil {
			return wrap(err, "bind /dev/"+d)
		}
	}
	for n, t := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err := os.Symlink(t, r+"/dev/"+n); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(r+"/proc", 0o555); err != nil {
		return err
	}
	if err := syscall.Mount("proc", r+"/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "hidepid=2"); err != nil {
		return wrap(err, "mount proc")
	}
	if err := os.MkdirAll(r+"/etc", 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"passwd": fmt.Sprintf("bob:x:%d:%d:bob:/work/home:/noshell\nnobody:x:65534:65534::/:/noshell\n", evalUID, evalUID),
		"group":  fmt.Sprintf("bob:x:%d:\nnobody:x:65534:\n", evalUID),
		"hosts":  "127.0.0.1 localhost\n::1 localhost\n",
	}
	for n, c := range files {
		if err := os.WriteFile(r+"/etc/"+n, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return os.Symlink("/work/tmp", r+"/tmp")
}

func pivot(r string) error {
	if err := os.MkdirAll(r+"/.old", 0o700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(r, r+"/.old"); err != nil {
		return wrap(err, "pivot_root")
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	if err := syscall.Unmount("/.old", syscall.MNT_DETACH); err != nil {
		return wrap(err, "detach old root")
	}
	_ = os.Remove("/.old")
	return wrap(syscall.Mount("", "/", "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, ""), "remount / read-only")
}

// runStage2 starts stage2 in a nested user namespace (uid 1000 -> 0, no capabilities after
// exec) and reaps every process of the pid namespace until stage2 exits; the kernel kills
// the rest when pid 1 returns.
func runStage2(cfg *stageConfig) (int, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return exitStage1, err
	}
	graph := os.NewFile(graphFD, "graph")
	cmd := exec.Command("/proc/self/exe", stage2Arg, string(b))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{graph}
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: evalUID, HostID: 0, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: evalUID, HostID: 0, Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		return exitStage1, wrap(err, "start stage2")
	}
	_ = graph.Close()
	target := cmd.Process.Pid
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return exitStage1, wrap(err, "wait")
		}
		if pid == target {
			if ws.Signaled() {
				return 128 + int(ws.Signal()), nil
			}
			return ws.ExitStatus(), nil
		}
	}
}

// stage2 runs as uid 1000 without capabilities. It locks itself down (no_new_privs, Landlock,
// seccomp), runs every step but the last as a child and replaces itself with the last one,
// which inherits the graph descriptor.
func stage2(cfg *stageConfig) (int, error) {
	runtime.LockOSThread() // no_new_privs and Landlock apply to this thread and what it starts
	if cfg.NProc > 0 {
		lim := syscall.Rlimit{Cur: uint64(cfg.NProc), Max: uint64(cfg.NProc)}
		if err := syscall.Setrlimit(rlimitNproc, &lim); err != nil {
			return exitStage2, wrap(err, "process limit")
		}
	}
	if _, _, e := syscall.RawSyscall(sysPrctl, prSetNoNewPrivs, 1, 0); e != 0 {
		return exitStage2, wrap(e, "no_new_privs")
	}
	if err := lockDown(cfg); err != nil {
		return exitStage2, err
	}
	if err := syscall.Chdir(cfg.Dir); err != nil {
		return exitStage2, wrap(err, "chdir")
	}
	if len(cfg.Steps) == 0 {
		return exitStage2, errors.New("no steps")
	}
	// The graph descriptor belongs to the last step only.
	if err := setCloexec(graphFD, true); err != nil {
		return exitStage2, err
	}
	for _, s := range cfg.Steps[:len(cfg.Steps)-1] {
		cmd := exec.Command(s.Args[0], s.Args[1:]...)
		cmd.Env = s.Env
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return exitCode(err), nil
			}
			return 1, err // e.g. the step's program is missing: an evaluation failure
		}
	}
	if err := setCloexec(graphFD, false); err != nil {
		return exitStage2, err
	}
	if err := syscall.Dup3(2, 1, 0); err != nil { // stdout of the evaluator is diagnostics too
		return exitStage2, err
	}
	last := cfg.Steps[len(cfg.Steps)-1]
	return 1, syscall.Exec(last.Args[0], last.Args, last.Env)
}

const (
	rlimitNproc     = 6
	prSetNoNewPrivs = 38
)

func setCloexec(fd int, on bool) error {
	v := uintptr(0)
	if on {
		v = syscall.FD_CLOEXEC
	}
	_, _, e := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, v)
	if e != 0 {
		return wrap(e, "fcntl graph fd")
	}
	return nil
}
