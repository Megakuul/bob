package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3count"
)

// M1.6 acceptance: the stack starts and stops cleanly.
func TestStartStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := Start(ctx, Options{Dir: t.TempDir(), Orchestrators: 2, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Info.Orchestrators) != 2 || len(s.Info.Workers) != 1 {
		t.Fatalf("processes: %+v", s.Info)
	}
	if _, err := ReadInfo(s.Info.Dir); err != nil {
		t.Fatal(err)
	}
	// The proxy fronts a working S3.
	c, err := s3count.Fetch(ctx, s.Info.S3ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lists(s.Info.Buckets[0]) != 0 {
		t.Fatalf("unexpected LIST on a fresh stack: %+v", c)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Info.Dir, "devstack.json")); !os.IsNotExist(err) {
		t.Fatalf("devstack.json still present: %v", err)
	}
	if err := s.Stop(); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestStrictFailsOnStubs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := Start(ctx, Options{Dir: t.TempDir(), Strict: true}); err == nil {
		t.Skip("bob-server modes are implemented; strict start succeeded")
	}
}

// H1: the harness contracts of srvtest (api_url, attach proxy, clock offset).
func TestHarnessContracts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-server")
	script := "#!/bin/sh\nenv > \"$BOB_STATE_DIR/env\"\nexec sleep 60\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Start(ctx, Options{Dir: filepath.Join(dir, "stack"), Orchestrators: 2, Workers: 1, Server: fake, ClockOffset: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	in := s.Info
	if in.AttachProxyURL == "" || in.Orchestrators[0].APIURL == "" || in.Orchestrators[1].APIURL == in.Orchestrators[0].APIURL || in.APIURL != in.Orchestrators[0].APIURL {
		t.Fatalf("info: %+v", in)
	}
	r, err := http.Get(in.AttachProxyURL + "/streams")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]int
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || len(m) != 2 {
		t.Fatalf("streams: %v %v", m, err)
	}
	r.Body.Close()
	read := func(name string) string {
		var b []byte
		for range 100 {
			if b, err = os.ReadFile(filepath.Join(in.Dir, name, "env")); err == nil && len(b) > 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return string(b)
	}
	if env := read("orchestrator-1"); !strings.Contains(env, "BOB_TEST_CLOCK_OFFSET=1m30s\n") {
		t.Fatalf("orchestrator env lacks clock offset:\n%s", env)
	}
	w := read("worker-0")
	if strings.Contains(w, "BOB_TEST_CLOCK_OFFSET") || strings.Count(w, "BOB_ORCHESTRATOR_ADDRS=http://127.0.0.1:") != 1 || !strings.Contains(w, ",http://127.0.0.1:") {
		t.Fatalf("worker env:\n%s", w)
	}
}
