package policy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
	"github.com/megakuul/bob/tests/serverenv/stack"
	"github.com/megakuul/bob/tests/store/env"
)

// policyStack is a dev stack with the shipped policies applied: S3 identities for the
// worker and the orchestrator next to the harness root.
type policyStack struct {
	s      *stack.Stack
	root   *client
	worker *client
	orch   *client
}

// startPolicyStack returns nil after failing t as red while tools/devstack applies no
// policies. The server processes themselves are irrelevant here (they may still be stubs).
func startPolicyStack(t *testing.T) *policyStack {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := stack.Start(ctx, stack.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	if !s.Info.PoliciesReady {
		harness.RedFeature(t, "tools/devstack applies no shipped policies yet (Info.policies_applied=false): "+
			"deploy/policies/{worker,orchestrator,control-bucket}.json are not applied to RustFS, so the S11 matrix has nothing to test")
		return nil
	}
	x, err := env.ReadExtra(s.Info.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if x.WorkerAccess == "" || x.WorkerSecret == "" || x.OrchAccess == "" || x.OrchSecret == "" {
		t.Fatalf("harness gap: devstack.json has policies_applied=true but no worker_access_key/worker_secret_key/orchestrator_access_key/orchestrator_secret_key")
	}
	mk := func(a, k string) *client {
		return &client{t: t, base: s.Info.S3URL, creds: s3sig.Creds{AccessKey: a, SecretKey: k, Region: s.Info.Region}}
	}
	return &policyStack{
		s:      s,
		root:   mk(s.Info.AccessKey, s.Info.SecretKey),
		worker: mk(x.WorkerAccess, x.WorkerSecret),
		orch:   mk(x.OrchAccess, x.OrchSecret),
	}
}

// seed (re)creates the seed objects every probe starts from.
func (ps *policyStack) seed(t *testing.T) {
	t.Helper()
	for _, tg := range targets {
		if r := ps.root.do("PUT", "/"+tg.bucket+"/"+tg.prefix+"obj", nil, []byte("seed")); !r.ok() {
			t.Fatalf("seed %s: %d %s", tg.name, r.status, r.body)
		}
	}
	if r := ps.root.do("PUT", "/"+fleet+"/"+readableSrcKey, nil, []byte("copy source")); !r.ok() {
		t.Fatalf("seed copy source: %d %s", r.status, r.body)
	}
}

// reset undoes every bucket-level change an allowed operation made.
func (ps *policyStack) reset(bucket string, policy string) {
	b := "/" + bucket
	ps.root.do("DELETE", b+"?lifecycle", nil, nil)
	ps.root.do("PUT", b+"?versioning", nil, []byte(xmlVersionng))
	if policy != "" {
		ps.root.do("PUT", b+"?policy", nil, []byte(policy))
	} else {
		ps.root.do("DELETE", b+"?policy", nil, nil)
	}
}

func (ps *policyStack) policies() map[string]string {
	out := map[string]string{}
	for _, b := range []string{control, fleet, analytics, artifacts} {
		if r := ps.root.do("GET", "/"+b+"?policy", nil, nil); r.ok() {
			out[b] = r.body
		} else {
			// A harmless policy to put back: denies nothing that exists.
			out[b] = fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/__bob_never__/*"]}]}`, b)
		}
	}
	return out
}

// TestRSEC002PolicyMatrix is the S11 matrix: 27 operations × 6 targets, with the worker and the
// orchestrator credential, 0 deviations from the expected table (R-SEC-002, R-STORE-002).
// The worker's 27 operations on bob-control include GET, PUT and DELETE: every one is 403.
func TestRSEC002PolicyMatrix(t *testing.T) {
	parity.Covers(t, "R-SEC-002", "R-STORE-002")
	ps := startPolicyStack(t)
	if ps == nil {
		return
	}
	ps.seed(t)
	docs := ps.policies()
	cells := 0
	var deviations []string
	for _, who := range []struct {
		name   string
		c      *client
		policy []stmt
	}{{"worker", ps.worker, workerPolicy}, {"orchestrator", ps.orch, orchestratorPolicy}} {
		p := &probe{c: who.c, policy: docs}
		for _, tg := range targets {
			for _, o := range ops {
				ps.seed(t)
				got := p.run(o.name, tg)
				ps.reset(tg.bucket, docs[tg.bucket])
				cells++
				want := expect(who.policy, o, tg)
				switch {
				case want && !got.ok():
					deviations = append(deviations, fmt.Sprintf("%s %s on %s: want allowed, got %d %.80s", who.name, o.name, tg.name, got.status, got.body))
				case !want && got.status != http.StatusForbidden:
					deviations = append(deviations, fmt.Sprintf("%s %s on %s: want 403, got %d %.80s", who.name, o.name, tg.name, got.status, got.body))
				}
			}
		}
	}
	if cells != 2*27*6 {
		t.Fatalf("ran %d cells, want %d", cells, 2*27*6)
	}
	if len(deviations) != 0 {
		t.Errorf("%d of %d cells deviate from the expected table:\n%s", len(deviations), cells, strings.Join(deviations, "\n"))
	}
}

// TestRSEC002PrefixTable is the acceptance addition of R-SEC-002: each fleet prefix gets one
// PUT allowed and one denied for the worker; deletes are denied everywhere in fleet.
func TestRSEC002PrefixTable(t *testing.T) {
	parity.Covers(t, "R-SEC-002", "R-STORE-002")
	ps := startPolicyStack(t)
	if ps == nil {
		return
	}
	put := func(c *client, key string) int { return c.do("PUT", "/"+fleet+"/"+key, nil, []byte("x")).status }
	for _, k := range []string{"wreg/x", "wrs/x", "wrt/x", "whb/x", "job/x", "qs/x", "qh/x"} {
		if st := put(ps.worker, k); st != http.StatusOK {
			t.Errorf("worker PUT on %s = %d, want 200", k, st)
		}
	}
	for _, k := range []string{"worker/x", "fleet/cfg", "orch/x", "ctl/x", "qt/x", "jact/00", "widx/00"} {
		if st := put(ps.worker, k); st != http.StatusForbidden {
			t.Errorf("worker PUT on %s = %d, want 403 (only the orchestrator writes it)", k, st)
		}
		if st := put(ps.orch, k); st != http.StatusOK {
			t.Errorf("orchestrator PUT on %s = %d, want 200", k, st)
		}
	}
	for _, k := range []string{"job/x", "wreg/x", "fleet/cfg", "qt/x"} {
		if st := ps.worker.do("DELETE", "/"+fleet+"/"+k, nil, nil).status; st != http.StatusForbidden {
			t.Errorf("worker DELETE on %s = %d, want 403 (no delete in fleet)", k, st)
		}
		if st := ps.worker.do("GET", "/"+fleet+"/"+k, nil, nil).status; st != http.StatusOK {
			t.Errorf("worker GET on %s = %d, want 200 (read all)", k, st)
		}
	}
	if st := ps.worker.do("GET", "/"+fleet+"/job/does-not-exist", nil, nil).status; st != http.StatusNotFound {
		t.Errorf("worker GET of a missing fleet key = %d, want 404 not 403 (s3:ListBucket is granted for that)", st)
	}
	// bob-control: worker credentials are refused for GET, PUT and DELETE on any key.
	for _, k := range []string{"fleet-cfg", "user/x", "okey/x", "_bob/probe"} {
		for _, m := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			if st := ps.worker.do(m, "/"+control+"/"+k, nil, []byte("x")).status; st != http.StatusForbidden {
				t.Errorf("worker %s on bob-control/%s = %d, want 403", m, k, st)
			}
		}
	}
	// analytics: worker may write and delete; artifacts: write only below staging/.
	if st := ps.worker.do("PUT", "/"+analytics+"/log_line/w", nil, []byte("x")).status; st != http.StatusOK {
		t.Errorf("worker PUT on analytics = %d, want 200", st)
	}
	if st := ps.worker.do("PUT", "/"+artifacts+"/staging/b/j/1/x", nil, []byte("x")).status; st != http.StatusOK {
		t.Errorf("worker PUT on artifacts staging/ = %d, want 200", st)
	}
	for _, k := range []string{"nix/nar/x", "oci/x", "rel/p/0000000001/a", "src/p/x", "nix/narinfo/x"} {
		if st := ps.worker.do("PUT", "/"+artifacts+"/"+k, nil, []byte("x")).status; st != http.StatusForbidden {
			t.Errorf("worker PUT on artifacts %s = %d, want 403", k, st)
		}
	}
}

// TestRSTORE002GlobalOperations: the 8 global/admin operations. Both identities may list
// bucket names (no policy can restrict that); everything else is refused: bucket create and
// delete, STS AssumeRole, and the RustFS admin API.
func TestRSTORE002GlobalOperations(t *testing.T) {
	parity.Covers(t, "R-STORE-002")
	ps := startPolicyStack(t)
	if ps == nil {
		return
	}
	type gop struct {
		name           string
		method, path   string
		body           string
		allowed        bool
		refusedAnyNon2 bool // STS may answer 400 or 403; both refuse
	}
	gops := []gop{
		{name: "ListBuckets", method: "GET", path: "/", allowed: true},
		{name: "CreateBucket", method: "PUT", path: "/bob-harness-extra"},
		{name: "DeleteBucket (not ours)", method: "DELETE", path: "/bob-harness-other"},
		{name: "STS AssumeRole", method: "POST", path: "/?Action=AssumeRole&Version=2011-06-15&RoleSessionName=evil&DurationSeconds=900", refusedAnyNon2: true},
		{name: "admin list-users", method: "GET", path: "/rustfs/admin/v3/list-users"},
		{name: "admin info-canned-policy", method: "GET", path: "/rustfs/admin/v3/info-canned-policy?name=bob-worker"},
		{name: "admin add-user", method: "PUT", path: "/rustfs/admin/v3/add-user?accessKey=evil", body: `{"secretKey":"evilevilevil1234","status":"enabled"}`},
		{name: "admin list-canned-policies", method: "GET", path: "/rustfs/admin/v3/list-canned-policies"},
	}
	if len(gops) != 8 {
		t.Fatalf("%d global operations, want 8", len(gops))
	}
	for _, who := range []struct {
		name string
		c    *client
	}{{"worker", ps.worker}, {"orchestrator", ps.orch}} {
		for _, g := range gops {
			r := who.c.do(g.method, g.path, nil, []byte(g.body))
			switch {
			case g.allowed && !r.ok():
				t.Errorf("%s %s = %d, want 2xx", who.name, g.name, r.status)
			case !g.allowed && g.refusedAnyNon2 && r.ok():
				t.Errorf("%s %s succeeded (%d %.80s)", who.name, g.name, r.status, r.body)
			case !g.allowed && !g.refusedAnyNon2 && r.status != http.StatusForbidden:
				t.Errorf("%s %s = %d %.80s, want 403", who.name, g.name, r.status, r.body)
			}
		}
	}
	// Nothing was created by the refused calls.
	if r := ps.root.do("GET", "/", nil, nil); strings.Contains(r.body, "bob-harness-extra") {
		t.Errorf("a refused CreateBucket created the bucket")
	}
}

// TestMatrixMachineryWithRoot validates the matrix client itself (HARNESS.md §10 spirit): the
// harness root may do everything, so on a stack without any policy every one of the 162
// cells must come back 2xx. A cell that fails here is a bug in the probe, not in a policy.
func TestMatrixMachineryWithRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := stack.Start(ctx, stack.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	root := &client{t: t, base: s.Info.S3URL, creds: s3sig.Creds{AccessKey: s.Info.AccessKey, SecretKey: s.Info.SecretKey, Region: s.Info.Region}}
	ps := &policyStack{s: s, root: root, worker: root, orch: root}
	ps.seed(t)
	docs := ps.policies()
	p := &probe{c: root, policy: docs}
	for _, tg := range targets {
		for _, o := range ops {
			ps.seed(t)
			got := p.run(o.name, tg)
			ps.reset(tg.bucket, docs[tg.bucket])
			if !got.ok() {
				t.Errorf("root %s on %s = %d %.100s", o.name, tg.name, got.status, got.body)
			}
		}
	}
}
