package isolation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

const probeArg = "isolation-probe"

// probe is what the program inside the container reports about its world.
type probe struct {
	UID, PID     int
	Hostname     string
	Env          []string
	Procs        []string
	WorkerEnv    string // error text reading the worker's /proc/<pid>/environ
	SecretRead   string // error text reading the secret file by its host path
	NixWrite     string // error writing into /nix (the store bind is read-write: "" expected)
	Cgroup       string
	TmpEntries   int
	ChildStarted string
}

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == ChildArg {
		os.Exit(Child(os.Args[2]))
	}
	if len(os.Args) > 1 && os.Args[1] == probeArg {
		os.Exit(runProbe(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "isolation-eval" {
		os.Exit(runEval())
	}
	if len(os.Args) > 1 && os.Args[1] == "isolation-ir" {
		f := os.NewFile(3, "ir")
		_, _ = fmt.Fprintf(f, "{\"uid\":%d}", os.Getuid())
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "isolation-sleep" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runProbe(args []string) int {
	p := probe{UID: os.Getuid(), PID: os.Getpid(), Env: os.Environ()}
	p.Hostname, _ = os.Hostname()
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err == nil {
			p.Procs = append(p.Procs, e.Name())
		}
	}
	if _, err := os.ReadFile("/proc/" + args[0] + "/environ"); err != nil {
		p.WorkerEnv = err.Error()
	}
	if _, err := os.ReadFile(args[1]); err != nil {
		p.SecretRead = err.Error()
	}
	if err := os.WriteFile("/nix/probe", []byte("x"), 0o644); err != nil {
		p.NixWrite = err.Error()
	}
	b, _ := os.ReadFile("/proc/self/cgroup")
	p.Cgroup = strings.TrimSpace(string(b))
	tmp, _ := os.ReadDir("/tmp")
	p.TmpEntries = len(tmp)
	// A process that would outlive the job if the container did not clean up.
	c := exec.Command("/bin/probe", "isolation-sleep")
	if err := c.Start(); err != nil {
		p.ChildStarted = err.Error()
	}
	_ = json.NewEncoder(os.Stdout).Encode(p)
	return 3
}

// runEval runs inside the job container: one evalbox evaluation in strict mode whose step
// reports its uid as graph IR.
func runEval() int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := Evaluate(ctx, evalbox.Spec{
		Binds: []evalbox.Bind{{Host: "/bin", Sandbox: "/tool", Exec: true}},
		Dir:   "/work",
		Steps: []evalbox.Step{{Args: []string{"/tool/probe", "isolation-ir"}, Env: []string{"PATH=/tool"}}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaluate: %v\n", err)
		return 1
	}
	fmt.Printf("%s cgroup=%v\n", out.Graph, out.Cgroup)
	return 0
}

func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root (the worker runs as root)")
	}
	if err := Check(); err != nil {
		t.Skipf("host cannot isolate builds: %v", err)
	}
}

func TestCheckRefusesWithoutCgroupV2(t *testing.T) {
	err := prepareSlice(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cgroup v2") {
		t.Fatalf("prepareSlice on a plain directory: %v, want a cgroup v2 refusal", err)
	}
}

func TestIDRangeMapped(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		m    string
		want bool
	}{
		{"0 0 4294967295\n", true},
		{"0 0 65536\n", false},
		{"0 100000 65536\n872415232 200000 8388608\n", true},
		{"", false},
	} {
		f := filepath.Join(dir, "map")
		if err := os.WriteFile(f, []byte(c.m), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := idRangeMapped(f, nixStartID, nixIDCount); got != c.want {
			t.Errorf("idRangeMapped(%q) = %v, want %v", c.m, got, c.want)
		}
	}
}

func TestNixConf(t *testing.T) {
	c := NixConf([]string{"https://bob.example/nix"}, []string{"bob-1:abc"})
	for _, want := range []string{
		"sandbox = true\n", "sandbox-fallback = false\n", "build-users-group = \n", "auto-allocate-uids = true\n",
		"use-cgroups = true\n", "allowed-users = root\n", "trusted-users = root\n",
		"substituters = https://bob.example/nix https://cache.nixos.org\n", "require-sigs = true\n",
		"netrc-file = /etc/nix/netrc\n",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("nix.conf lacks %q:\n%s", want, c)
		}
	}
}

// TestContainer runs a probe in a job container and checks R-FLEET-008's container contract:
// pid namespace (only its own processes, none of the worker's), no worker environment, no
// host files, a private /tmp, the store bound at /nix, the cgroup namespace at the job leaf,
// limits on the leaf, and nothing left in the leaf afterwards.
func TestContainer(t *testing.T) {
	needRoot(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOB_ISOLATION_SECRET", "s3cr3t")
	base, err := os.MkdirTemp("/var/tmp", "bob-isolation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	secret := filepath.Join(base, "credentials")
	if err := os.WriteFile(secret, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := filepath.Join(base, "nix")
	if err := os.MkdirAll(st, 0o755); err != nil {
		t.Fatal(err)
	}
	job := fmt.Sprintf("t%d", time.Now().UnixNano())
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exit, err := Run(ctx, Spec{
		JobID: job, RootDir: filepath.Join(base, "root"), Store: st,
		Binds:  []Bind{{Host: exe, Path: "/bin/probe"}},
		Args:   []string{"/bin/probe", probeArg, strconv.Itoa(os.Getpid()), secret},
		Env:    []string{"PATH=/bin", "HOME=/root"},
		Limits: Limits{MemoryBytes: 512 << 20, Pids: 64, CPUs: 1},
		Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("Run: %v (stderr %s)", err, errb.String())
	}
	if exit != 3 {
		t.Fatalf("exit %d, want the probe's 3; stderr:\n%s", exit, errb.String())
	}
	var p probe
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatalf("probe output %q: %v", out.String(), err)
	}
	if p.PID != 1 {
		t.Errorf("the program is pid %d in the container, want 1 (own pid namespace)", p.PID)
	}
	if len(p.Procs) > 3 {
		t.Errorf("the container sees %d processes %v, want only its own", len(p.Procs), p.Procs)
	}
	for _, kv := range p.Env {
		if strings.Contains(kv, "s3cr3t") {
			t.Errorf("worker environment reached the container: %s", kv)
		}
	}
	if len(p.Env) != 2 {
		t.Errorf("environment %v, want exactly the job's", p.Env)
	}
	if p.WorkerEnv == "" {
		t.Errorf("the container read the worker's /proc/<pid>/environ")
	}
	if p.SecretRead == "" {
		t.Errorf("the container read a worker file by its host path")
	}
	if p.NixWrite != "" {
		t.Errorf("the store is not writable for root in the container: %s", p.NixWrite)
	}
	if _, err := os.Stat(filepath.Join(st, "probe")); err != nil {
		t.Errorf("the write to /nix did not land in the persistent store: %v", err)
	}
	if p.Cgroup != "0::/" {
		t.Errorf("/proc/self/cgroup = %q, want the job leaf as cgroup namespace root (0::/)", p.Cgroup)
	}
	if p.Hostname != "bob-job" {
		t.Errorf("host name %q, want bob-job (own uts namespace)", p.Hostname)
	}
	if p.TmpEntries != 0 {
		t.Errorf("/tmp has %d entries, want a private empty /tmp", p.TmpEntries)
	}
	if p.ChildStarted != "" {
		t.Errorf("the probe could not start a child process: %s", p.ChildStarted)
	}
	if procs := Procs(job); len(procs) != 0 {
		t.Errorf("processes remain in the job cgroup: %v", procs)
	}
	if _, err := os.Stat(filepath.Join(CgroupRoot, Slice, "job-"+job)); err == nil {
		t.Errorf("the job cgroup was not removed")
	}
}

// TestEvalboxInContainer: evalbox in strict mode works inside a job container (it delegates
// from the job leaf) and the evaluator does not run as root.
func TestEvalboxInContainer(t *testing.T) {
	needRoot(t)
	exe, _ := os.Executable()
	base, err := os.MkdirTemp("/var/tmp", "bob-isolation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	st := filepath.Join(base, "nix")
	_ = os.MkdirAll(st, 0o755)
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exit, err := Run(ctx, Spec{JobID: fmt.Sprintf("e%d", time.Now().UnixNano()), RootDir: filepath.Join(base, "root"), Store: st,
		Binds: []Bind{{Host: exe, Path: "/bin/probe"}}, Args: []string{"/bin/probe", "isolation-eval"},
		Limits: Limits{MemoryBytes: 4 << 30, Pids: 1024, CPUs: 2}, Stdout: &out, Stderr: &errb})
	if err != nil || exit != 0 {
		t.Fatalf("exit %d err %v\nstdout %s\nstderr %s", exit, err, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "cgroup=true") {
		t.Errorf("evalbox did not use a delegated cgroup inside the container: %s", out.String())
	}
	if strings.Contains(out.String(), `"uid":0}`) {
		t.Errorf("the evaluator ran as root: %s", out.String())
	}
}

// TestContainerKilledOnCancel: cancelling ctx kills the whole container.
func TestContainerKilledOnCancel(t *testing.T) {
	needRoot(t)
	exe, _ := os.Executable()
	base, err := os.MkdirTemp("/var/tmp", "bob-isolation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	st := filepath.Join(base, "nix")
	_ = os.MkdirAll(st, 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Run(ctx, Spec{JobID: fmt.Sprintf("k%d", time.Now().UnixNano()), RootDir: filepath.Join(base, "root"), Store: st,
		Binds: []Bind{{Host: exe, Path: "/bin/probe"}}, Args: []string{"/bin/probe", "isolation-sleep"}})
	if err == nil {
		t.Fatalf("Run returned no error after its context ended")
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Run took %v after cancel", d)
	}
}
