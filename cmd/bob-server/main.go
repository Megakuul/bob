// Command bob-server is the bob build and cache server (modes: orchestrator, worker, admin).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/megakuul/bob/internal/isolation"
	"github.com/megakuul/bob/internal/safe"
	"github.com/megakuul/bob/internal/server"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/worker"
	"github.com/megakuul/bob/pkg/bob/diag"
)

const usage = `usage: bob-server <mode>

modes:
  orchestrator   serve the API and run the controllers
  worker         run builds for the orchestrators
  admin          administrative commands: create-user --login <name> [--admin]
  store fsck     check that every record of the control and fleet buckets decodes and validates

Configuration comes from BOB_* environment variables (SPEC §05.11.1).
`

// startTimeout bounds the start-up checks (storage probe, policy self-check).
const startTimeout = 2 * time.Minute

func main() {
	// A re-executed bob-server that sets up a job container (internal/isolation) is not
	// bob-server: it never reaches run.
	if len(os.Args) > 2 && os.Args[1] == isolation.ChildArg {
		os.Exit(isolation.Child(os.Args[2]))
	}
	os.Exit(run(os.Args[1:], os.Getenv, os.Stderr))
}

func run(args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	mode := args[0]
	switch mode {
	case "-h", "--help", "help":
		fmt.Fprint(stderr, usage)
		return 0
	case server.ModeOrchestrator, server.ModeWorker:
	case "store":
		if len(args) > 1 && args[1] == "fsck" {
			return storeFsck(getenv, os.Stdout, stderr)
		}
		fmt.Fprint(stderr, "usage: bob-server store fsck\n")
		return 2
	case "job-run":
		// Hidden: the program of a job container (worker side), never started by hand.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := worker.RunJob(ctx, os.Stdin, os.Stdout, worker.NotBuilt{}); err != nil {
			fmt.Fprintf(stderr, "✗ %v\n", err)
			return 1
		}
		return 0
	case "admin":
		// `admin create-user --login <name> --admin` (SPEC §6.3.2) writes UserRecord, the
		// login name, the users index and AdminIndex in the control bucket.
		if len(args) > 1 && args[1] == "create-user" {
			return adminCreateUser(args[2:], getenv, os.Stdin, os.Stdout, stderr)
		}
		fmt.Fprint(stderr, "usage: bob-server admin create-user --login <name> [--admin]  (password from stdin)\n")
		return 2
	default:
		fmt.Fprintf(stderr, "bob-server: unknown mode %q\n\n%s", mode, usage)
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With("mode", mode)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var env *server.Env
	err := safe.Run(func() error {
		sctx, cancel := context.WithTimeout(ctx, startTimeout)
		defer cancel()
		var err error
		env, err = server.Open(sctx, mode, getenv, log, workerHostCheck)
		return err
	})
	if err != nil {
		return report(stderr, err)
	}
	err = safe.Run(func() error {
		if mode == server.ModeOrchestrator {
			pctx, cancel := context.WithTimeout(ctx, startTimeout)
			parts, err := orchestratorParts(pctx, env)
			cancel()
			if err != nil {
				return err
			}
			return server.Orchestrator(ctx, env, parts)
		}
		parts, err := workerParts(env)
		if err != nil {
			return err
		}
		return server.Worker(ctx, env, parts)
	})
	if err != nil {
		return report(stderr, err)
	}
	return 0
}

// report prints a start-up refusal or failure and returns the exit status: 78 (EX_CONFIG)
// for refusals and bad configuration, the diag class status for bob errors, 1 otherwise.
func report(stderr io.Writer, err error) int {
	var (
		refusal *store.Refusal
		host    *server.HostRefusal
		cfgErr  *server.ConfigError
		de      *diag.Error
	)
	switch {
	case errors.As(err, &refusal):
		fmt.Fprintf(stderr, "✗ %s\nhint: %s\n", refusal.Msg, refusal.Hint)
		return refusal.ExitCode()
	case errors.As(err, &host):
		fmt.Fprintf(stderr, "✗ %s\nhint: %s\n", host.Msg, host.Hint)
		return host.ExitCode()
	case errors.As(err, &cfgErr):
		fmt.Fprintf(stderr, "✗ %s\n", cfgErr.Msg)
		return store.ExitConfig
	case errors.As(err, &de):
		fmt.Fprintf(stderr, "error[%s]: %s\n", de.Code.ID(), de.Error())
		if h := de.Hint(); h != "" {
			fmt.Fprintf(stderr, "hint: %s\n", h)
		}
		return diag.ExitCode([]*diag.Error{de})
	default:
		fmt.Fprintf(stderr, "✗ bob-server: %v\n", err)
		return 1
	}
}
