package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/tests/store/env"
)

// gcHash is a valid store hash (32 nix32 characters) for a test path.
func gcHash(c byte) string { return strings.Repeat(string(c), 32) }

func gcPath(h string) string { return cache.StoreDir + "/" + h + "-p" }

// TestCacheGC: the cache GC of the retention controller (R-STORE-007, R-STORE-150) with
// retention.cache_unreferenced_days = 0 (explicit, from the settings): an unreferenced path
// goes (narinfo, listing, NAR, visibility document); released paths, their references, paths
// with a public release in their visibility document and the references of a recent
// promotion stay; an entry older than its path's narinfo (collected and promoted again) only
// drops the entry. After the release is deleted, the next run collects its paths.
func TestCacheGC(t *testing.T) {
	be := env.Start(t)
	control := be.Open(t, "control")
	if control == nil {
		return
	}
	ab := be.Bucket(t, "artifacts")
	ctx := context.Background()
	obj, err := store.OpenObjects(ctx, s3client.Options{Endpoint: ab.Endpoint, Region: ab.Region}, ab.Name)
	if err != nil {
		t.Fatal(err)
	}
	real := time.Now().UTC()
	gcNow := real.Add(2 * time.Hour) // the GC orchestrator's clock
	clock := gcNow
	vis := cache.NewVisStore(obj, cache.NixVisPrefix, func() time.Time { return clock })

	A, B, C, D, E, F, G := gcHash('a'), gcHash('b'), gcHash('c'), gcHash('d'), gcHash('f'), gcHash('g'), gcHash('h')
	refs := map[string][]string{B: {C}, E: {F}}
	public := map[string]bool{D: true}
	for _, h := range []string{A, B, C, D, E, F, G} {
		var rs []string
		for _, r := range refs[h] {
			rs = append(rs, gcPath(r))
		}
		ni := &cache.NarInfo{StorePath: gcPath(h), URL: "nar/" + h + ".nar.zst", Compression: "zstd",
			NarHash: "sha256:" + strings.Repeat("0", 52), NarSize: 1, References: rs}
		pub := "[]"
		if public[h] {
			pub = `["p1.0000000001"]`
		}
		for k, body := range map[string]string{cache.NarInfoKey(h): string(ni.Format()), cache.NarKey(h): "nar", cache.ListingKey(h): "{}",
			cache.NixVisPrefix + h + ".json": `{"v":1,"projects":["p1"],"public":` + pub + `}`} {
			if _, err := obj.Put(ctx, k, []byte(body), "application/octet-stream", store.Cond{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	promo := func(h string, at time.Time) {
		t.Helper()
		if _, err := store.PromoLog(control, at).Append(ctx, &store.PromoEntry{StoreHash: store.Set(h), Project: store.Set[string]("p1"),
			Build: store.Set[string]("b1"), At: store.Set(at)}); err != nil {
			t.Fatal(err)
		}
	}
	old := real.Add(-time.Minute) // promoted just before the objects were written
	for _, h := range []string{A, B, C, D, F} {
		promo(h, old)
	}
	promo(E, gcNow.Add(-time.Minute)) // recent: younger than the grace, never judged; F is in its closure
	promo(G, real.Add(-3*time.Hour))  // an older entry of a path whose narinfo is newer
	promo(A, real.Add(-48*time.Hour)) // a closed day before yesterday: read once, then skipped
	released := map[string]bool{B: true}
	gc := &orchestrator.CacheGC{Control: control, Objects: obj, Vis: vis, Now: func() time.Time { return clock },
		Retained: func(context.Context) (map[string]bool, error) { return released, nil }}
	ret := &orchestrator.Retention{Cache: 90 * 24 * time.Hour, CacheGC: gc,
		CacheSetting: func(context.Context) (time.Duration, bool) { return 0, true }}

	served := func(h string) bool {
		_, err := obj.Head(ctx, cache.NarInfoKey(h))
		if err != nil && !errors.Is(err, store.ErrObjectNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	gone := func(h string) bool {
		for _, k := range []string{cache.NarInfoKey(h), cache.NarKey(h), cache.ListingKey(h), cache.NixVisPrefix + h + ".json"} {
			if _, err := obj.Head(ctx, k); !errors.Is(err, store.ErrObjectNotFound) {
				return false
			}
		}
		return true
	}

	r := &orchestrator.Run{Now: gcNow}
	res, err := ret.Collect(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.CachePaths != 1 || !gone(A) {
		t.Fatalf("first run removed %d paths (A gone %v), want 1: the unreferenced path A", res.CachePaths, gone(A))
	}
	for name, h := range map[string]string{"released B": B, "B's reference C": C, "public D": D, "recent E": E, "E's reference F": F, "re-promoted G": G} {
		if !served(h) {
			t.Errorf("first run collected %s", name)
		}
	}
	var st struct {
		C struct {
			From string            `cbor:"f"`
			Seq  map[string]uint64 `cbor:"s"`
		} `cbor:"c"`
	}
	if err := cbor.Unmarshal(r.State, &st); err != nil {
		t.Fatal(err)
	}
	if want := gcNow.AddDate(0, 0, -1).Format("20060102"); st.C.From != want {
		t.Errorf("walk position %q, want %s: the days before yesterday are closed, yesterday stays open", st.C.From, want)
	}

	// A second run at once judges nothing new; then the release is deleted and D's public
	// release removed: an hour later everything but G (its entry is gone) is collected.
	if res, err := ret.Collect(ctx, &orchestrator.Run{Now: gcNow, State: r.State}); err != nil || res.CachePaths != 0 {
		t.Fatalf("immediate second run removed %d (%v), want 0", res.CachePaths, err)
	}
	released = map[string]bool{}
	if _, err := obj.Put(ctx, cache.NixVisPrefix+D+".json", []byte(`{"v":1,"projects":["p1"],"public":[]}`), "application/json", store.Cond{}); err != nil {
		t.Fatal(err)
	}
	clock = gcNow.Add(time.Hour)
	r2 := &orchestrator.Run{Now: clock, State: r.State}
	res, err = ret.Collect(ctx, r2)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]string{"B": B, "C": C, "D": D, "E": E, "F": F} {
		if !gone(h) {
			t.Errorf("after the release was deleted, %s was not collected", name)
		}
	}
	if !served(G) || res.CachePaths != 5 {
		t.Errorf("second run removed %d paths, G served %v; want 5 and G kept", res.CachePaths, served(G))
	}

	// A retention of 90 d (the setting unset) collects nothing young and runs once a day.
	ret.CacheSetting = func(context.Context) (time.Duration, bool) { return 0, false }
	promo(A, clock.Add(-time.Hour))
	if _, err := obj.Put(ctx, cache.NarInfoKey(A), []byte("StorePath: "+gcPath(A)+"\nURL: x\nCompression: zstd\nNarHash: x\nNarSize: 1\nReferences: \n"), "text/x-nix-narinfo", store.Cond{}); err != nil {
		t.Fatal(err)
	}
	if res, err := ret.Collect(ctx, &orchestrator.Run{Now: clock, State: r2.State}); err != nil || res.CachePaths != 0 || !served(A) {
		t.Errorf("90 d retention removed %d paths (%v), A served %v; want none", res.CachePaths, err, served(A))
	}
}
