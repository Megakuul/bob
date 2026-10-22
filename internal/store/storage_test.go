package store_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
	"github.com/megakuul/bob/tests/serverenv/stack"
)

// TestOpenStorageStartupChecks runs the start-up checks of R-STORE-102 and R-SEC-102 against
// RustFS with the shipped policies applied (the devstack's S3 side; its server processes are
// not used): an orchestrator with a correct policy set starts, a worker credential starts a
// worker although it may not probe bob-fleet, and the orchestrator refuses once the control
// bucket policy is gone.
func TestOpenStorageStartupChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := stack.Start(ctx, stack.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	if !s.Info.PoliciesReady {
		t.Skip("devstack applies no policies")
	}
	cfg := store.StorageConfig{Endpoint: s.Info.S3URL, Region: s.Info.Region,
		Control: "bob-control", Fleet: "bob-fleet", Analytics: "bob-analytics", Artifacts: "bob-artifacts",
		WorkerProbeCredential: s.Info.WorkerAccess + ":" + s.Info.WorkerSecret}
	as := func(access, secret string) {
		t.Setenv("AWS_ACCESS_KEY_ID", access)
		t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	}

	as(s.Info.OrchAccess, s.Info.OrchSecret)
	st, err := store.OpenStorage(ctx, cfg, "orchestrator")
	if err != nil {
		t.Fatalf("orchestrator with the shipped policies: %v", err)
	}
	if st.Control == nil || st.Fleet == nil {
		t.Fatalf("orchestrator storage lacks a bucket: %+v", st)
	}

	as(s.Info.WorkerAccess, s.Info.WorkerSecret)
	if st, err := store.OpenStorage(ctx, cfg, "worker"); err != nil || st.Control != nil || st.Fleet == nil {
		t.Fatalf("worker with the shipped policies: %+v, %v", st, err)
	}

	root := s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region}
	do := func(method, path string, body []byte) (int, []byte) {
		resp, err := rustfs.Do(ctx, s.Info.S3URL, root, method, path, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	code, policy := do(http.MethodGet, "/bob-control?policy", nil)
	if code != 200 {
		t.Fatalf("read control bucket policy: %d %s", code, policy)
	}
	if code, b := do(http.MethodDelete, "/bob-control?policy", nil); code/100 != 2 {
		t.Fatalf("delete control bucket policy: %d %s", code, b)
	}
	defer do(http.MethodPut, "/bob-control?policy", policy)

	as(s.Info.OrchAccess, s.Info.OrchSecret)
	_, err = store.OpenStorage(ctx, cfg, "orchestrator")
	var r *store.Refusal
	if !errors.As(err, &r) || r.Msg != "control bucket is readable with the worker credential" || r.ExitCode() != 78 {
		t.Errorf("orchestrator without the control bucket policy: %v, want the R-SEC-102 refusal", err)
	}
}
