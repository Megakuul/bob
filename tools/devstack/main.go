// Command devstack runs the all-in-one local dev stack (HARNESS.md §13.2).
//
//	devstack up [-orchestrators N] [-workers M] [-dir D] [-json] [-strict] [-offline] [-seed=false]
//	devstack down [-dir D]
//	devstack kill [-dir D] <process>
//
// `up` stays in the foreground (children die with it); run it in the background.
// It prints one JSON line when the stack is ready. Unless -seed=false it first creates the
// seed data set (projects p1/p2 and one build each, stack.Seed) and lists it under "seed".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/megakuul/bob/tests/serverenv/stack"
)

const defaultDir = "out/devstack"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devstack:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: devstack up|down|kill [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := fs.String("dir", defaultDir, "state directory")
	orch := fs.Int("orchestrators", 1, "orchestrator processes (up)")
	workers := fs.Int("workers", 1, "worker processes (up)")
	asJSON := fs.Bool("json", true, "print the info line as JSON (up)")
	strict := fs.Bool("strict", false, "fail if a server process exits during start-up (up)")
	offline := fs.Bool("offline", false, "never download pinned tools (up)")
	seed := fs.Bool("seed", true, "create the seed projects and builds (up)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "up":
		return up(*dir, *orch, *workers, *asJSON, *strict, *offline, *seed)
	case "down":
		i, err := stack.ReadInfo(*dir)
		if err != nil {
			return err
		}
		return syscall.Kill(i.Pid, syscall.SIGTERM)
	case "kill":
		if fs.NArg() != 1 {
			return errors.New("usage: devstack kill [-dir D] <process>")
		}
		i, err := stack.ReadInfo(*dir)
		if err != nil {
			return err
		}
		for _, p := range append(append([]stack.Proc{i.Rustfs}, i.Orchestrators...), i.Workers...) {
			if p.Name == fs.Arg(0) {
				return syscall.Kill(-p.Pid, syscall.SIGKILL)
			}
		}
		return fmt.Errorf("no process %q", fs.Arg(0))
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func up(dir string, orch, workers int, asJSON, strict, offline, seed bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// The test clock of this stack is <dir>/clock (the harness contract of tests/auth/authenv
	// SetClock). Set it here: an inherited value may name another stack's file.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.Setenv("BOB_TEST_CLOCK_FILE", filepath.Join(abs, "clock")); err != nil {
		return err
	}
	s, err := stack.Start(ctx, stack.Options{Dir: dir, Orchestrators: orch, Workers: workers, Strict: strict, Offline: offline})
	if err != nil {
		return err
	}
	if seed && len(s.Info.Orchestrators) > 0 && s.Info.Orchestrators[0].Running {
		if err := s.Seed(ctx); err != nil {
			// the stack still runs; suites that need the seed report it as missing
			fmt.Fprintln(os.Stderr, "devstack:", err)
		}
	}
	b, _ := json.Marshal(s.Info)
	if asJSON {
		fmt.Println(string(b))
	} else {
		fmt.Printf("stack up: s3 %s (proxy %s), state %s/devstack.json\n", s.Info.S3URL, s.Info.S3ProxyURL, s.Info.Dir)
	}
	<-ctx.Done()
	done := make(chan error, 1)
	go func() { done <- s.Stop() }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		return errors.New("stop timed out")
	}
}
