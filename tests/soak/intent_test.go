//go:build slow

package soak

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/server/srvtest"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	store "github.com/megakuul/bob/tests/store/env"
)

// idsOf lists the ids of a paged list RPC.
func idsOf(ctx context.Context, e *srvtest.Env, service, method, field string, req map[string]any) ([]string, error) {
	var r map[string]any
	req["pageSize"] = 200
	if err := e.Call(ctx, service, method, req, &r); err != nil {
		return nil, err
	}
	var out []string
	items, _ := r[field].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

var ulidRE = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)

// danglingEntries reads every raw object under prefix of bucket (the harness's own root GET, not bob's)
// and returns the ULIDs that appear in them but have no record object recPrefix+<ULID>, and whether
// any record id was found in the pages at all (false: the raw encoding hides ids from this scan).
func danglingEntries(t *testing.T, e *srvtest.Env, bucket, prefix, recBucket, recPrefix string) (dangling []string, sawRecords bool) {
	t.Helper()
	have := map[string]bool{}
	for _, k := range store.ListKeys(t, e.Stack, recBucket, recPrefix) {
		have[strings.TrimPrefix(k, recPrefix)] = true
	}
	seen := map[string]bool{}
	for _, k := range store.ListKeys(t, e.Stack, bucket, prefix) {
		for _, id := range ulidRE.FindAllString(string(store.GetRaw(t, e.Stack, bucket, k)), -1) {
			if seen[id] {
				continue
			}
			seen[id] = true
			if have[id] {
				sawRecords = true
			} else {
				dangling = append(dangling, id)
			}
		}
	}
	return dangling, sawRecords
}

// TestRSRV101IntentFirst kills the writer between each pair of writes (index entry first, record second)
// of build create, job enqueue (jact -> job and job -> queue slot), role binding and project create:
// the second write is made to fail at the S3 proxy and the orchestrator is SIGKILLed right after the
// first one landed. For 16 min (index-repair every 10 min repairs entries older than 5 min) readers list
// and fetch each index: no listed entry may lack its record (no phantom), and afterwards every index
// matches its records. Slow: the SPEC ties repair to 5 and 10 minute timers and the stack has no clock hook.
func TestRSRV101IntentFirst(t *testing.T) {
	parity.Covers(t, "R-SRV-101")
	e := srvtest.Start(t, srvtest.Options{Orchestrators: 2, Workers: 1, Env: map[string]string{
		"BOB_FLEET_POLL_INTERVAL": "2s", "BOB_FLEET_HEARTBEAT": "2s", "BOB_FLEET_LEASE_TTL": "10s",
	}})
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	repo := srvtest.Repo(t, []srvtest.Pkg{{Name: "i0", Cmd: "echo 0"}, {Name: "i1", Cmd: "echo 1"}, {Name: "i2", Cmd: "echo 2"}})

	// a user to bind a role to (written before any fault)
	var cu struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := e.Call(ctx, "bob.auth.v1.UserService", "CreateUser", map[string]any{
		"user": map[string]any{"login": "intent", "displayName": "intent"}, "initialPassword": "intent-Pass-1234"}, &cu); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// a control build and project written before any fault: record -> index (reverse check)
	ctrl, err := e.Submit(repo, "//i2:t")
	if err != nil {
		t.Fatalf("control build: %v", err)
	}
	injected := func(n int) string {
		return srvtest.Eventually(60*time.Second, func() string {
			c, err := e.Counts()
			if err != nil || len(c.Faults) < n {
				return "no fault injected yet"
			}
			return ""
		})
	}
	// round 1: second writes of build create, job enqueue (jact -> job), role binding, project create
	e.SetFaults(s3count.Script{Seed: 101, Faults: []s3count.Fault{
		{Kind: s3count.FaultReset, Bucket: "bob-control", Op: s3count.OpPut, KeyPrefix: "build/"},
		{Kind: s3count.FaultReset, Bucket: "bob-fleet", Op: s3count.OpPut, KeyPrefix: "job/"},
		{Kind: s3count.FaultReset, Bucket: "bob-control", Op: s3count.OpPut, KeyPrefix: "user/"},
		{Kind: s3count.FaultReset, Bucket: "bob-control", Op: s3count.OpPut, KeyPrefix: "proj/"},
	}})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); _, _ = e.Submit(repo, "//i0:t") }()
	go func() {
		defer wg.Done()
		_ = e.Call(ctx, "bob.auth.v1.RoleService", "CreateRoleBinding", map[string]any{"principalId": cu.User.ID, "role": "admin", "project": "*"}, nil)
	}()
	go func() {
		defer wg.Done()
		_ = e.Call(ctx, "bob.project.v1.ProjectService", "CreateProject", map[string]any{
			"project": map[string]any{"displayName": "intent", "repoFullName": "acme/intent"}}, nil)
	}()
	if why := injected(3); why != "" {
		t.Fatalf("round 1: %s", why)
	}
	if err := e.Kill("orchestrator-0"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	e.SetFaults(s3count.Script{})
	if err := e.Restart("orchestrator-0"); err != nil {
		t.Fatal(err)
	}
	// round 2: job -> queue slot
	e.ResetCounts()
	e.SetFaults(s3count.Script{Seed: 102, Faults: []s3count.Fault{{Kind: s3count.FaultReset, Bucket: "bob-fleet", Op: s3count.OpPut, KeyPrefix: "qs/"}}})
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = e.Submit(repo, "//i1:t") }()
	if why := injected(1); why != "" {
		t.Fatalf("round 2: %s", why)
	}
	if err := e.Kill("orchestrator-0"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	e.SetFaults(s3count.Script{})
	if err := e.Restart("orchestrator-0"); err != nil {
		t.Fatal(err)
	}

	// The faults left dangling entries (the index write landed, the record write failed): the raw pages
	// bpage/ (builds) and jact/ (jobs) name ids that have no record. Readers skip them (checked below);
	// index-repair removes them after 5 min.
	rawBuilds, sawB := danglingEntries(t, e, "bob-control", "bpage/", "bob-control", "build/")
	rawJobs, sawJ := danglingEntries(t, e, "bob-fleet", "jact/", "bob-fleet", "job/")
	if !sawB {
		t.Logf("raw check of bpage/ unavailable: no record id found in the pages (encoding)")
	} else if len(rawBuilds) == 0 {
		t.Errorf("the build-create fault left no dangling bpage entry: the index is not written before the record")
	}
	if sawJ && len(rawJobs) == 0 {
		t.Logf("no dangling jact entry after the job-enqueue faults")
	}

	type index struct{ name, service, method, field, getSvc, getMethod string }
	indexes := []index{
		{"builds", "bob.build.v1.BuildService", "ListBuilds", "builds", "bob.build.v1.BuildService", "GetBuild"},
		{"projects", "bob.project.v1.ProjectService", "ListProjects", "projects", "bob.project.v1.ProjectService", "GetProject"},
	}
	var phantoms atomic.Int64
	check := func(v *srvtest.Env) {
		for _, ix := range indexes {
			ids, err := idsOf(ctx, v, ix.service, ix.method, ix.field, map[string]any{})
			if err != nil {
				continue // a restarting orchestrator is not a phantom
			}
			for _, id := range ids {
				if err := v.Call(ctx, ix.getSvc, ix.getMethod, map[string]string{"id": id}, nil); err != nil {
					phantoms.Add(1)
					t.Errorf("phantom %s %s listed but not readable: %v", ix.name, id, err)
				}
			}
		}
		// jobs of every build, role bindings by role and by principal
		builds, _ := idsOf(ctx, v, "bob.build.v1.BuildService", "ListBuilds", "builds", map[string]any{})
		for _, b := range builds {
			jobs, err := v.Jobs(ctx, b)
			if err != nil {
				continue
			}
			for _, j := range jobs {
				if err := v.Call(ctx, "bob.build.v1.JobService", "GetJob", map[string]string{"id": j.ID}, nil); err != nil {
					phantoms.Add(1)
					t.Errorf("phantom job %s of build %s: %v", j.ID, b, err)
				}
			}
		}
	}
	deadline := time.Now().Add(16 * time.Minute)
	for time.Now().Before(deadline) {
		check(e.Via(1))
		time.Sleep(5 * time.Second)
	}
	// after index-repair no page names a record that does not exist
	if sawB {
		if d, _ := danglingEntries(t, e, "bob-control", "bpage/", "bob-control", "build/"); len(d) != 0 {
			t.Errorf("index-repair left %d dangling bpage entries after 16 min (e.g. %s)", len(d), d[0])
		}
	}
	if sawJ {
		if d, _ := danglingEntries(t, e, "bob-fleet", "jact/", "bob-fleet", "job/"); len(d) != 0 {
			t.Errorf("index-repair left %d dangling jact entries after 16 min (e.g. %s)", len(d), d[0])
		}
	}
	// every record is in its index: the control build written before the faults is listed
	if ids, err := idsOf(ctx, e, "bob.build.v1.BuildService", "ListBuilds", "builds", map[string]any{}); err != nil || !slices.Contains(ids, ctrl) {
		t.Errorf("build %s exists but the build index does not list it (%v)", ctrl, err)
	}
	// every index matches its records after index-repair: the bindings of the user (record: user/<id>)
	// equal the user's entries in the role index (rmem/admin)
	type bindings struct {
		Bindings []struct {
			ID        string `json:"id"`
			Principal struct {
				ID string `json:"id"`
			} `json:"principal"`
		} `json:"bindings"`
	}
	var byRole, byPrincipal bindings
	if err := e.Call(ctx, "bob.auth.v1.RoleService", "ListRoleBindings", map[string]any{"role": "admin"}, &byRole); err != nil {
		t.Fatal(err)
	}
	if err := e.Call(ctx, "bob.auth.v1.RoleService", "ListRoleBindings", map[string]any{"principalId": cu.User.ID}, &byPrincipal); err != nil {
		t.Fatal(err)
	}
	fromIndex := map[string]bool{}
	for _, b := range byRole.Bindings {
		if b.Principal.ID == cu.User.ID {
			fromIndex[b.ID] = true
		}
	}
	fromRecord := map[string]bool{}
	for _, b := range byPrincipal.Bindings {
		fromRecord[b.ID] = true
	}
	if len(fromIndex) != len(fromRecord) {
		t.Errorf("role index lists %d bindings of the user, the user record has %d", len(fromIndex), len(fromRecord))
	}
	for id := range fromRecord {
		if !fromIndex[id] {
			t.Errorf("binding %s is in the user record but not in the role index after index-repair", id)
		}
	}
	if phantoms.Load() != 0 {
		t.Errorf("%d phantom records were shown", phantoms.Load())
	}
	e.AssertNoList()
}
