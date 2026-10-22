package policy

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/stack"
	"github.com/megakuul/bob/tests/store/env"
)

// iamDoc is the part of an identity or bucket policy document the tests read.
type iamDoc struct {
	Statement []struct {
		Effect    string
		Principal any
		Action    stringList
		Resource  stringList
		Condition map[string]map[string]stringList
	}
}

type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

// loadPolicy reads deploy/policies/<name>. A missing file is red: the file is not shipped yet.
func loadPolicy(t *testing.T, name string) (iamDoc, bool) {
	t.Helper()
	var d iamDoc
	b, err := os.ReadFile(filepath.Join(harness.Root(t), "deploy", "policies", name))
	if os.IsNotExist(err) {
		harness.RedFeature(t, "deploy/policies/"+name+" is not shipped")
		return d, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("deploy/policies/%s: %v", name, err)
	}
	return d, true
}

// model converts an identity policy document to the evaluator's statements.
func (d iamDoc) model() []stmt {
	var out []stmt
	for _, s := range d.Statement {
		m := stmt{deny: s.Effect == "Deny", actions: s.Action}
		for _, r := range s.Resource {
			m.resources = append(m.resources, strings.TrimPrefix(r, "arn:aws:s3:::"))
		}
		out = append(out, m)
	}
	return out
}

// TestRSEC002ShippedPolicyFiles: deploy/policies/{worker,orchestrator,control-bucket}.json say
// what the SPEC table says. The worker and orchestrator documents give the same answer as the
// reference model for the 27 operations on the 6 targets and for every fleet prefix.
func TestRSEC002ShippedPolicyFiles(t *testing.T) {
	parity.Covers(t, "R-SEC-002")
	worker, ok1 := loadPolicy(t, "worker.json")
	orch, ok2 := loadPolicy(t, "orchestrator.json")
	ctl, ok3 := loadPolicy(t, "control-bucket.json")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	for _, c := range []struct {
		name string
		got  []stmt
		want []stmt
	}{{"worker.json", worker.model(), workerPolicy}, {"orchestrator.json", orch.model(), orchestratorPolicy}} {
		for _, o := range ops {
			for _, tg := range targets {
				if g, w := expect(c.got, o, tg), expect(c.want, o, tg); g != w {
					t.Errorf("%s: %s on %s is %v, the SPEC table says %v", c.name, o.name, tg.name, g, w)
				}
			}
		}
		for _, p := range []string{"fleet/cfg", "orch/x", "ctl/x", "worker/x", "qt/x", "jact/00", "widx/00", "wreg/x", "wrs/x", "wrt/x", "whb/x", "job/x", "qs/x", "qh/x"} {
			for _, a := range []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"} {
				n := []need{{a, fleet + "/" + p}}
				if g, w := allowed(c.got, n), allowed(c.want, n); g != w {
					t.Errorf("%s: %s on bob-fleet/%s is %v, the SPEC table says %v", c.name, a, p, g, w)
				}
			}
		}
		for _, p := range []string{"nix/nar/x", "oci/x", "rel/p/1/a", "src/p/x", "staging/b/j/1/x"} {
			for _, a := range []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"} {
				n := []need{{a, artifacts + "/" + p}}
				if g, w := allowed(c.got, n), allowed(c.want, n); g != w {
					t.Errorf("%s: %s on bob-artifacts/%s is %v, the SPEC table says %v", c.name, a, p, g, w)
				}
			}
		}
		for _, a := range []string{"s3:ListBucket", "s3:CreateBucket", "s3:DeleteBucket", "s3:GetBucketPolicy", "s3:PutBucketPolicy", "s3:ListAllMyBuckets"} {
			for _, b := range []string{control, fleet, analytics, artifacts} {
				n := []need{{a, b}}
				if g, w := allowed(c.got, n), allowed(c.want, n); g != w {
					t.Errorf("%s: %s on %s is %v, the SPEC table says %v", c.name, a, b, g, w)
				}
			}
		}
	}
	// The bucket policy denies everyone except the orchestrator principals on bob-control.
	found := false
	for _, s := range ctl.Statement {
		if s.Effect != "Deny" || len(s.Action) != 1 || s.Action[0] != "s3:*" {
			continue
		}
		res := strings.Join(s.Resource, " ")
		if strings.Contains(res, "arn:aws:s3:::"+control+"/*") && strings.Contains(res, "arn:aws:s3:::"+control) && s.Condition["StringNotEquals"]["aws:username"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("control-bucket.json lacks Deny s3:* on bob-control and bob-control/* unless aws:username is an orchestrator principal")
	}
}

func shippedGo(t testing.TB, fn func(rel string, f *ast.File)) {
	t.Helper()
	root := harness.Root(t)
	for _, tree := range []string{"pkg", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			fn(filepath.ToSlash(rel), f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestRSEC100NoCredentialMintingInSource: no server package imports service/sts or service/iam
// (and no AssumeRole-style call exists), the acceptance rule of `go run ./tools/ci`.
func TestRSEC100NoCredentialMintingInSource(t *testing.T) {
	parity.Covers(t, "R-SEC-100")
	banned := map[string]bool{"AssumeRole": true, "AssumeRoleWithWebIdentity": true, "GetSessionToken": true, "GetFederationToken": true, "CreateAccessKey": true}
	shippedGo(t, func(rel string, f *ast.File) {
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if strings.HasPrefix(p, "github.com/aws/aws-sdk-go-v2/service/sts") || strings.HasPrefix(p, "github.com/aws/aws-sdk-go-v2/service/iam") {
				t.Errorf("%s imports %s: bob never mints S3 credentials (R-SEC-100)", rel, p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && banned[sel.Sel.Name] {
				t.Errorf("%s calls %s: bob has no credential-vending path (R-SEC-100)", rel, sel.Sel.Name)
			}
			return true
		})
	})
}

// TestRSEC100RPCsWithoutWorkerTokenReturnNoCredentials: an RPC without a worker token cannot
// return any S3 credential or worker token, and a user token is not a worker token.
func TestRSEC100RPCsWithoutWorkerTokenReturnNoCredentials(t *testing.T) {
	parity.Covers(t, "R-SEC-100")
	s := env.StartStack(t, stack.Options{Orchestrators: 1, Workers: 1})
	if s == nil {
		return
	}
	x := env.MustExtra(t, s, "api", "admin")
	secrets := []string{s.Info.SecretKey, s.Info.AccessKey, x.WorkerSecret, x.WorkerAccess, x.OrchSecret, x.OrchAccess}
	body := map[string]any{"job_id": "01J0000000000000000000000", "epoch": 1}
	for _, m := range []string{"bob.worker.v1.WorkerControlService/GetCheckoutCredential", "bob.worker.v1.WorkerControlService/Attach"} {
		for _, token := range []string{"-", "", "bob_wt_invalid"} { // none, admin user token, forged worker token
			c := env.RPC(t, x, m, token, body)
			if c.Status == 200 {
				t.Errorf("%s with token %q answered 200: %s", m, token, c.Body)
			}
			for _, sec := range secrets {
				if sec != "" && strings.Contains(string(c.Body), sec) {
					t.Errorf("%s with token %q leaked a credential in its answer", m, token)
				}
			}
		}
	}
	// Every public, parameterless read answers without any S3 credential or token material.
	for _, m := range []string{"bob.auth.v1.AuthService/GetServerInfo", "bob.fleet.v1.FleetService/ListWorkers", "bob.fleet.v1.FleetService/ListOrchestrators"} {
		c := env.RPC(t, x, m, "", map[string]any{})
		for _, sec := range secrets {
			if sec != "" && strings.Contains(string(c.Body), sec) {
				t.Errorf("%s leaked an S3 credential", m)
			}
		}
		if strings.Contains(string(c.Body), "sealed") || strings.Contains(string(c.Body), "bob_wt_") {
			t.Errorf("%s exposes worker token material: %.200s", m, c.Body)
		}
	}
}

// TestRSEC102PositivePathAndRefusal runs the orchestrator start-up self-check with a real
// worker credential: with the shipped control-bucket policy it starts; after the bucket
// policy is removed it refuses with exit 78 and the SPEC text.
func TestRSEC102PositivePathAndRefusal(t *testing.T) {
	parity.Covers(t, "R-SEC-102")
	ps := startPolicyStack(t)
	if ps == nil {
		return
	}
	x, err := env.ReadExtra(ps.s.Info.Dir)
	if err != nil {
		t.Fatal(err)
	}
	probe := "BOB_STORAGE_WORKER_PROBE_CREDENTIAL=" + x.WorkerAccess + ":" + x.WorkerSecret
	p := env.ExtraOrchestrator(t, ps.s, "orchestrator-probe", probe)
	select {
	case <-p.Exited():
		t.Fatalf("orchestrator with a correct policy set exited: %v (%s)", p.Err(), p.Log)
	case <-time.After(5 * time.Second):
	}

	docs := ps.policies()
	if r := ps.root.do("DELETE", "/"+control+"?policy", nil, nil); !r.ok() {
		t.Fatalf("remove the control bucket policy: %d %s", r.status, r.body)
	}
	defer ps.root.do("PUT", "/"+control+"?policy", nil, []byte(docs[control]))
	r := harness.Run(t, "bob-server", harness.Spec{Env: []string{
		"BOB_STORAGE_ENDPOINT=" + ps.s.Info.S3URL, "BOB_STORAGE_REGION=" + ps.s.Info.Region,
		"AWS_ACCESS_KEY_ID=" + ps.s.Info.AccessKey, "AWS_SECRET_ACCESS_KEY=" + ps.s.Info.SecretKey, probe,
	}, Timeout: time.Minute}, "orchestrator")
	if r.Exit != 78 || !strings.Contains(r.Stderr, "✗ control bucket is readable with the worker credential") {
		t.Errorf("orchestrator without the control bucket policy: exit %d, stderr:\n%s", r.Exit, r.Stderr)
	}
}
