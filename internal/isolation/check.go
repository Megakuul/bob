// Package isolation is the worker's build isolation (SPEC §05.8, R-FLEET-008 / R-SEC-000,
// ADR 0015 decision 4): the host check a root worker runs before it starts, the job container
// every job runs in (bob's own namespace code: no OCI runtime, no daemon), and the root-mode
// configuration of the embedded Nix inside it.
//
// The job container has its own cgroup v2 leaf bob-worker.slice/job-<jobID> with memory.max,
// pids.max and cpu.max; new mount, pid, ipc, uts and cgroup namespaces; a tmpfs root with
// only what the job needs (the persistent store at /nix, the bob binary, a private /tmp, a
// generated /etc); and nothing of the worker: no environment, no state directory, no
// credential file, no view of the worker's processes.
package isolation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

// Refusal is a host that cannot isolate builds (BOB-E-7010): "worker cannot isolate builds:
// <Reason>".
type Refusal struct {
	Reason string
	Err    error // the underlying failure, if any
}

func (r *Refusal) Error() string { return "worker cannot isolate builds: " + r.Reason }

func (r *Refusal) Unwrap() error { return r.Err }

// CgroupRoot is where the cgroup v2 hierarchy of the worker's cgroup namespace is mounted.
const CgroupRoot = "/sys/fs/cgroup"

// Slice is the cgroup below CgroupRoot that holds one leaf per job.
const Slice = "bob-worker.slice"

// cgroup2Magic is CGROUP2_SUPER_MAGIC (linux/magic.h).
const cgroup2Magic = 0x63677270

// MinLandlockABI is the Landlock ABI the evaluator sandbox needs on a worker (TCP rules).
const MinLandlockABI = 4

// Nix's auto-allocate-uids range (nix.conf start-id and id-count defaults, with use-cgroups
// every build gets up to 2^16 ids): builds run as these uids, so the worker's user namespace
// must map them (a privileged container that maps only 0–65535 cannot run builds).
const (
	nixStartID = 872415232
	nixIDCount = 128 * 65536
)

// Check runs the host checks of R-FLEET-008 in order: cgroup v2 with the memory, pids and cpu
// controllers and a usable bob-worker.slice, Landlock ABI ≥ 4, and the uid range that Nix's
// auto-allocate-uids hands to builds. It prepares bob-worker.slice (creates it and enables the
// controllers for its leaves). The first failure is a *Refusal.
func Check() error {
	if err := prepareSlice(CgroupRoot); err != nil {
		return &Refusal{Reason: err.Error()}
	}
	if abi := evalbox.LandlockABI(); abi < MinLandlockABI {
		return &Refusal{Reason: fmt.Sprintf("the kernel offers Landlock ABI %d, the evaluator sandbox needs %d or newer", abi, MinLandlockABI)}
	}
	if !idRangeMapped("/proc/self/uid_map", nixStartID, nixIDCount) || !idRangeMapped("/proc/self/gid_map", nixStartID, nixIDCount) {
		return &Refusal{Reason: fmt.Sprintf("Nix auto-allocate-uids needs the ids %d–%d, which this worker's user namespace does not map", nixStartID, nixStartID+nixIDCount-1)}
	}
	return nil
}

// prepareSlice checks that root is a cgroup v2 mount with the controllers jobs need, creates
// root/bob-worker.slice and enables memory, pids and cpu for its leaves.
func prepareSlice(root string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return fmt.Errorf("no cgroup v2 hierarchy at %s: %v", root, err)
	}
	if uint32(st.Type) != cgroup2Magic {
		return fmt.Errorf("%s is not a cgroup v2 (unified) hierarchy; boot with cgroup v2 (systemd.unified_cgroup_hierarchy=1)", root)
	}
	have := strings.Fields(readTrim(filepath.Join(root, "cgroup.controllers")))
	for _, c := range []string{"memory", "pids", "cpu"} {
		if !contains(have, c) {
			return fmt.Errorf("the cgroup v2 controller %q is not available in %s (have %q)", c, root, strings.Join(have, " "))
		}
	}
	if err := enable(root, "memory", "pids", "cpu"); err != nil {
		return fmt.Errorf("cannot enable the memory, pids and cpu cgroup controllers in %s: %v (the worker needs a delegated cgroup v2 subtree: run it as a systemd service with Delegate=yes or a privileged container with a private cgroup namespace)", root, err)
	}
	slice := filepath.Join(root, Slice)
	if err := os.Mkdir(slice, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cannot create the cgroup %s: %v", slice, err)
	}
	if err := enable(slice, "memory", "pids", "cpu"); err != nil {
		return fmt.Errorf("cannot enable the memory, pids and cpu cgroup controllers in %s: %v", slice, err)
	}
	return nil
}

// enable makes the controllers available to dir's children (no-op when they already are).
func enable(dir string, ctl ...string) error {
	have := strings.Fields(readTrim(filepath.Join(dir, "cgroup.subtree_control")))
	var add []string
	for _, c := range ctl {
		if !contains(have, c) {
			add = append(add, "+"+c)
		}
	}
	if len(add) == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(strings.Join(add, " ")), 0)
}

// idRangeMapped reports whether every id of [start, start+n) is inside one line of the id
// map file (an unreadable map counts as not mapped).
func idRangeMapped(file string, start, n uint64) bool {
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
		cnt, e2 := strconv.ParseUint(f[2], 10, 33)
		if e1 == nil && e2 == nil && start >= in && start+n <= in+cnt {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}
