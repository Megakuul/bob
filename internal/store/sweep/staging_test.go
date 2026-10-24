package sweep_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store/sweep"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// TestStagingSweep: objects under staging/ younger than 7 days stay, older ones go (the clock is
// advanced through the now argument); every LIST is attributed to bob-sweep/staging-sweep.
func TestStagingSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	be.Creds(t)
	cfg := be.Bucket(t, "artifacts")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, k := range []string{"staging/b1/j1/1/x.nar.zst", "staging/b1/j1/1/COMPLETE.json", "nix/narinfo/keep.narinfo"} {
		resp, err := be.FS.Do(ctx, be.FS.Root, http.MethodPut, "/"+cfg.Name+"/"+k, nil, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("seed %s: %d", k, resp.StatusCode)
		}
	}
	s, err := sweep.NewStaging(ctx, cfg.Endpoint, cfg.Region, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.EnsureLifecycle(ctx)
	t.Logf("lifecycle rule applied: %v (%v)", applied, err)

	if n, err := s.Sweep(ctx, time.Now(), sweep.StagingMaxAge); err != nil || n != 0 {
		t.Fatalf("sweep of fresh staging deleted %d, %v; want 0", n, err)
	}
	if n, err := s.Sweep(ctx, time.Now().Add(sweep.StagingMaxAge+time.Hour), sweep.StagingMaxAge); err != nil || n != 2 {
		t.Fatalf("sweep 7 d later deleted %d, %v; want the 2 staging objects", n, err)
	}
	resp, err := be.FS.Do(ctx, be.FS.Root, http.MethodGet, "/"+cfg.Name+"/nix/narinfo/keep.narinfo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("an object outside staging/ was touched: %d", resp.StatusCode)
	}
	lists := 0
	for _, r := range be.Proxy.Counts().Rows {
		if r.Op == s3count.OpList {
			lists += r.Count
			if r.Sweep != sweep.StagingController {
				t.Errorf("LIST attributed to %q, want %q", r.Sweep, sweep.StagingController)
			}
		}
	}
	if lists == 0 {
		t.Errorf("the proxy saw no LIST")
	}
}
