package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Delegated cgroup v2 handling. The worker's own cgroup (systemd Delegate=yes) is the "root".
// Because of the no-internal-processes rule, the worker first moves itself into <root>/worker,
// enables the controllers on <root>, and then creates one leaf <root>/eval-<id> per evaluation.
// The child is placed into the leaf atomically with clone3(CLONE_INTO_CGROUP).

type cgroup struct {
	dir string
	f   *os.File
}

type stats struct {
	oomKill, pidsMax, memPeak, cpuUsec, pidsPeak uint64
}

func ownCgroup() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "0::") {
			return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(l, "0::")), nil
		}
	}
	return "", errors.New("no cgroup v2 hierarchy (/proc/self/cgroup has no 0:: line)")
}

// delegatedRoot returns the cgroup that evaluation leaves are created under, preparing it once.
func delegatedRoot() (string, error) {
	own, err := ownCgroup()
	if err != nil {
		return "", err
	}
	root := own
	if filepath.Base(own) == "worker" {
		root = filepath.Dir(own)
	}
	ctl, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return "", err
	}
	have := string(ctl)
	if strings.Contains(have, "memory") && strings.Contains(have, "pids") && strings.Contains(have, "cpu") {
		return root, nil
	}
	// Move every process of root into root/worker (we own the delegated subtree), then enable.
	w := filepath.Join(root, "worker")
	if err := os.Mkdir(w, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cgroup %s is not delegated to this user (%w)", root, err)
	}
	procs, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	for _, p := range strings.Fields(string(procs)) {
		if err := os.WriteFile(filepath.Join(w, "cgroup.procs"), []byte(p), 0); err != nil && !errors.Is(err, syscall.ESRCH) {
			return "", fmt.Errorf("move pid %s into %s: %w", p, w, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0); err != nil {
		return "", fmt.Errorf("enable memory/pids/cpu controllers on %s: %w (controllers delegated: %q)", root, err, readTrim(filepath.Join(root, "cgroup.controllers")))
	}
	return root, nil
}

func newCgroup(l limits) (*cgroup, error) {
	root, err := delegatedRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, fmt.Sprintf("eval-%d", os.Getpid()))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	period := 100000
	set := map[string]string{
		"memory.max":      l.mem,
		"memory.swap.max": "0",
		"memory.oom.group": "1",
		"pids.max":        strconv.Itoa(l.pids),
		"cpu.max":         fmt.Sprintf("%d %d", int(l.cpu*float64(period)), period),
	}
	for k, v := range set {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0); err != nil {
			if k == "memory.swap.max" && errors.Is(err, os.ErrNotExist) {
				continue // no swap accounting configured
			}
			os.Remove(dir)
			return nil, fmt.Errorf("set %s=%s: %w", k, v, err)
		}
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	return &cgroup{dir: dir, f: f}, nil
}

func (c *cgroup) fd() int { return int(c.f.Fd()) }

func (c *cgroup) kill() { _ = os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0) }

func (c *cgroup) stats() stats {
	var s stats
	s.oomKill = kv(filepath.Join(c.dir, "memory.events"), "oom_kill")
	s.pidsMax = kv(filepath.Join(c.dir, "pids.events"), "max")
	s.cpuUsec = kv(filepath.Join(c.dir, "cpu.stat"), "usage_usec")
	if v, err := strconv.ParseUint(readTrim(filepath.Join(c.dir, "pids.peak")), 10, 64); err == nil {
		s.pidsPeak = v
	}
	if v, err := strconv.ParseUint(readTrim(filepath.Join(c.dir, "memory.peak")), 10, 64); err == nil {
		s.memPeak = v
	}
	return s
}

func (c *cgroup) remove() {
	c.f.Close()
	// The leaf can only be removed once it is empty; cgroup.kill is asynchronous.
	for i := 0; i < 200; i++ {
		if err := os.Remove(c.dir); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		c.kill()
		var ts = syscall.Timespec{Nsec: 5e6}
		_ = syscall.Nanosleep(&ts, nil)
	}
	fmt.Fprintln(os.Stderr, "evalbox: warning: could not remove", c.dir)
}

func kv(path, key string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) == 2 && fs[0] == key {
			v, _ := strconv.ParseUint(fs[1], 10, 64)
			return v
		}
	}
	return 0
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}
