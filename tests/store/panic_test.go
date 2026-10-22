package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRSTORE005CallRecoversMisusePanics feeds every misuse panic of S9 §3.6 to the recover
// boundary. Each must come back as store.ErrPanic and the process must keep running.
func TestRSTORE005CallRecoversMisusePanics(t *testing.T) {
	parity.Covers(t, "R-STORE-005")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var nilPtr *int
	cases := []struct {
		name string
		fn   func(context.Context) (int, error)
	}{
		{"filter operator in an update", func(context.Context) (int, error) { panic("dynamitedb: filter operator in Update") }},
		{"update operator in a Get filter", func(context.Context) (int, error) { panic("dynamitedb: update operator in filter") }},
		{"nil model", func(context.Context) (int, error) { panic(errors.New("dynamitedb: nil model")) }},
		{"non-struct T", func(context.Context) (int, error) { panic("dynamitedb: model must be a struct") }},
		{"Value() on an operator", func(context.Context) (int, error) { panic("dynamitedb: Value() called on operator") }},
		{"nil pointer (missing ETag header)", func(context.Context) (int, error) { return *nilPtr, nil }},
		{"panic with a non-string, non-error value", func(context.Context) (int, error) { panic(42) }},
	}
	for _, c := range cases {
		got, err := store.Call(ctx, c.fn)
		if env.Red(t, err) {
			return
		}
		if !errors.Is(err, store.ErrPanic) || got != 0 {
			t.Errorf("%s: Call = %d, %v; want ErrPanic", c.name, got, err)
		}
	}
	// A panic in a goroutine started by the wrapped call cannot be recovered by Call itself
	// (S9: dynamitedb.Query crashes the process this way); that is why bob never calls Query.
	// What Call does promise: errors and values pass through unchanged.
	sentinel := errors.New("backend said no")
	if _, err := store.Call(ctx, func(context.Context) (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Call changed an ordinary error: %v", err)
	}
	if v, err := store.Call(ctx, func(context.Context) (int, error) { return 7, nil }); err != nil || v != 7 {
		t.Errorf("Call = %d, %v; want 7, nil", v, err)
	}
}

// TestRSTORE005PrimitivesReturnErrPanic misuses the primitives themselves: a nil model, a
// non-struct record, and a backend that omits the ETag header. The process keeps running and
// the bucket stays usable afterwards.
func TestRSTORE005PrimitivesReturnErrPanic(t *testing.T) {
	parity.Covers(t, "R-STORE-005")
	be := env.Start(t)
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if _, err := store.Get[rec](ctx, b, nil); !errors.Is(err, store.ErrPanic) {
		t.Errorf("Get with a nil model: %v, want ErrPanic", err)
	}
	if _, err := store.Get[int](ctx, b, new(int)); !errors.Is(err, store.ErrPanic) {
		t.Errorf("Get with a non-struct T: %v, want ErrPanic", err)
	}
	if err := store.Create[rec](ctx, b, nil); !errors.Is(err, store.ErrPanic) {
		t.Errorf("Create with a nil model: %v, want ErrPanic", err)
	}

	seed := &rec{ID: store.ID("p1"), N: store.Set(1)}
	if err := store.Create(ctx, b, seed); err != nil {
		t.Fatalf("Create: %v", err)
	}
	be.Proxy.SetScript(s3count.Script{Seed: 5, Faults: []s3count.Fault{{Kind: s3count.FaultStripETag, Op: s3count.OpGet, Bucket: be.Last}}})
	if _, err := store.Get(ctx, b, &rec{ID: store.ID("p1")}); !errors.Is(err, store.ErrPanic) {
		t.Errorf("Get from a backend without ETag header: %v, want ErrPanic", err)
	}
	be.Proxy.SetScript(s3count.Script{})
	got, err := store.Get(ctx, b, &rec{ID: store.ID("p1")})
	if err != nil || got.N.Value() != 1 || store.ETagOf(got.ETag) == "" {
		t.Errorf("Get after the panic = %+v, %v; the bucket must stay usable and carry the ETag", got, err)
	}
}
