package evalbox

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Without a delegated cgroup the memory limit is enforced by a polling watchdog (SPEC R-FW-045,
// ruling G13): it sums the anonymous and shared resident memory of every process below
// stage1 plus the /work tmpfs pages, which is what memory.max would charge. The same walk
// counts tasks, one input of the pids classification without a cgroup.

type usage struct {
	memory uint64 // bytes
	tasks  int
}

// sandboxUsage measures the process tree rooted at pid (stage1, pid 1 of the sandbox).
func sandboxUsage(pid int) usage {
	var u usage
	seen := map[int]bool{}
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		base := "/proc/" + strconv.Itoa(p)
		mem, threads := statusUsage(base + "/status")
		u.memory += mem
		u.tasks += threads
		tids, _ := os.ReadDir(base + "/task")
		for _, t := range tids {
			b, _ := os.ReadFile(base + "/task/" + t.Name() + "/children")
			for c := range strings.FieldsSeq(string(b)) {
				if n, err := strconv.Atoi(c); err == nil {
					queue = append(queue, n)
				}
			}
		}
	}
	// The /work tmpfs of the sandbox, seen through stage1's root. Counted only once it is that
	// tmpfs: before stage1 has pivoted, its root is the caller's, whose /work may be any
	// filesystem (the rootless harness binds the repository there), and the used blocks of a
	// disk are no memory of the sandbox (seen as "peak 127701 MiB" on a 151 GB disk).
	var st syscall.Statfs_t
	if syscall.Statfs("/proc/"+strconv.Itoa(pid)+"/root/work", &st) == nil && st.Type == tmpfsMagic && st.Blocks >= st.Bfree &&
		!sameFile("/proc/"+strconv.Itoa(pid)+"/root/work", "/work") {
		u.memory += (st.Blocks - st.Bfree) * uint64(st.Bsize)
	}
	return u
}

// tmpfsMagic is TMPFS_MAGIC of statfs(2).
const tmpfsMagic = 0x01021994

// sameFile reports whether a and b are the same directory (b missing: false).
func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// statusUsage returns RssAnon+RssShmem (bytes) and Threads of one /proc/<pid>/status.
func statusUsage(path string) (uint64, int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var mem uint64
	threads := 0
	for l := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		switch k {
		case "RssAnon", "RssShmem":
			mem += n << 10 // kB
		case "Threads":
			threads = int(n)
		}
	}
	return mem, threads
}
