package stack

import (
	"context"
	"os"
	"path/filepath"
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
