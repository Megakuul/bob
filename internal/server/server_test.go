package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type critical struct{ Func }

func (critical) Critical() bool { return true }

func TestRunRestartsFailingComponents(t *testing.T) {
	var runs atomic.Int32
	flaky := Func{N: "flaky", F: func(ctx context.Context) error {
		switch runs.Add(1) {
		case 1:
			panic("controller bug")
		case 2:
			return errors.New("transient")
		}
		<-ctx.Done()
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, quiet, time.Second, flaky) }()
	deadline := time.Now().Add(10 * time.Second)
	for runs.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run after an orderly stop = %v", err)
	}
	if runs.Load() != 3 {
		t.Errorf("component ran %d times, want 3 (panic, error, then healthy)", runs.Load())
	}
}

func TestRunStopsOnCriticalFailure(t *testing.T) {
	stopped := make(chan struct{})
	other := Func{N: "controller", F: func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }}
	lis := critical{Func{N: "listener", F: func(context.Context) error { return errors.New("accept: broken") }}}
	err := Run(context.Background(), quiet, time.Second, other, lis)
	if err == nil || !strings.Contains(err.Error(), "listener: accept: broken") {
		t.Errorf("Run = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Errorf("the other component was not stopped")
	}
}

func TestRunGrace(t *testing.T) {
	stuck := Func{N: "stuck", F: func(context.Context) error { select {} }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, quiet, 100*time.Millisecond, stuck); err == nil || !strings.Contains(err.Error(), "stuck did not stop") {
		t.Errorf("Run = %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	base := map[string]string{"BOB_STORAGE_ENDPOINT": "http://127.0.0.1:9000", "BOB_SERVER_NAME": "o1"}
	env := func(extra map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}
	c, err := ConfigFromEnv(ModeOrchestrator, env(map[string]string{"BOB_LISTEN": "127.0.0.1:1234"}))
	if err != nil || c.Listen != "127.0.0.1:1234" || c.AdminListen != "127.0.0.1:9090" || c.Name != "o1" {
		t.Errorf("orchestrator config = %+v, %v", c, err)
	}
	c, err = ConfigFromEnv(ModeOrchestrator, env(map[string]string{"BOB_LISTEN": "x:1", "BOB_SERVER_LISTEN": ":8000"}))
	if err != nil || c.Listen != ":8000" {
		t.Errorf("BOB_SERVER_LISTEN wins over BOB_LISTEN: %+v, %v", c, err)
	}
	c, err = ConfigFromEnv(ModeWorker, env(map[string]string{"BOB_ORCHESTRATOR_ADDRS": "http://a:1, http://b:2", "BOB_WORKER_SLOTS": "3"}))
	if err != nil || len(c.OrchestratorAddrs) != 2 || c.Slots != 3 {
		t.Errorf("worker config = %+v, %v", c, err)
	}
	for name, e := range map[string]map[string]string{
		"no endpoint":     {"BOB_STORAGE_ENDPOINT": ""},
		"short token":     {"BOB_ADMIN_TOKEN": "short"},
		"bad base url":    {"BOB_SERVER_BASE_URL": "ftp://x"},
		"bad probe cred":  {"BOB_STORAGE_WORKER_PROBE_CREDENTIAL": "nocolon"},
		"bad orch addr":   {"BOB_ORCHESTRATOR_ADDRS": "not a url"},
		"bad slots":       {"BOB_WORKER_SLOTS": "0"},
		"bad slots (nan)": {"BOB_WORKER_SLOTS": "many"},
	} {
		mode := ModeOrchestrator
		if strings.Contains(name, "orch addr") || strings.Contains(name, "slots") {
			mode = ModeWorker
		}
		if _, err := ConfigFromEnv(mode, env(e)); !IsConfigError(err) {
			t.Errorf("%s: err = %v, want a ConfigError", name, err)
		}
	}
}

// TestAdminTokenOnlyInTestBuilds: BOB_ADMIN_TOKEN is refused by release builds and accepted by
// -tags bobtest builds (run the package both ways).
func TestAdminTokenOnlyInTestBuilds(t *testing.T) {
	env := map[string]string{"BOB_STORAGE_ENDPOINT": "http://127.0.0.1:9000", "BOB_SERVER_NAME": "o1",
		"BOB_ADMIN_TOKEN": strings.Repeat("a", 32)}
	c, err := ConfigFromEnv(ModeOrchestrator, func(k string) string { return env[k] })
	if bootstrapToken {
		if err != nil || c.AdminToken == "" {
			t.Errorf("bobtest build: %v", err)
		}
		return
	}
	if !IsConfigError(err) || !strings.Contains(err.Error(), "bob-server admin create-user") {
		t.Errorf("release build accepted BOB_ADMIN_TOKEN: %v", err)
	}
}
