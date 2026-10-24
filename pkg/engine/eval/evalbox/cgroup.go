package evalbox

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Delegated cgroup v2 handling (S13 §5.2 step 1). Because of cgroup v2's no-internal-process
// rule, evaluation leaves cannot live in a cgroup that holds processes. bob therefore moves
// itself into <root>/bob-supervisor once, enables the controllers on <root>, and creates one
// leaf <root>/eval-<id> per evaluation. It only does that when it is alone in its cgroup (a
// worker unit with Delegate=yes, or `systemd-run --scope -p Delegate=yes bob ...`); a cgroup
// shared with other processes (a login shell's scope) is never rearranged.

const supervisorCgroup = "bob-supervisor"

type cgroup struct {
	dir string
	f   *os.File
}

type cgroupStats struct {
	oomKill, pidsMax, memPeak uint64
}

func ownCgroup() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for l := range strings.SplitSeq(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", p), nil
		}
	}
	return "", errors.New("no cgroup v2 hierarchy")
}

func hasControllers(dir string, want ...string) bool {
	have := strings.Fields(readTrim(filepath.Join(dir, "cgroup.subtree_control")))
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// delegatedRoot returns the cgroup that evaluation leaves are created in, preparing it when
// this process is alone in its cgroup. The error says why there is no usable delegation.
func delegatedRoot() (string, error) {
	own, err := ownCgroup()
	if err != nil {
		return "", err
	}
	if filepath.Base(own) == supervisorCgroup {
		root := filepath.Dir(own)
		if hasControllers(root, "memory", "pids") {
			return root, nil
		}
		return "", fmt.Errorf("the memory and pids controllers are not enabled in %s", root)
	}
	if hasControllers(own, "memory", "pids") {
		// Only the root cgroup can hold processes and enable controllers at once.
		return own, nil
	}
	procs := strings.Fields(readTrim(filepath.Join(own, "cgroup.procs")))
	self := strconv.Itoa(os.Getpid())
	if len(procs) != 1 || procs[0] != self {
		return "", fmt.Errorf("cgroup %s is shared with %d other processes", own, len(procs)-1)
	}
	sup := filepath.Join(own, supervisorCgroup)
	if err := os.Mkdir(sup, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cgroup %s is not delegated to this user (%w)", own, err)
	}
	if err := os.WriteFile(filepath.Join(sup, "cgroup.procs"), []byte(self), 0); err != nil {
		_ = os.Remove(sup)
		return "", fmt.Errorf("cgroup %s is not delegated to this user (%w)", own, err)
	}
	ctl := "+memory +pids"
	if strings.Contains(readTrim(filepath.Join(own, "cgroup.controllers")), "cpu") {
		ctl += " +cpu"
	}
	if err := os.WriteFile(filepath.Join(own, "cgroup.subtree_control"), []byte(ctl), 0); err != nil {
		return "", fmt.Errorf("cannot enable the memory and pids controllers in %s (delegated: %q): %w",
			own, readTrim(filepath.Join(own, "cgroup.controllers")), err)
	}
	return own, nil
}

// newCgroup creates the leaf of one evaluation with l's limits.
func newCgroup(l Limits) (*cgroup, error) {
	root, err := delegatedRoot()
	if err != nil {
		return nil, err
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	dir := filepath.Join(root, "eval-"+hex.EncodeToString(id[:]))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	c := &cgroup{dir: dir}
	set := []struct{ file, value string }{
		{"memory.max", strconv.FormatInt(l.MemoryBytes, 10)},
		{"memory.swap.max", "0"},
		{"memory.oom.group", "1"},
		{"pids.max", strconv.Itoa(l.Pids)},
		{"cpu.max", fmt.Sprintf("%d 100000", l.CPUs*100000)},
	}
	for _, s := range set {
		err := os.WriteFile(filepath.Join(dir, s.file), []byte(s.value), 0)
		if err != nil && errors.Is(err, os.ErrNotExist) && (s.file == "memory.swap.max" || s.file == "cpu.max") {
			continue // no swap accounting or no cpu controller: optional
		}
		if err != nil {
			c.remove()
			return nil, fmt.Errorf("set %s: %w", s.file, err)
		}
	}
	if c.f, err = os.Open(dir); err != nil {
		c.remove()
		return nil, err
	}
	return c, nil
}

func (c *cgroup) kill() { _ = os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0) }

func (c *cgroup) stats() cgroupStats {
	s := cgroupStats{
		oomKill: keyValue(filepath.Join(c.dir, "memory.events"), "oom_kill"),
		pidsMax: keyValue(filepath.Join(c.dir, "pids.events"), "max"),
	}
	s.memPeak, _ = strconv.ParseUint(readTrim(filepath.Join(c.dir, "memory.peak")), 10, 64)
	return s
}

// remove deletes the leaf; it can only go once it is empty, and cgroup.kill is asynchronous.
func (c *cgroup) remove() {
	if c.f != nil {
		_ = c.f.Close()
	}
	for range 400 {
		if err := os.Remove(c.dir); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		c.kill()
		time.Sleep(5 * time.Millisecond)
	}
}

func keyValue(path, key string) uint64 {
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

// fd is the cgroup descriptor clone3(CLONE_INTO_CGROUP) needs.
func (c *cgroup) fd() int { return int(c.f.Fd()) }
