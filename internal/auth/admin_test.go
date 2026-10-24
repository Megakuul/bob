package auth

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/store/env"
)

// restoreAdmin undoes adminRemove when the following write failed (F4). The request context is
// often what failed; the undo must still land, and a failed undo must not be silent.
func TestRestoreAdmin(t *testing.T) {
	be := env.Start(t)
	control := be.Open(t, "control")
	if control == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var logs bytes.Buffer
	id := &Identity{control: control, log: slog.New(slog.NewTextHandler(&logs, nil))}
	for _, p := range []string{"user:a", "user:b"} {
		if err := id.adminAdd(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := id.adminRemove(ctx, "user:a"); err != nil {
		t.Fatal(err)
	}
	gone, stop := context.WithCancel(ctx)
	stop()
	if err := id.adminAdd(gone, "user:a"); err == nil {
		t.Fatal("precondition: adminAdd on a cancelled context succeeded")
	}
	id.restoreAdmin(gone, "user:a")
	idx, err := store.Get(ctx, control, store.AdminIndexKey())
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Principals.Value(); !slices.Equal(got, []string{"user:a", "user:b"}) {
		t.Errorf("admin index after restore = %v, want [user:a user:b]", got)
	}
	if logs.Len() != 0 {
		t.Errorf("successful restore logged: %s", logs.String())
	}

	broken := &Identity{log: id.log} // no bucket: every store call fails
	broken.restoreAdmin(ctx, "user:c")
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "admin index not restored") || !strings.Contains(out, "principal=user:c") {
		t.Errorf("failed restore not logged as an error: %q", out)
	}
}
