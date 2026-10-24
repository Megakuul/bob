package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// S3 identities of the dev stack. The orchestrator user name is the one the shipped
// control-bucket.json lets in (deploy/policies/control-bucket.json).
const (
	WorkerAccessKey       = "worker-bob"
	WorkerSecretKey       = "worker-bob-secret-0123456789"
	OrchestratorAccessKey = "orchestrator-bob"
	OrchestratorSecretKey = "orchestrator-bob-secret-0123456789"
)

// applyPolicies installs deploy/policies on RustFS: the worker and orchestrator identity
// policies as canned policies attached to two users, and the bucket policy on bob-control.
// RustFS' own admin API (/rustfs/admin/v3, plain JSON) is used; no extra tool is needed.
func applyPolicies(ctx context.Context, fs *rustfs.Server) error {
	root, err := serverenv.Root()
	if err != nil {
		return err
	}
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "policies", name))
		if err != nil {
			return nil, fmt.Errorf("devstack: %w", err)
		}
		return b, nil
	}
	admin := func(method, path string, q url.Values, body []byte) error {
		p := path
		if len(q) > 0 {
			p += "?" + q.Encode()
		}
		resp, err := fs.Do(ctx, fs.Root, method, p, nil, body)
		if err != nil {
			return fmt.Errorf("devstack: %w", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("devstack: apply policies: %s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return nil
	}
	for _, r := range []struct{ file, policy, user, secret string }{
		{"worker.json", "bob-worker", WorkerAccessKey, WorkerSecretKey},
		{"orchestrator.json", "bob-orchestrator", OrchestratorAccessKey, OrchestratorSecretKey},
	} {
		doc, err := read(r.file)
		if err != nil {
			return err
		}
		if err := admin(http.MethodPut, "/rustfs/admin/v3/add-canned-policy", url.Values{"name": {r.policy}}, doc); err != nil {
			return err
		}
		u, _ := json.Marshal(map[string]string{"secretKey": r.secret, "status": "enabled"})
		if err := admin(http.MethodPut, "/rustfs/admin/v3/add-user", url.Values{"accessKey": {r.user}}, u); err != nil {
			return err
		}
		if err := admin(http.MethodPut, "/rustfs/admin/v3/set-user-or-group-policy",
			url.Values{"policyName": {r.policy}, "userOrGroup": {r.user}, "isGroup": {"false"}}, nil); err != nil {
			return err
		}
	}
	doc, err := read("control-bucket.json")
	if err != nil {
		return err
	}
	if doc, err = allowHarnessRoot(doc); err != nil {
		return err
	}
	resp, err := fs.Do(ctx, fs.Root, http.MethodPut, "/"+rustfs.Buckets[0]+"?policy", nil, doc)
	if err != nil {
		return fmt.Errorf("devstack: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("devstack: put control bucket policy: %d %s", resp.StatusCode, b)
	}
	return nil
}

// allowHarnessRoot adds the harness root user to the usernames the control bucket policy lets
// in. RustFS evaluates the bucket policy for the root user too (unlike AWS); the harness seeds
// and inspects bob-control as root. The shipped file itself names only the orchestrator.
func allowHarnessRoot(doc []byte) ([]byte, error) {
	var d struct {
		Version   string
		Statement []map[string]any
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("devstack: control-bucket.json: %w", err)
	}
	for _, st := range d.Statement {
		cond, _ := st["Condition"].(map[string]any)
		ne, _ := cond["StringNotEquals"].(map[string]any)
		if names, ok := ne["aws:username"].([]any); ok {
			ne["aws:username"] = append(names, rustfs.RootAccessKey)
		}
	}
	return json.Marshal(d)
}
