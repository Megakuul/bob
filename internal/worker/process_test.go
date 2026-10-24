package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/isolation"
	"github.com/megakuul/bob/internal/store"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == isolation.ChildArg {
		os.Exit(isolation.Child(os.Args[2]))
	}
	if len(os.Args) > 1 && os.Args[1] == "job-run" {
		if err := RunJob(context.Background(), os.Stdin, os.Stdout, NotBuilt{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestExecRunsJobInContainer: a claimed job runs as `job-run` in a job container; the stub
// runner's BOB-E-9008 comes back as the job's outcome, and nothing of the job is left.
func TestExecRunsJobInContainer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root (the worker runs as root)")
	}
	if err := isolation.Check(); err != nil {
		t.Skipf("host cannot isolate builds: %v", err)
	}
	base, err := os.MkdirTemp("/var/tmp", "bob-worker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	p, err := NewProcess(ProcessConfig{Fleet: &store.Bucket{}, StateDir: base, Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(base, "worker", "etc", "nix.conf")); err != nil || !strings.Contains(string(b), "auto-allocate-uids = true") {
		t.Fatalf("root-mode nix.conf: %v %s", err, b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	o := p.exec(ctx, Claim{JobID: "01jjob00000000000000000009", Epoch: 1, Spec: []byte("spec")})
	if o.Status != "failed" || !strings.Contains(o.Error, "BOB-E-9008") && !strings.Contains(o.Error, "not implemented yet") {
		t.Fatalf("outcome %+v, want the runner stub's not-implemented failure", o)
	}
	if _, err := os.Stat(filepath.Join(base, "worker", "jobs", "01jjob00000000000000000009")); !os.IsNotExist(err) {
		t.Errorf("the job directory was left behind: %v", err)
	}
	if procs := isolation.Procs("01jjob00000000000000000009"); len(procs) != 0 {
		t.Errorf("processes remain in the job cgroup: %v", procs)
	}
}
