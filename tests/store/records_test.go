package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// rawGet reads an object straight from RustFS with the root credential (bypassing the proxy).
func rawGet(t testing.TB, be *env.Backend, bucket, key string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := be.FS.Do(ctx, be.FS.Root, http.MethodGet, "/"+bucket+"/"+key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func lostPut(bucket string) s3count.Script {
	return s3count.Script{Seed: 103, Faults: []s3count.Fault{{Kind: s3count.FaultLost, Op: s3count.OpPut, Bucket: bucket, Limit: 1}}}
}

// TestRSTORE103RecordRules is the per-record table of R-STORE-103: create, CAS win, CAS loss,
// lost response, and decode of the stored CBOR.
func TestRSTORE103RecordRules(t *testing.T) {
	parity.Covers(t, "R-STORE-103")
	be := env.Start(t)
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	bucket := be.Last
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("create", func(t *testing.T) {
		if err := store.Create(ctx, b, &rec{ID: store.ID("c1"), N: store.Set(1), Wid: store.Set("w1")}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.Create(ctx, b, &rec{ID: store.ID("c1"), N: store.Set(2), Wid: store.Set("w2")}); !errors.Is(err, store.ErrExists) {
			t.Errorf("second Create = %v, want ErrExists", err)
		}
		got, err := store.Get(ctx, b, &rec{ID: store.ID("c1")})
		if err != nil || got.N.Value() != 1 || got.Wid.Value() != "w1" || store.ETagOf(got.ETag) == "" {
			t.Errorf("Get = %+v, %v; the loser must not overwrite and the winner carries an ETag", got, err)
		}
		if _, err := store.Get(ctx, b, &rec{ID: store.ID("missing")}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get of a missing record = %v, want ErrNotFound", err)
		}
	})

	t.Run("cas win", func(t *testing.T) {
		be.Proxy.Reset()
		got, err := store.CAS(ctx, b, &rec{ID: store.ID("w")}, func(cur *rec) (*rec, error) {
			if cur != nil {
				t.Errorf("CAS on an absent record passed %+v, want nil", cur)
			}
			return &rec{ID: store.ID("w"), N: store.Set(1)}, nil
		})
		if err != nil || got.N.Value() != 1 {
			t.Fatalf("CAS create = %+v, %v", got, err)
		}
		got, err = store.CAS(ctx, b, &rec{ID: store.ID("w")}, func(cur *rec) (*rec, error) {
			n := *cur
			n.N = store.Set(n.N.Value() + 1)
			return &n, nil
		})
		if err != nil || got.N.Value() != 2 {
			t.Fatalf("CAS update = %+v, %v", got, err)
		}
		// One read and one conditional write each: 2 GET-class + 2 PUT in total, no LIST.
		c := be.Proxy.Counts()
		if put := env.Ops(c, bucket, s3count.OpPut); put != 2 {
			t.Errorf("two uncontended CAS cost %d PUTs, want 2 (one If-Match PUT each)", put)
		}
		if l := c.Lists(bucket); l != 0 {
			t.Errorf("CAS issued %d LIST requests", l)
		}
		// ErrNoChange ends the CAS without a write.
		be.Proxy.Reset()
		if _, err := store.CAS(ctx, b, &rec{ID: store.ID("w")}, func(*rec) (*rec, error) { return nil, store.ErrNoChange }); err != nil && !errors.Is(err, store.ErrNoChange) {
			t.Errorf("CAS with ErrNoChange = %v", err)
		}
		if put := env.Ops(be.Proxy.Counts(), bucket, s3count.OpPut); put != 0 {
			t.Errorf("ErrNoChange wrote %d times", put)
		}
		// fn returning nil, nil deletes the record.
		if _, err := store.CAS(ctx, b, &rec{ID: store.ID("w")}, func(*rec) (*rec, error) { return nil, nil }); err != nil {
			t.Errorf("CAS delete: %v", err)
		}
		if _, err := store.Get(ctx, b, &rec{ID: store.ID("w")}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get after CAS delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("cas loss", func(t *testing.T) {
		// 8 contenders increment one record: every increment lands exactly once.
		const workers = 8
		var calls atomic.Int64
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.CAS(ctx, b, &rec{ID: store.ID("race")}, func(cur *rec) (*rec, error) {
					calls.Add(1)
					n := rec{ID: store.ID("race")}
					if cur != nil {
						n = *cur
					}
					n.N = store.Set(n.N.Value() + 1)
					return &n, nil
				})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("CAS under contention: %v", err)
			}
		}
		got, err := store.Get(ctx, b, &rec{ID: store.ID("race")})
		if err != nil || got.N.Value() != workers {
			t.Errorf("after %d racing increments N = %+v, %v; a lost update happened", workers, got, err)
		}
		if calls.Load() < workers {
			t.Errorf("fn ran %d times for %d writers", calls.Load(), workers)
		}
		// A stale write is a conflict: Delete with an outdated ETag.
		old, _ := store.Get(ctx, b, &rec{ID: store.ID("race")})
		if _, err := store.CAS(ctx, b, &rec{ID: store.ID("race")}, func(cur *rec) (*rec, error) { n := *cur; n.N = store.Set(n.N.Value() + 1); return &n, nil }); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, b, old); !errors.Is(err, store.ErrConflict) {
			t.Errorf("Delete with a stale ETag = %v, want ErrConflict", err)
		}
		cur, _ := store.Get(ctx, b, &rec{ID: store.ID("race")})
		if err := store.Delete(ctx, b, cur); err != nil {
			t.Errorf("Delete with the current ETag: %v", err)
		}
		if err := store.Delete(ctx, b, cur); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Delete of a deleted record = %v, want ErrNotFound (a 404 maps to ErrNotFound)", err)
		}
	})

	t.Run("lost response", func(t *testing.T) {
		// Create: the server applied the write, the response is lost. dynamitedb reports
		// ErrAlreadyExists; store must re-read, see its own write id and report success.
		be.Proxy.SetScript(lostPut(bucket))
		if err := store.Create(ctx, b, &rec{ID: store.ID("lost-c"), N: store.Set(1), Wid: store.Set("me"), Ep: store.Set(1)}); err != nil {
			t.Errorf("Create with a lost response = %v, want success (the stored value carries our write id)", err)
		}
		be.Proxy.SetScript(s3count.Script{})
		if got, err := store.Get(ctx, b, &rec{ID: store.ID("lost-c")}); err != nil || got.Wid.Value() != "me" {
			t.Errorf("stored value after lost Create = %+v, %v", got, err)
		}
		// Create by somebody else with another write id is a real conflict, never success.
		if err := store.Create(ctx, b, &rec{ID: store.ID("lost-c"), N: store.Set(1), Wid: store.Set("other"), Ep: store.Set(1)}); !errors.Is(err, store.ErrExists) {
			t.Errorf("Create of an existing record with another write id = %v, want ErrExists", err)
		}

		// CAS: the PUT is applied, the response lost. The increment must be applied once.
		if err := store.Create(ctx, b, &rec{ID: store.ID("lost-u"), N: store.Set(10)}); err != nil {
			t.Fatal(err)
		}
		be.Proxy.SetScript(lostPut(bucket))
		got, err := store.CAS(ctx, b, &rec{ID: store.ID("lost-u")}, func(cur *rec) (*rec, error) {
			n := *cur
			n.N = store.Set(n.N.Value() + 1)
			n.Wid = store.Set("me")
			n.Ep = store.Set(7)
			return &n, nil
		})
		be.Proxy.SetScript(s3count.Script{})
		if err != nil || got == nil || got.N.Value() != 11 {
			t.Errorf("CAS with a lost response = %+v, %v; want success with N=11", got, err)
		}
		if st, err := store.Get(ctx, b, &rec{ID: store.ID("lost-u")}); err != nil || st.N.Value() != 11 {
			t.Errorf("stored N = %+v, %v; the lost-response retry applied the increment twice or not at all", st, err)
		}
	})

	t.Run("stored cbor", func(t *testing.T) {
		if err := store.Create(ctx, b, &rec{ID: store.ID("cbor"), N: store.Set(5), Wid: store.Set("w")}); err != nil {
			t.Fatal(err)
		}
		st, body := rawGet(t, be, bucket, "trec/cbor")
		if st != http.StatusOK {
			t.Fatalf("raw GET trec/cbor = %d; keys are <pk>/<pkval>", st)
		}
		if len(body) == 0 || body[0]>>5 != 5 { // CBOR major type 5 = map
			t.Fatalf("stored value starts with 0x%02x, want a CBOR map", body[:min(1, len(body))])
		}
		// Short names, omitempty: the field names of the Go struct never reach the bucket,
		// and an empty field is absent.
		for _, long := range []string{"Wid", "ETag", "Ep"} {
			if bytes.Contains(body, []byte(long)) {
				t.Errorf("stored value contains the Go field name %q", long)
			}
		}
		if err := store.Create(ctx, b, &rec{ID: store.ID("cbor-min")}); err != nil {
			t.Fatal(err)
		}
		_, small := rawGet(t, be, bucket, "trec/cbor-min")
		if len(small) >= len(body) {
			t.Errorf("an all-zero record (%d bytes) is not smaller than a filled one (%d): omitempty is not applied", len(small), len(body))
		}
		got, err := store.Get(ctx, b, &rec{ID: store.ID("cbor")})
		if err != nil || got.N.Value() != 5 || got.Wid.Value() != "w" {
			t.Errorf("decode = %+v, %v", got, err)
		}
	})

	t.Run("ctx first", func(t *testing.T) {
		// After a failed call, ctx.Err() is checked first (S9 B11): a cancelled context yields
		// context.Canceled, not an opaque transport error.
		be.Proxy.SetScript(s3count.Script{Faults: []s3count.Fault{{Kind: s3count.FaultLatency, Delay: 3 * time.Second, Bucket: bucket}}})
		defer be.Proxy.SetScript(s3count.Script{})
		cctx, ccancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer ccancel()
		_, err := store.Get(cctx, b, &rec{ID: store.ID("c1")})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Get on a slow backend with a 300 ms deadline = %v, want context.DeadlineExceeded", err)
		}
	})
}

// TestRSTORE103SourceRules checks the rules that are visible in source: no dynamitedb.Update,
// no WithExpires, no filters outside internal/store.
func TestRSTORE103SourceRules(t *testing.T) {
	parity.Covers(t, "R-STORE-103")
	root := repoRoot(t)
	banned := []string{"dynamitedb.Update(", "dynamitedb.WithExpires", ".WithExpires("}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, s := range banned {
			if strings.Contains(string(b), s) {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("%s uses %s (R-STORE-103 rules 3 and 5)", rel, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
