package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// recordCase runs the R-STORE-103 table for one record type: create, CAS win, CAS loss (a
// stale ETag), lost response (the proxy drops the PUT response after the backend applied it),
// and decode of the stored CBOR (the record reads back to the same data).
func recordCase[T any](t *testing.T, ctx context.Context, be *env.Backend, b *store.Bucket, v *T, change func(*T)) {
	t.Helper()
	enc := func(x *T) []byte {
		out, err := cbor.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if err := store.Create(ctx, b, v); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Create(ctx, b, v); !errors.Is(err, store.ErrExists) {
		t.Fatalf("second create = %v, want ErrExists", err)
	}
	got, err := store.Get(ctx, b, v)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(enc(got), enc(v)) {
		t.Errorf("decode: got %+v, wrote %+v", got, v)
	}
	stale := *got
	won, err := store.CAS(ctx, b, v, func(cur *T) (*T, error) { change(cur); return cur, nil })
	if err != nil {
		t.Fatalf("cas: %v", err)
	}
	if err := store.Delete(ctx, b, &stale); !errors.Is(err, store.ErrConflict) {
		t.Errorf("delete with the ETag before the CAS = %v, want ErrConflict", err)
	}
	be.Proxy.SetScript(s3count.Script{Seed: 1, Faults: []s3count.Fault{{Kind: s3count.FaultLost, Op: s3count.OpPut, Bucket: be.Last, Limit: 1}}})
	lost, err := store.CAS(ctx, b, v, func(cur *T) (*T, error) { change(cur); return cur, nil })
	be.Proxy.SetScript(s3count.Script{})
	if err != nil {
		t.Fatalf("cas with a lost response: %v", err)
	}
	after, err := store.Get(ctx, b, v)
	if err != nil || !bytes.Equal(enc(after), enc(lost)) || bytes.Equal(enc(after), enc(won)) {
		t.Errorf("after the lost-response CAS the record is %+v (%v); want the change applied exactly once", after, err)
	}
}

// TestRecordTypes is the R-STORE-103 table over the §05.4 record types.
func TestRecordTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	b := be.Open(t, "fleet")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	t.Run("FleetConfig", func(t *testing.T) {
		v := store.FleetConfigKey()
		v.Gen = 1
		v.Classes = []store.FleetClass{{ID: "x86_64-linux.aaaaaaaaaa", Platform: "x86_64-linux"}}
		v.Params = store.FleetParams{Heartbeat: 15 * time.Second, JobShards: 16}
		recordCase(t, ctx, be, b, v, func(c *store.FleetConfig) { c.Gen++ })
	})
	t.Run("OrchRecord", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.OrchRecord{ID: "o1", Endpoint: "https://o1", Started: now},
			func(c *store.OrchRecord) { c.Heartbeat = c.Heartbeat.Add(time.Second) })
	})
	t.Run("WorkerRecord", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.WorkerRecord{Wid: "w1", State: "active", Serial: 1, Caps: []string{"kvm"}, TokenExp: now},
			func(c *store.WorkerRecord) { c.Serial++ })
	})
	t.Run("Heartbeat", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.Heartbeat{Wid: "w1", At: now, Jobs: []store.HeldJobEpoch{{Job: "j1", Epoch: 2}}},
			func(c *store.Heartbeat) { c.Free++ })
	})
	t.Run("QueueSlot", func(t *testing.T) {
		v := store.QueueSlotKey("x86_64-linux.aaaaaaaaaa", 7)
		v.Job, v.At = "j1", now
		recordCase(t, ctx, be, b, v, func(c *store.QueueSlot) { c.Taken += "w" })
	})
	t.Run("JobRecord", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.JobRecord{ID: "j1", Spec: []byte{1, 2}, State: "queued", Queued: now},
			func(c *store.JobRecord) { c.Epoch++; c.Owner = "w1"; c.State = "claimed" })
	})
	t.Run("BuildRecord", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.BuildRecord{ID: "b1", Project: "p1", Number: 1, State: "pending",
			Trigger: store.BuildTrigger{Kind: "push"}, Jobs: []store.BuildJob{{ID: "j1"}}, Created: now},
			func(c *store.BuildRecord) { c.Jobs[0].Attempts++ })
	})
	t.Run("ProjectCounters", func(t *testing.T) {
		recordCase(t, ctx, be, b, &store.ProjectCounters{Project: "p1"}, func(c *store.ProjectCounters) { c.Builds++ })
	})
}

func TestControlKeys(t *testing.T) {
	for _, c := range []struct {
		kind string
		args []string
	}{
		{"pname", []string{"my project"}},
		{"oidcsub", []string{"https://issuer", "sub"}},
		{"tok", []string{"bob_pat_x"}},
		{"relv", []string{"01JABCDEFGHJKMNPQRSTVWXYZ0", "a release name of sixty characters......................", "1.0.0"}},
		{"repo", []string{"01JABCDEFGHJKMNPQRSTVWXYZ0", "owner/repo"}},
		{"fdel", []string{"01JABCDEFGHJKMNPQRSTVWXYZ0", "delivery-1"}},
	} {
		k, err := store.ControlKey(c.kind, c.args...)
		if err != nil {
			t.Errorf("ControlKey(%s, %q) = %v", c.kind, c.args, err)
		}
		t.Logf("%s -> %s", c.kind, k)
	}
	if _, err := store.ControlKey("tok", ""); !errors.Is(err, store.ErrKeySegment) {
		t.Errorf("an empty token = %v, want ErrKeySegment", err)
	}
}
