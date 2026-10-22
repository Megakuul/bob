package env

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
	"github.com/megakuul/bob/tests/serverenv/stack"
)

// RemoteBuild runs `bob build --remote <args>` in dir against the stack as its admin.
func RemoteBuild(t testing.TB, x Extra, dir string, args ...string) harness.Result {
	t.Helper()
	spec := harness.Spec{Dir: dir, Timeout: 5 * time.Minute, Env: []string{
		"BOB_SERVER=" + x.APIURL, "BOB_TOKEN=" + x.AdminToken, "BOB_HOME=" + t.TempDir(),
	}}
	return harness.Run(t, "bob", spec, append([]string{"build", "--remote"}, args...)...)
}

// RootCreds are the credentials of the stack's S3 (the harness root).
func RootCreds(s *stack.Stack) s3sig.Creds {
	return s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region}
}

// ListKeys lists a bucket directly on RustFS with the root credential. It is the harness's
// own LIST (the proxy is bypassed, so it never counts as bob's), used to inspect state.
func ListKeys(t testing.TB, s *stack.Stack, bucket, prefix string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var keys []string
	token := ""
	for {
		q := "?list-type=2&prefix=" + prefix
		if token != "" {
			q += "&continuation-token=" + token
		}
		resp, err := rustfs.Do(ctx, s.Info.S3URL, RootCreds(s), http.MethodGet, "/"+bucket+q, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Contents    []struct{ Key string }
			IsTruncated bool
			NextToken   string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("list %s/%s: %v", bucket, prefix, err)
		}
		for _, c := range out.Contents {
			keys = append(keys, c.Key)
		}
		if !out.IsTruncated {
			return keys
		}
		token = out.NextToken
	}
}

// PutRaw writes one object directly on RustFS as the root.
func PutRaw(t testing.TB, s *stack.Stack, bucket, key string, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := rustfs.Do(ctx, s.Info.S3URL, RootCreds(s), http.MethodPut, "/"+bucket+"/"+key, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw PUT %s/%s = %d", bucket, key, resp.StatusCode)
	}
}

// GetRaw reads one object directly on RustFS as the root; a missing object returns nil.
func GetRaw(t testing.TB, s *stack.Stack, bucket, key string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := rustfs.Do(ctx, s.Info.S3URL, RootCreds(s), http.MethodGet, "/"+bucket+"/"+key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw GET %s/%s = %d", bucket, key, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ExtraOrchestrator starts one more bob-server orchestrator against the stack's proxy (same
// credentials as the stack's own), with extra environment entries such as the clock offset
// BOB_TEST_CLOCK_OFFSET. It ends with the test.
func ExtraOrchestrator(t testing.TB, s *stack.Stack, name string, extra ...string) *serverenv.Process {
	t.Helper()
	cmd := exec.Command(harness.Bin(t, "bob-server"), "orchestrator")
	cmd.Env = append([]string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TZ=UTC", "LC_ALL=C",
		"BOB_STORAGE_ENDPOINT=" + s.Info.S3ProxyURL,
		"BOB_STORAGE_REGION=" + s.Info.Region,
		"AWS_ACCESS_KEY_ID=" + s.Info.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + s.Info.SecretKey,
		"BOB_SERVER_NAME=" + name,
		"BOB_STATE_DIR=" + filepath.Join(s.Info.Dir, name),
	}, extra...)
	p, err := serverenv.StartProcess(cmd, filepath.Join(t.TempDir(), name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(5 * time.Second) })
	return p
}

// Describe names a process for failure messages.
func Describe(p *serverenv.Process) string { return fmt.Sprintf("pid %d (log %s)", p.Pid(), p.Log) }
