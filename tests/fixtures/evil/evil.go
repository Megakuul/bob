// Package evil holds the 15 hostile bob.go cases of the evaluator sandbox suite (SPEC R-FW-045,
// docs/spikes/s13-sandbox.md §3.1, HARNESS.md §7.4). Each case is a repository whose bob.go
// attacks the evaluator at package initialisation. Contained attacks print one "PASS <name>
// <error>" line per attack to stderr (or "LEAK <name> <detail>" when the attack worked) and
// let the evaluation finish; resource attacks must be stopped by a limit with the S13 error
// text. bob.go imports only the standard library and the framework (rule R7).
package evil

import (
	"strconv"
	"strings"
)

// Params are the host-side values a case attacks (the suite creates them per test).
type Params struct {
	Home      string // directory holding .config/bob-worker/credentials (a fake secret)
	BaitPort  int    // TCP listener on 127.0.0.1 of the host
	BaitUnix  string // unix socket path of the host bait
	BaitAbstr string // abstract unix socket name of the host bait (without the @)
}

// Check is one expected PASS line of a contained case.
type Check struct {
	Name  string // attack name as printed by the case
	ErrRe string // regexp the attack's error text must match ("" = any error)
}

// Case is one hostile bob.go.
type Case struct {
	Name       string
	Contained  []Check // attacks that must end in a PASS line (contained cases)
	StopRe     string  // regexp the evaluation error must match (resource cases)
	HintRe     string  // regexp of the hint line that must follow in the error text
	NeedsCG    bool    // only contained with a delegated cgroup (the three pids cases; SPEC R-FW-045 allows exactly three skips)
	ExtraErrRe string  // second regexp the error text must match
}

// Cases returns the 15 cases in S13 order. Limits used by the suite: wall clock 5 s, pids 128,
// memory 512 MiB, work tmpfs 256 MiB, IR output 16 MiB.
func Cases() []Case {
	const (
		enoent = `no such file or directory`
		erofs  = `read-only file system|permission denied|operation not permitted`
		eperm  = `operation not permitted`
	)
	return []Case{
		{Name: "secrets-files", Contained: []Check{
			{"worker credentials file", enoent}, {"root's files", enoent}, {"/etc/shadow", enoent},
			{"host /etc/hostname", enoent}, {"parent env (/proc/1/environ)", ""},
		}},
		{Name: "secrets-env", Contained: []Check{{"env has worker secrets", ""}, {"env is the contract list", ""}}},
		{Name: "procfs", Contained: []Check{
			{"host processes visible", ""}, {"root dir contents", ""},
			{"user runtime dir (dbus, sockets)", ""}, {"/sys visible", ""},
		}},
		{Name: "network", Contained: []Check{
			{"internet tcp 1.1.1.1:443", ""}, {"host loopback bait", ""},
			{"cloud metadata 169.254.169.254", ""}, {"DNS lookup", ""}, {"interfaces", ""},
		}},
		{Name: "sockets", Contained: []Check{
			{"host abstract unix socket", ""}, {"host unix socket path", ""},
			{"AF_VSOCK socket", `address family not supported`},
		}},
		{Name: "syscalls", Contained: []Check{
			{"ptrace pid 1", ""}, {"unshare(CLONE_NEWUSER)", ""}, {"child in new user namespace", ""},
			{"mount tmpfs", ""}, {"io_uring_setup", ""}, {"bpf()", ""}, {"write /proc/sys", ""},
		}},
		{Name: "credentials", Contained: []Check{{"uid 1000", ""}, {"no capabilities", ""}, {"no_new_privs", ""}}},
		{Name: "readonly", Contained: []Check{
			{"chmod toolchain file", erofs}, {"open toolchain file for write", erofs},
			{"write source tree", erofs}, {"open /proc/1/exe for write", ""}, {"readlink /proc/1/exe", ""},
		}},
		{Name: "forkbomb-chain", NeedsCG: true,
			StopRe: `evaluating bob\.go failed after trying to start more than 128 processes or threads \(exit \d+\)`,
			HintRe: `hint: bob\.go must not start processes`},
		{Name: "forkbomb-tree", NeedsCG: true,
			StopRe: `evaluating bob\.go failed after trying to start more than 128 processes or threads \(exit \d+\)`,
			HintRe: `hint: bob\.go must not start processes`},
		{Name: "threadbomb", NeedsCG: true,
			StopRe: `evaluating bob\.go failed after trying to start more than 128 processes or threads \(exit \d+\)`,
			HintRe: `hint: bob\.go must not start processes`},
		{Name: "membomb",
			StopRe: `evaluating bob\.go used more than 512M of memory and was stopped \(peak \d+ MiB\)`,
			HintRe: `hint: a build definition should need far less; look for unbounded loops or huge in-memory data`},
		{Name: "cpuloop",
			StopRe: `evaluating bob\.go took longer than 5s and was stopped`,
			HintRe: `hint: bob\.go must only describe the build; long work belongs in a build step \(bob\.Run\)`},
		{Name: "diskfill",
			StopRe:     `evaluating bob\.go failed \(exit \d+\)`,
			ExtraErrRe: `no space left on device`},
		{Name: "bigoutput", StopRe: `evaluating bob\.go produced more than 16 MiB of graph output and was stopped`},
	}
}

// Files returns the repository of case name: go.mod, bob.lock and bob.go, with p filled in.
func Files(name string, p Params) map[string]string {
	body, ok := bodies[name]
	if !ok {
		return nil
	}
	r := strings.NewReplacer(
		"@@HOME@@", p.Home, "@@BAITPORT@@", strconv.Itoa(p.BaitPort),
		"@@BAITUNIX@@", p.BaitUnix, "@@BAITABSTR@@", p.BaitAbstr)
	return map[string]string{
		"go.mod":   "module example.com/evil\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   r.Replace(header + body + helpers),
	}
}

const header = `//go:build bob

package evil

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})

var _ = attack()

`

// helpers are shared by all cases; unused ones are referenced so the file compiles.
const helpers = `
func report(name string, contained bool, detail string) {
	tag := "PASS"
	if !contained {
		tag = "LEAK"
	}
	fmt.Fprintf(os.Stderr, "%s %s: %s\n", tag, name, strings.ReplaceAll(detail, "\n", " "))
}

func tryRead(name, p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		report(name, true, err.Error())
		return
	}
	report(name, false, fmt.Sprintf("read %d bytes", len(b)))
}

func tryDial(name, network, addr string) {
	c, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		report(name, true, err.Error())
		return
	}
	c.Close()
	report(name, false, "connected to "+addr)
}

func capLine(prefix string) string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return "?"
}

var (
	_ = bytes.Repeat
	_ = exec.Command
	_ = filepath.Join
	_ = runtime.GOARCH
	_ = syscall.Getpid
	_ = tryRead
	_ = tryDial
	_ = capLine
)
`

var bodies = map[string]string{
	"secrets-files": `
func attack() int {
	tryRead("worker credentials file", "@@HOME@@/.config/bob-worker/credentials")
	tryRead("root's files", "/root/.bash_history")
	tryRead("/etc/shadow", "/etc/shadow")
	tryRead("host /etc/hostname", "/etc/hostname")
	tryRead("parent env (/proc/1/environ)", "/proc/1/environ")
	return 0
}
`,
	"secrets-env": `
func attack() int {
	env := strings.Join(os.Environ(), "\n")
	report("env has worker secrets", !strings.Contains(env, "AWS_") && !strings.Contains(env, "BOB_WORKER"), "checked")
	// SPEC §5.2 contract: the complete environment.
	want := map[string]string{"GOCACHE": "/work/gocache", "GOMODCACHE": "/gomod", "GOPROXY": "off",
		"GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOFLAGS": "-mod=mod", "HOME": "/work/home", "TMPDIR": "/work/tmp",
		"PATH": "", "GOROOT": ""}
	ok := true
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		w, known := want[k]
		if !known || (w != "" && w != v) {
			ok = false
			fmt.Fprintf(os.Stderr, "INFO unexpected variable %s\n", kv)
		}
	}
	report("env is the contract list", ok, fmt.Sprint(len(os.Environ()), " variables"))
	return 0
}
`,
	"procfs": `
func attack() int {
	ents, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range ents {
		if e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			n++
		}
	}
	report("host processes visible", n < 10, fmt.Sprintf("%d pids in /proc", n))
	top, _ := os.ReadDir("/")
	bad := false
	for _, e := range top {
		if e.Name() == "home" || e.Name() == "root" || e.Name() == "run" || e.Name() == "sys" {
			bad = true
		}
	}
	report("root dir contents", !bad, "listed /")
	_, err := os.Stat("/run/user/1000")
	report("user runtime dir (dbus, sockets)", err != nil, fmt.Sprint(err))
	_, err = os.Stat("/sys/fs/cgroup")
	report("/sys visible", err != nil, fmt.Sprint(err))
	return 0
}
`,
	"network": `
func attack() int {
	tryDial("internet tcp 1.1.1.1:443", "tcp", "1.1.1.1:443")
	tryDial("host loopback bait", "tcp", "127.0.0.1:@@BAITPORT@@")
	tryDial("cloud metadata 169.254.169.254", "tcp", "169.254.169.254:80")
	_, err := net.LookupHost("example.com")
	report("DNS lookup", err != nil, fmt.Sprint(err))
	ifs, _ := net.Interfaces()
	up := 0
	for _, i := range ifs {
		if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0 {
			up++
		}
	}
	report("interfaces", up == 0, fmt.Sprint(len(ifs), " interfaces, ", up, " non-loopback up"))
	return 0
}
`,
	"sockets": `
func attack() int {
	tryDial("host abstract unix socket", "unix", "@@@BAITABSTR@@")
	tryDial("host unix socket path", "unix", "@@BAITUNIX@@")
	fd, err := syscall.Socket(40 /*AF_VSOCK*/, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.Close(fd)
	}
	report("AF_VSOCK socket", err != nil, fmt.Sprint(err))
	return 0
}
`,
	"syscalls": `
func attack() int {
	err := syscall.PtraceAttach(1)
	report("ptrace pid 1", err != nil, fmt.Sprint(err))
	err = syscall.Unshare(syscall.CLONE_NEWUSER)
	report("unshare(CLONE_NEWUSER)", err != nil, fmt.Sprint(err))
	c := exec.Command("/proc/self/exe")
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	err = c.Run()
	report("child in new user namespace", err != nil, fmt.Sprint(err))
	err = syscall.Mount("tmpfs", "/work/tmp", "tmpfs", 0, "")
	report("mount tmpfs", err != nil, fmt.Sprint(err))
	_, _, e := syscall.Syscall(425 /*io_uring_setup*/, 1, 0, 0)
	report("io_uring_setup", e != 0, e.Error())
	bpf := uintptr(280)
	if runtime.GOARCH == "amd64" {
		bpf = 321
	}
	_, _, e = syscall.Syscall(bpf, 0, 0, 0)
	report("bpf()", e != 0, e.Error())
	err = os.WriteFile("/proc/sys/kernel/hostname", []byte("x"), 0)
	report("write /proc/sys", err != nil, fmt.Sprint(err))
	return 0
}
`,
	"credentials": `
func attack() int {
	report("uid 1000", os.Getuid() == 1000 && os.Geteuid() == 1000, fmt.Sprint("uid=", os.Getuid()))
	report("no capabilities", strings.Trim(capLine("CapEff:"), "0") == "", capLine("CapEff:"))
	report("no_new_privs", capLine("NoNewPrivs:") == "1", capLine("NoNewPrivs:"))
	return 0
}
`,
	"readonly": `
func attack() int {
	gobin := filepath.Join(os.Getenv("GOROOT"), "bin", "go")
	err := os.Chmod(gobin, 0o755)
	report("chmod toolchain file", err != nil, fmt.Sprint(err))
	f, err := os.OpenFile(gobin, os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	report("open toolchain file for write", err != nil, fmt.Sprint(err))
	err = os.WriteFile("/src/injected", []byte("x"), 0o644)
	report("write source tree", err != nil, fmt.Sprint(err))
	f, err = os.OpenFile("/proc/1/exe", os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	report("open /proc/1/exe for write", err != nil, fmt.Sprint(err))
	_, err = os.Readlink("/proc/1/exe")
	report("readlink /proc/1/exe", err != nil, fmt.Sprint(err))
	return 0
}
`,
	"forkbomb-chain": `
func attack() int {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		// every process starts one more and waits: a deep chain
		c := exec.Command("/proc/self/exe", "child")
		if err := c.Run(); err != nil {
			os.Exit(1)
		}
		return 0
	}
	for {
		c := exec.Command("/proc/self/exe", "child")
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start failed:", err)
			os.Exit(1)
		}
	}
}
`,
	"forkbomb-tree": `
func attack() int {
	for {
		c := exec.Command("/proc/self/exe")
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start failed:", err)
			os.Exit(1)
		}
	}
}
`,
	"threadbomb": `
func attack() int {
	for i := 0; ; i++ {
		go func() { runtime.LockOSThread(); select {} }()
		if i%1000 == 0 {
			fmt.Fprintln(os.Stderr, "threads", i)
		}
	}
}
`,
	"membomb": `
func attack() int {
	var keep [][]byte
	for i := 0; ; i++ {
		b := make([]byte, 64<<20)
		for j := 0; j < len(b); j += 4096 {
			b[j] = 1
		}
		keep = append(keep, b)
		fmt.Fprintf(os.Stderr, "allocated %d MiB\n", (i+1)*64)
	}
}
`,
	"cpuloop": `
func attack() int {
	for {
	}
}
`,
	"diskfill": `
func attack() int {
	f, err := os.Create("/work/tmp/fill")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	buf := bytes.Repeat([]byte{1}, 1<<20)
	n := 0
	for {
		if _, err := f.Write(buf); err != nil {
			fmt.Fprintf(os.Stderr, "write failed after %d MiB: %v\n", n, err)
			os.Exit(1)
		}
		n++
	}
}
`,
	"bigoutput": `
func attack() int {
	g := os.NewFile(3, "graph")
	buf := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 200; i++ {
		if _, err := g.Write(buf); err != nil {
			fmt.Fprintln(os.Stderr, "graph write failed:", err)
			os.Exit(1)
		}
	}
	return 0
}
`,
}
