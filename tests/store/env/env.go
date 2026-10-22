// Package env is the server-data test environment shared by the store, fleet and policy
// suites: one RustFS behind the counting proxy per test, scratch buckets, the credentials
// of bob-server processes, and the single place that turns "the feature answers
// BOB-E-9008" into a red test (tests/harness).
package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tools/devstack/stack"
)

// Backend is a RustFS with the counting proxy in front of it.
type Backend struct {
	FS    *rustfs.Server
	Proxy *s3count.Server
	n     int
	// Last is the name of the bucket the latest Bucket or Open call created.
	Last string
}

// Start launches RustFS and the proxy; both end with the test.
func Start(t testing.TB) *Backend {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true, NoSetup: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Stop() })
	px, err := s3count.New(s3count.Options{Backend: fs.URL, Secrets: map[string]string{fs.Root.AccessKey: fs.Root.SecretKey}, Roles: map[string]string{fs.Root.AccessKey: "root"}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := s3count.Listen(px, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return &Backend{FS: fs, Proxy: srv}
}

// Bucket creates a fresh bucket and returns the config that store.Open takes. The AWS SDK
// default chain reads the root credentials from the environment of the test.
func (b *Backend) Bucket(t testing.TB, role string) store.BucketConfig {
	t.Helper()
	b.n++
	name := fmt.Sprintf("t%d-%s-%d", os.Getpid(), role, b.n)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.FS.CreateBucket(ctx, name); err != nil {
		t.Fatal(err)
	}
	b.Last = name
	return store.BucketConfig{Endpoint: b.Proxy.URL, Region: b.FS.Root.Region, Name: name, Role: role}
}

// Creds exports the root credentials where the SDK default chain finds them.
func (b *Backend) Creds(t testing.TB) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", b.FS.Root.AccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", b.FS.Root.SecretKey)
	t.Setenv("AWS_REGION", b.FS.Root.Region)
}

// ServerEnv is the environment of a bob-server process that talks to the proxy (the same
// variables tools/devstack sets). Extra entries come last and win.
func (b *Backend) ServerEnv(extra ...string) []string {
	e := []string{
		"BOB_STORAGE_ENDPOINT=" + b.Proxy.URL,
		"BOB_STORAGE_REGION=" + b.FS.Root.Region,
		"AWS_ACCESS_KEY_ID=" + b.FS.Root.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + b.FS.Root.SecretKey,
	}
	return append(e, extra...)
}

// NotImplemented reports whether err is the stub answer (diag BOB-E-9008).
func NotImplemented(err error) bool {
	var de *diag.Error
	return errors.As(err, &de) && de.Code == diag.CodeNotImplemented
}

// Red fails t as red when err is the stub answer and returns true; the caller returns.
// Any other error is the caller's to judge.
func Red(t testing.TB, err error) bool {
	t.Helper()
	if !NotImplemented(err) {
		return false
	}
	harness.RedFeature(t, err.Error())
	return true
}

// Open opens a bucket on the backend. It returns nil after failing t as red when
// store.Open is a stub; any other error is fatal.
func (b *Backend) Open(t testing.TB, role string) *store.Bucket {
	t.Helper()
	b.Creds(t)
	cfg := b.Bucket(t, role)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	bk, err := store.Open(ctx, cfg)
	if Red(t, err) {
		return nil
	}
	if err != nil {
		t.Fatalf("store.Open(%s): %v", cfg.Name, err)
	}
	return bk
}

// StartStack starts the all-in-one dev stack (tools/devstack). While the server modes are
// stubs their processes exit with BOB-E-9008: the test then fails as red (naming the first
// such process) and StartStack returns nil. A process that exits for any other reason is
// fatal.
func StartStack(t testing.TB, o stack.Options) *stack.Stack {
	t.Helper()
	if o.Dir == "" {
		o.Dir = t.TempDir()
	}
	o.Offline = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := stack.Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	procs := append(append([]stack.Proc{}, s.Info.Orchestrators...), s.Info.Workers...)
	for _, p := range procs {
		if p.Running {
			continue
		}
		log, _ := os.ReadFile(p.Log)
		if strings.Contains(string(log), harness.NotImplemented) {
			harness.RedFeature(t, fmt.Sprintf("bob-server process %s exited at start (%s)", p.Name, firstLine(string(log))))
			return nil
		}
		t.Fatalf("devstack: %s exited during start-up: %s (log %s):\n%s", p.Name, p.ExitNote, filepath.Base(p.Log), log)
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// Wait polls cond every 100 ms until it holds or d has passed.
func Wait(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// DefaultBuckets creates bob's four default buckets (the names bob-server uses unless
// configured otherwise).
func (b *Backend) DefaultBuckets(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range rustfs.Buckets {
		if err := b.FS.CreateBucket(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
}
