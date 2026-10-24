package rustfs_test

import (
	"context"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// A fresh instance has bob's four buckets and honours conditional writes
// (the property D60's store relies on), and Stop leaves no process behind.
func TestStartBucketsConditionalStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Stop(); err != nil {
			t.Error(err)
		}
		if err := syscall.Kill(s.Pid(), 0); err == nil {
			t.Errorf("pid %d still alive after Stop", s.Pid())
		}
	}()
	if s.Port < 23000 || s.Port > 23999 {
		t.Errorf("port %d outside the harness range", s.Port)
	}
	status := func(method, path string, hdr http.Header, body string) (int, http.Header) {
		t.Helper()
		resp, err := s.Do(ctx, s.Root, method, path, hdr, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	for _, b := range rustfs.Buckets {
		if code, _ := status("HEAD", "/"+b, nil, ""); code != 200 {
			t.Errorf("HEAD %s: %d", b, code)
		}
	}
	code, h := status("PUT", "/bob-fleet/leases/l1", http.Header{"If-None-Match": {"*"}}, "a")
	if code != 200 {
		t.Fatalf("create: %d", code)
	}
	etag := h.Get("ETag")
	for _, tc := range []struct {
		hdr  http.Header
		want int
	}{
		{http.Header{"If-None-Match": {"*"}}, 412},
		{http.Header{"If-Match": {`"0000"`}}, 412},
		{http.Header{"If-Match": {etag}}, 200},
	} {
		if code, _ := status("PUT", "/bob-fleet/leases/l1", tc.hdr, "b"); code != tc.want {
			t.Errorf("PUT %v: %d, want %d", tc.hdr, code, tc.want)
		}
	}
}
