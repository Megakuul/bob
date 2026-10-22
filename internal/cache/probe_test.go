package cache

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// startObjects starts a RustFS with bob's buckets and returns the artifacts bucket.
func startObjects(t *testing.T) *Objects {
	t.Helper()
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	o, err := OpenObjects(ctx, s3client.Options{Endpoint: s.URL, Region: "us-east-1",
		AccessKey: rustfs.RootAccessKey, SecretKey: rustfs.RootSecretKey}, "bob-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// TestObjectsConditionalOps pins the backend behaviour promotion relies on (RustFS 1.0.3):
// create-only PUT, If-Match PUT, CopyObject guarded by the source ETag and create-only on
// the destination with replaced metadata, single ranges and 416.
func TestObjectsConditionalOps(t *testing.T) {
	o := startObjects(t)
	ctx := context.Background()
	etag, err := o.Put(ctx, "a", []byte("0123456789"), "", Cond{IfNoneMatch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Put(ctx, "a", []byte("x"), "", Cond{IfNoneMatch: true}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("create-only over an existing key: %v", err)
	}
	if _, err := o.Put(ctx, "a", []byte("x"), "", Cond{IfMatch: `"nope"`}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("If-Match with a stale ETag: %v", err)
	}
	if err := o.Copy(ctx, "a", `"0123"`, "b", map[string]string{"bob-narhash": "h1"}, true); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("copy with a stale source ETag: %v", err)
	}
	if err := o.Copy(ctx, "a", etag, "b", map[string]string{"bob-narhash": "h1"}, true); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := o.Copy(ctx, "a", etag, "b", map[string]string{"bob-narhash": "h2"}, true); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("create-only copy over an existing key: %v", err)
	}
	inf, err := o.Head(ctx, "b")
	if err != nil || inf.Meta["bob-narhash"] != "h1" || inf.Size != 10 {
		t.Fatalf("head b: %+v %v", inf, err)
	}
	obj, err := o.Get(ctx, "b", "bytes=2-4", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj.Body)
	obj.Body.Close()
	if string(b) != "234" || obj.ContentRange != "bytes 2-4/10" || obj.Total != 10 {
		t.Fatalf("range: %q %q %d", b, obj.ContentRange, obj.Total)
	}
	if _, err := o.Get(ctx, "b", "bytes=20-", ""); !errors.Is(err, ErrRange) {
		t.Fatalf("range past the end: %v", err)
	}
	if _, err := o.Get(ctx, "missing", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := o.Delete(ctx, "missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}
