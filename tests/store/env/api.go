package env

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/serverenv/stack"
)

// Extra is what the server suites need from <dir>/devstack.json beyond stack.Info. tools/devstack
// does not write these fields yet (HARNESS.md §13.2 owner: tools/devstack); a stack that lacks
// one makes MustExtra fail the test as a harness gap, not as red.
type Extra struct {
	APIURL          string `json:"api_url"`     // public listener, http://127.0.0.1:<port>
	AdminToken      string `json:"admin_token"` // bearer token of a bootstrap admin
	WorkerAccess    string `json:"worker_access_key"`
	WorkerSecret    string `json:"worker_secret_key"`
	OrchAccess      string `json:"orchestrator_access_key"`
	OrchSecret      string `json:"orchestrator_secret_key"`
	PoliciesApplied bool   `json:"policies_applied"`
}

// ReadExtra reads the extra fields of a running stack.
func ReadExtra(dir string) (Extra, error) {
	var x Extra
	b, err := os.ReadFile(filepath.Join(dir, "devstack.json"))
	if err != nil {
		return x, fmt.Errorf("env: %w", err)
	}
	if err := json.Unmarshal(b, &x); err != nil {
		return x, fmt.Errorf("env: devstack.json: %w", err)
	}
	return x, nil
}

// MustExtra reads the extra fields and requires the ones named by need ("api", "admin").
func MustExtra(t testing.TB, s *stack.Stack, need ...string) Extra {
	t.Helper()
	x, err := ReadExtra(s.Info.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range need {
		switch {
		case n == "api" && x.APIURL == "":
			t.Fatalf("harness gap: devstack.json has no api_url (tools/devstack must publish it)")
		case n == "admin" && x.AdminToken == "":
			t.Fatalf("harness gap: devstack.json has no admin_token (tools/devstack must publish it)")
		}
	}
	return x
}

// Call is one Connect-protocol unary call with a JSON body.
type Call struct {
	Status int
	Body   []byte
}

// RPC calls "<package.Service>/<Method>" under /api/ (SPEC §6.2) on the stack's public listener as the admin.
// token "" uses the admin token; token "-" sends no credential.
func RPC(t testing.TB, x Extra, method, token string, req any) Call {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, x.APIURL+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Connect-Protocol-Version", "1")
	switch token {
	case "":
		r.Header.Set("Authorization", "Bearer "+x.AdminToken)
	case "-":
	default:
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("rpc %s: %v", method, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return Call{Status: resp.StatusCode, Body: b}
}

// Total sums the requests of one bucket in c (every operation, every credential).
func Total(c s3count.Counts, bucket string) int {
	n := 0
	for _, r := range c.Rows {
		if r.Bucket == bucket {
			n += r.Count
		}
	}
	return n
}

// Ops sums the requests of one bucket and operation (s3count.OpGet, OpPut, ...).
func Ops(c s3count.Counts, bucket, op string) int {
	n := 0
	for _, r := range c.Rows {
		if r.Bucket == bucket && r.Op == op {
			n += r.Count
		}
	}
	return n
}

// ProxyCounts fetches the counters of the proxy the stack's servers use.
func ProxyCounts(t testing.TB, s *stack.Stack) s3count.Counts {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := s3count.Fetch(ctx, s.Info.S3ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ResetProxy clears the counters of the stack's proxy.
func ResetProxy(t testing.TB, s *stack.Stack) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.Info.S3ProxyURL+"/_s3count", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
