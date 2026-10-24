// Package rustfs starts a fresh single-node RustFS (the pinned upstream 1.0.3
// binary, D63) for one test suite and creates bob's four buckets. Each suite gets
// its own instance, data directory and port (HARNESS.md §8).
package rustfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// Buckets are bob's buckets (SPEC §05.2).
var Buckets = []string{"bob-control", "bob-fleet", "bob-analytics", "bob-artifacts"}

// Root credentials of every harness instance. They never leave loopback.
const (
	RootAccessKey = "bobharnessroot"
	RootSecretKey = "bobharnessroot-secret-0123456789"
)

// Options configure Start.
type Options struct {
	Dir     string // data and log directory; required
	Port    int    // 0 picks a free port in the harness range
	Offline bool   // fail instead of downloading the pinned binary
	NoSetup bool   // do not create the buckets
}

// Server is a running RustFS.
type Server struct {
	URL  string // http://127.0.0.1:<port>
	Port int
	Dir  string
	Root s3sig.Creds
	proc *serverenv.Process
}

// Start launches RustFS and waits until it serves signed requests.
func Start(ctx context.Context, o Options) (*Server, error) {
	if o.Dir == "" {
		return nil, errors.New("rustfs: Options.Dir is required")
	}
	bin, err := serverenv.ToolPath(ctx, "rustfs", o.Offline)
	if err != nil {
		return nil, err
	}
	port := o.Port
	if port == 0 {
		if port, err = serverenv.FreePort(); err != nil {
			return nil, err
		}
	}
	data := filepath.Join(o.Dir, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return nil, fmt.Errorf("rustfs: %w", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	cmd := exec.Command(bin, "server", "--address", addr, data)
	cmd.Env = append(os.Environ(),
		"RUSTFS_ACCESS_KEY="+RootAccessKey,
		"RUSTFS_SECRET_KEY="+RootSecretKey,
		"RUSTFS_CONSOLE_ENABLE=false",
		"RUSTFS_REGION=us-east-1",
		"RUSTFS_OBS_LOGGER_LEVEL=warn",
		"RUSTFS_OBS_LOG_DIRECTORY="+filepath.Join(o.Dir, "logs"),
	)
	proc, err := serverenv.StartProcess(cmd, filepath.Join(o.Dir, "rustfs.log"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		URL: "http://" + addr, Port: port, Dir: o.Dir,
		Root: s3sig.Creds{AccessKey: RootAccessKey, SecretKey: RootSecretKey, Region: "us-east-1"},
		proc: proc,
	}
	if err := s.waitReady(ctx); err != nil {
		return nil, errors.Join(err, s.Stop())
	}
	if !o.NoSetup {
		for _, b := range Buckets {
			if err := s.CreateBucket(ctx, b); err != nil {
				return nil, errors.Join(err, s.Stop())
			}
		}
	}
	return s, nil
}

func (s *Server) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var last error
	for {
		// ListBuckets goes straight to RustFS, never through the counting proxy.
		resp, err := s.Do(ctx, s.Root, http.MethodGet, "/", nil, nil)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %s", resp.Status)
		} else {
			last = err
		}
		select {
		case <-s.proc.Exited():
			return fmt.Errorf("rustfs: exited during start-up (%v), see %s: %w", s.proc.Err(), s.proc.Log, serverenv.ErrHarness)
		case <-ctx.Done():
			return fmt.Errorf("rustfs: not ready on %s: %v: %w", s.URL, last, serverenv.ErrHarness)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// CreateBucket creates bucket (idempotent).
func (s *Server) CreateBucket(ctx context.Context, bucket string) error {
	resp, err := s.Do(ctx, s.Root, http.MethodPut, "/"+bucket, nil, nil)
	if err != nil {
		return fmt.Errorf("rustfs: create bucket %s: %w", bucket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("rustfs: create bucket %s: %s: %s", bucket, resp.Status, b)
	}
	return nil
}

// Do sends one signed path-style request to base + path (path may carry a
// query). It is the harness's own minimal S3 client.
func Do(ctx context.Context, base string, c s3sig.Creds, method, path string, hdr http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rustfs: %w", err)
	}
	req.ContentLength = int64(len(body))
	for k, v := range hdr {
		req.Header[k] = v
	}
	sum := sha256.Sum256(body)
	s3sig.Sign(req, c, hex.EncodeToString(sum[:]), time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rustfs: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// Do is the package Do against this server.
func (s *Server) Do(ctx context.Context, c s3sig.Creds, method, path string, hdr http.Header, body []byte) (*http.Response, error) {
	return Do(ctx, s.URL, c, method, path, hdr, body)
}

// Pid is the server's process id (for the chaos driver).
func (s *Server) Pid() int { return s.proc.Pid() }

// Stop kills RustFS and waits for it. It is safe to call more than once. RustFS
// keeps no state a test needs after Stop, so there is no graceful phase.
func (s *Server) Stop() error {
	if err := s.proc.Stop(0); err != nil {
		return fmt.Errorf("rustfs: %w", err)
	}
	return nil
}
