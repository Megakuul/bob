package rbac

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// TestResolver resolves ids against RustFS: one GET per lookup; unknown or malformed ids are
// NotFound "<what> not found", the same answer as a project without permission.
func TestResolver(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control, fleet := be.Open(t, "control"), be.Open(t, "fleet")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := store.Create(ctx, control, &store.BuildRecord{ID: "01jabcdefghjkmnpqrstvwxyz0", Project: "p1", Number: 1}); err != nil {
		t.Fatal(err)
	}
	spec, err := cbor.Marshal(store.JobSpec{V: 1, JobID: "01jjob00000000000000000000", Project: "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, fleet, &store.JobRecord{ID: "01jjob00000000000000000000", Spec: spec, State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, control, &releaseHead{Project: "p3", N: "0000000007"}); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Control: control, Fleet: fleet}
	for _, c := range []struct{ kind, id, want string }{
		{"build", "01jabcdefghjkmnpqrstvwxyz0", "p1"},
		{"job", "01jjob00000000000000000000", "p2"},
		{"release", "p3.0000000007", "p3"},
		{"repository", "p4/api", "p4"},
	} {
		got, err := r.Project(ctx, c.kind, c.id)
		if err != nil || got != c.want {
			t.Errorf("%s %s: got %q, %v; want %s", c.kind, c.id, got, err, c.want)
		}
	}
	for _, c := range []struct{ kind, id string }{
		{"build", "01jmissing0000000000000000"}, {"build", "../x"}, {"build", "a/b"},
		{"job", "01jmissing0000000000000000"},
		{"release", "p3.0000000008"}, {"release", "p3.7"}, {"release", "nodot"},
		{"repository", "noslash"}, {"repository", "*/x"}, {"repository", "p4/"},
	} {
		_, err := r.Project(ctx, c.kind, c.id)
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeNotFound || ce.Message() != c.kind+" not found" {
			t.Errorf("%s %q: want NotFound %q, got %v", c.kind, c.id, c.kind+" not found", err)
		}
	}
	// roles: a deleted role stops granting on the next call (conditional GET, no stale cache)
	roles := NewRoles(control)
	rec := &store.RoleRecord{ID: "dev", Permissions: []Permission{p(getBuild, "*")}}
	if err := store.Create(ctx, control, rec); err != nil {
		t.Fatal(err)
	}
	if ps, ok, err := roles.Permissions(ctx, "dev"); err != nil || !ok || len(ps) != 1 {
		t.Fatalf("role dev: %v %v %v", ps, ok, err)
	}
	if _, err := store.CAS(ctx, control, store.RoleKey("dev"), func(*store.RoleRecord) (*store.RoleRecord, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := roles.Permissions(ctx, "dev"); err != nil || ok {
		t.Fatalf("deleted role dev still found (%v, %v)", ok, err)
	}
}
