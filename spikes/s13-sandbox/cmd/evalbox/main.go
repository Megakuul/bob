// Command evalbox is a throwaway spike (S13): it runs an untrusted bob.go evaluation on a worker
// without root. Layers, outermost first:
//
//  1. cgroup v2 leaf (delegated subtree): memory.max, memory.swap.max=0, pids.max, cpu.max; wall
//     clock timeout enforced with cgroup.kill.
//  2. stage1: new user (0 -> worker uid), mount, net, pid, ipc, uts and cgroup namespaces. Builds a
//     tmpfs root that contains only: the Go toolchain store paths (ro), the source tree (ro), the
//     module cache (ro), a size-capped tmpfs /work (rw), minimal /dev, /etc, fresh /proc. pivot_root.
//     Caps the nested user namespace count. Stays as pid 1 (reaper).
//  3. stage2: nested user namespace (uid 1000 -> 0), so the evaluator is a non-root uid without any
//     capability over the mount namespace. no_new_privs, Landlock (fs + tcp + scopes), seccomp
//     deny-list. Then exec the evaluator.
//
// The evaluator writes its graph to fd 3; the parent caps the size.
//
//	evalbox [flags] -- cmd args...
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	stage1Marker = "__evalbox_stage1"
	stage2Marker = "__evalbox_stage2"
)

// Config is passed from the parent to stage1/stage2 as JSON (argv), never through the
// environment, so the worker's environment never reaches the sandbox.
type Config struct {
	Src        string   `json:"src"`        // host path of the source tree, mounted ro at /src
	ModCache   string   `json:"modCache"`   // host path of a pre-downloaded GOMODCACHE, ro at /gomod
	SeedCache  string   `json:"seedCache"`  // host path of a trusted warm GOCACHE, overlay lower at /gocache
	StorePaths []string `json:"storePaths"` // closure of the toolchain, each bind-mounted ro
	WorkSize   string   `json:"workSize"`   // tmpfs size for /work, e.g. 512m
	Dir        string   `json:"dir"`        // working dir inside the sandbox
	Env        []string `json:"env"`        // complete environment of the evaluator
	Argv       []string `json:"argv"`       // evaluator command
	NoLandlock bool     `json:"noLandlock"`
	NoSeccomp  bool     `json:"noSeccomp"`
	Timing     bool     `json:"timing"`
	Start      int64    `json:"start"`
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == stage1Marker {
		if err := stage1(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "evalbox(stage1):", err)
			os.Exit(120)
		}
		return
	}
	if len(os.Args) > 2 && os.Args[1] == stage2Marker {
		if err := stage2(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "evalbox(stage2):", err)
			os.Exit(121)
		}
		return
	}
	code, err := parent()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

type limits struct {
	mem      string
	pids     int
	cpu      float64
	timeout  time.Duration
	outMax   int64
	cgroupOn bool
}

func parent() (int, error) {
	var cfg Config
	var lim limits
	var storeList, out string
	flag.StringVar(&cfg.Src, "src", "", "source tree (ro)")
	flag.StringVar(&cfg.ModCache, "modcache", "", "pre-downloaded GOMODCACHE (ro)")
	flag.StringVar(&cfg.SeedCache, "seedcache", "", "trusted warm GOCACHE used as overlay lower layer")
	flag.StringVar(&storeList, "store", "", "comma separated store paths, or @file with one per line")
	flag.StringVar(&cfg.WorkSize, "work", "512m", "tmpfs size for /work")
	flag.StringVar(&cfg.Dir, "dir", "/src", "working directory inside the sandbox")
	flag.BoolVar(&cfg.NoLandlock, "no-landlock", false, "test only: skip Landlock")
	flag.BoolVar(&cfg.NoSeccomp, "no-seccomp", false, "test only: skip seccomp")
	flag.BoolVar(&cfg.Timing, "timing", false, "print setup timing")
	flag.StringVar(&lim.mem, "mem", "1G", "memory.max")
	flag.IntVar(&lim.pids, "pids", 256, "pids.max")
	flag.Float64Var(&lim.cpu, "cpus", 2, "cpu.max in CPUs")
	flag.DurationVar(&lim.timeout, "timeout", 120*time.Second, "wall clock limit")
	flag.Int64Var(&lim.outMax, "outmax", 64<<20, "max bytes on the graph fd")
	flag.BoolVar(&lim.cgroupOn, "cgroup", true, "use a delegated cgroup (refuse if unavailable)")
	flag.StringVar(&out, "out", "", "write the graph to this file")
	var envs multi
	flag.Var(&envs, "env", "KEY=VALUE for the evaluator (repeatable)")
	flag.Parse()
	cfg.Argv = flag.Args()
	if len(cfg.Argv) == 0 {
		return 2, errors.New("usage: evalbox [flags] -- cmd args")
	}
	cfg.Env = envs
	paths, err := parseStore(storeList)
	if err != nil {
		return 2, err
	}
	cfg.StorePaths = paths
	cfg.Start = time.Now().UnixNano()

	var cg *cgroup
	if lim.cgroupOn {
		cg, err = newCgroup(lim)
		if err != nil {
			return 3, fmt.Errorf("✗ worker cannot isolate evaluations: %w\n  hint: run the worker under systemd with Delegate=yes (e.g. `systemd-run --user -p Delegate=yes …` or a unit with Delegate=yes)", err)
		}
		defer cg.remove()
	}

	cj, _ := json.Marshal(cfg)
	pr, pw, err := os.Pipe()
	if err != nil {
		return 1, err
	}
	cmd := exec.Command("/proc/self/exe", stage1Marker, string(cj))
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{pw} // fd 3: graph output
	cmd.Env = []string{}            // nothing from the worker environment
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID |
			syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWCGROUP,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Pdeathsig:                  syscall.SIGKILL,
		Setsid:                     true,
	}
	if cg != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = cg.fd()
	}
	if err := cmd.Start(); err != nil {
		return 4, fmt.Errorf("✗ cannot start the evaluation sandbox: %w", err)
	}
	pw.Close()

	// Read the graph with a hard cap.
	type readRes struct {
		n   int64
		err error
	}
	rc := make(chan readRes, 1)
	var sink io.Writer = io.Discard
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return 1, err
		}
		defer f.Close()
		sink = f
	}
	tooBig := false
	go func() {
		n, err := io.Copy(sink, io.LimitReader(pr, lim.outMax+1))
		if n > lim.outMax && cg != nil {
			cg.kill() // stop a writer that would otherwise block on the full pipe until the timeout
		}
		rc <- readRes{n, err}
	}()

	timedOut := false
	timer := time.AfterFunc(lim.timeout, func() {
		timedOut = true
		if cg != nil {
			cg.kill()
		} else {
			_ = cmd.Process.Kill()
		}
	})
	waitErr := cmd.Wait()
	timer.Stop()
	// stage1 is gone; make sure nothing else in the cgroup survives (stragglers that escaped
	// stage1's reaping are impossible in a pid ns, but the cgroup kill is the backstop).
	if cg != nil {
		cg.kill()
	}
	r := <-rc
	pr.Close()
	if r.n > lim.outMax {
		tooBig = true
	}

	var st stats
	if cg != nil {
		st = cg.stats()
	}
	code := exitCode(waitErr)
	if lim.cgroupOn && os.Getenv("EVALBOX_STATS") != "" {
		fmt.Fprintf(os.Stderr, "evalbox: exit=%d oom_kill=%d pids_max_hits=%d pids_peak=%d mem_peak=%d cpu_usec=%d out_bytes=%d wall=%.0fms\n",
			code, st.oomKill, st.pidsMax, st.pidsPeak, st.memPeak, st.cpuUsec, r.n, float64(time.Now().UnixNano()-cfg.Start)/1e6)
	}
	switch {
	case timedOut:
		return 10, fmt.Errorf("✗ evaluating bob.go took longer than %s and was stopped\n  hint: bob.go must only describe the build; long work belongs in a build step (bob.Run)", lim.timeout)
	case st.oomKill > 0:
		return 11, fmt.Errorf("✗ evaluating bob.go used more than %s of memory and was stopped (peak %s)\n  hint: a build definition should need far less; look for unbounded loops or huge in-memory data", lim.mem, human(st.memPeak))
	case tooBig:
		return 12, fmt.Errorf("✗ evaluating bob.go produced more than %s of graph output and was stopped", human(uint64(lim.outMax)))
	case code != 0 && st.pidsMax > 0:
		return 13, fmt.Errorf("✗ evaluating bob.go failed after trying to start more than %d processes or threads (exit %d)\n  hint: bob.go must not start processes", lim.pids, code)
	case code == 120 || code == 121:
		return 14, fmt.Errorf("✗ internal error: the evaluation sandbox could not be set up (exit %d); this is a bob bug", code)
	case code != 0:
		return 15, fmt.Errorf("✗ evaluating bob.go failed (exit %d)", code)
	}
	if st.pidsMax > 0 {
		fmt.Fprintf(os.Stderr, "warning: evaluation hit the process limit (%d) but succeeded\n", lim.pids)
	}
	return 0, nil
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

func parseStore(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var raw []string
	if strings.HasPrefix(s, "@") {
		b, err := os.ReadFile(s[1:])
		if err != nil {
			return nil, err
		}
		raw = strings.Fields(string(b))
	} else {
		raw = strings.Split(s, ",")
	}
	var out []string
	for _, p := range raw {
		p = filepath.Clean(p)
		if !strings.HasPrefix(p, "/nix/store/") || strings.Count(p, "/") != 3 {
			return nil, fmt.Errorf("not a store path: %q", p)
		}
		out = append(out, p)
	}
	return out, nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func human(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d KiB", b>>10)
	}
}
