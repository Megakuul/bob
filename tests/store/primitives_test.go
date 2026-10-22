package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/store/env"
)

// TestRSTORE106SeqLog: 20 concurrent appenders x 50 give 1,000 dense unique slots, no lost write.
func TestRSTORE106SeqLog(t *testing.T) {
	parity.Covers(t, "R-STORE-106")
	be := env.Start(t)
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const writers, each = 20, 50
	q := store.NewSeqLog[slot](b, "tqs", "tqt", "tqh", "x86_64-linux.k3j9d0a1b2")

	var mu sync.Mutex
	got := map[uint64]slot{}
	var wg sync.WaitGroup
	errc := make(chan error, writers)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				// Each appender uses its own handle: the log is a value type that performs no I/O.
				l := store.NewSeqLog[slot](b, "tqs", "tqt", "tqh", "x86_64-linux.k3j9d0a1b2")
				seq, err := l.Append(ctx, &slot{ID: fmt.Sprintf("a%d-%d", w, i), By: int64(w), Ord: int64(i)})
				if err != nil {
					errc <- fmt.Errorf("writer %d append %d: %w", w, i, err)
					return
				}
				mu.Lock()
				if prev, dup := got[seq]; dup {
					errc <- fmt.Errorf("slot %d returned twice (writers %d and %d)", seq, prev.By, w)
				}
				got[seq] = slot{By: int64(w), Ord: int64(i)}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if env.Red(t, err) {
			return
		}
		t.Error(err)
	}
	if len(got) != writers*each {
		t.Fatalf("%d distinct slots returned, want %d", len(got), writers*each)
	}
	for s := uint64(1); s <= writers*each; s++ {
		v, err := q.Read(ctx, s)
		if err != nil {
			t.Errorf("slot %d is missing or unreadable: %v (the log must have no gaps)", s, err)
			continue
		}
		if want := got[s]; v.By != want.By || v.Ord != want.Ord {
			t.Errorf("slot %d holds writer %d/%d, the appender that got it was %d/%d", s, v.By, v.Ord, want.By, want.Ord)
		}
	}
	if _, err := q.Read(ctx, writers*each+1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Read past the end = %v, want ErrNotFound", err)
	}
	if _, err := q.Read(ctx, 0); err == nil {
		t.Errorf("Read(0) succeeded; slots start at 1")
	}
	// Per-writer order is preserved: a writer's later append gets a higher slot.
	last := map[int64]int64{}
	seqs := make([]uint64, 0, len(got))
	for s := range got {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, s := range seqs {
		v := got[s]
		if prev, ok := last[v.By]; ok && v.Ord != prev+1 {
			t.Errorf("writer %d: append %d landed after append %d", v.By, v.Ord, prev)
		}
		last[v.By] = v.Ord
	}

	// Hints: tail never exceeds the first free slot; head only moves up.
	tail, err := q.Tail(ctx)
	if err != nil || tail > writers*each+1 {
		t.Errorf("Tail = %d, %v; must be <= %d", tail, err, writers*each+1)
	}
	if err := q.AdvanceHead(ctx, 10); err != nil {
		t.Errorf("AdvanceHead(10): %v", err)
	}
	if err := q.AdvanceHead(ctx, 5); err != nil {
		t.Errorf("AdvanceHead(5): %v", err)
	}
	if h, err := q.Head(ctx); err != nil || h != 10 {
		t.Errorf("Head = %d, %v; AdvanceHead never lowers it, want 10", h, err)
	}
	be.Proxy.AssertNoList(t, be.Last)
}

// TestRSTORE106PagedIndex: 20 writers on one page, no lost entry.
func TestRSTORE106PagedIndex(t *testing.T) {
	parity.Covers(t, "R-STORE-106")
	be := env.Start(t)
	b := be.Open(t, "control")
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	idx := store.NewPagedIndex[entry](b, "tbpage", "p1", 50)

	if got := idx.LastPage(1); got != 0 {
		t.Errorf("LastPage(1) = %d, want 0", got)
	}
	for _, c := range []struct{ count, want uint64 }{{50, 0}, {51, 1}, {100, 1}, {101, 2}, {0, 0}} {
		if got := idx.LastPage(c.count); got != c.want {
			t.Errorf("LastPage(%d) = %d, want %d", c.count, got, c.want)
		}
	}

	var wg sync.WaitGroup
	errc := make(chan error, 20)
	for w := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errc <- idx.Set(ctx, uint64(w), entry{W: int64(w), V: fmt.Sprintf("e%d", w)})
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Errorf("Set under contention: %v", err)
		}
	}
	page, err := idx.Page(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for w := range 20 {
		if w >= len(page) || page[w].V != fmt.Sprintf("e%d", w) || page[w].W != int64(w) {
			t.Errorf("entry %d lost or wrong: %+v", w, page[min(w, len(page)-1)])
		}
	}
	for w := 20; w < len(page); w++ {
		if page[w] != (entry{}) {
			t.Errorf("entry %d was never set but is %+v (missing entries are zero values)", w, page[w])
		}
	}
	// Entry 120 lives on page 2 and creates it on first use; page 1 does not exist yet.
	if err := idx.Set(ctx, 120, entry{W: 120, V: "far"}); err != nil {
		t.Fatal(err)
	}
	p2, err := idx.Page(ctx, 2)
	if err != nil || len(p2) == 0 || p2[120-100].V != "far" {
		t.Errorf("Page(2) = %v, %v; entry 120 belongs at offset 20", p2, err)
	}
	// Reads are cached: an unchanged page costs one conditional GET (304), not a re-read.
	be.Proxy.Reset()
	for range 3 {
		if _, err := idx.Page(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := env.Total(be.Proxy.Counts(), be.Last); n > 4 {
		t.Errorf("3 reads of an unchanged page cost %d requests, want <= 4 (2 for the first, 1 each for the 304s)", n)
	}
	be.Proxy.AssertNoList(t, be.Last)
}

// TestRSTORE106ShardSet: concurrent Add/Remove over the shard documents.
func TestRSTORE106ShardSet(t *testing.T) {
	parity.Covers(t, "R-STORE-106")
	be := env.Start(t)
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	type act struct {
		B string `cbor:"b,omitempty"`
	}
	set := store.NewShardSet[act](b, "tjact", 4)
	var wg sync.WaitGroup
	errc := make(chan error, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errc <- set.Add(ctx, fmt.Sprintf("job%03d", i), act{B: fmt.Sprintf("b%d", i)})
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if env.Red(t, err) {
			return
		}
		if err != nil {
			t.Errorf("Add: %v", err)
		}
	}
	all, err := set.All(ctx)
	if err != nil || len(all) != 200 {
		t.Fatalf("All = %d entries, %v; want 200 (no lost add)", len(all), err)
	}
	for i := range 200 {
		if v := all[fmt.Sprintf("job%03d", i)]; v.B != fmt.Sprintf("b%d", i) {
			t.Errorf("job%03d = %+v", i, v)
		}
	}
	for i := 0; i < 200; i += 2 {
		if err := set.Remove(ctx, fmt.Sprintf("job%03d", i)); err != nil {
			t.Errorf("Remove: %v", err)
		}
	}
	if err := set.Remove(ctx, "never-added"); err != nil {
		t.Errorf("Remove of an absent id = %v, want nil", err)
	}
	all, err = set.All(ctx)
	if err != nil || len(all) != 100 {
		t.Errorf("All after removing half = %d, %v; want 100", len(all), err)
	}
	// An unchanged set costs N conditional GETs (304).
	be.Proxy.Reset()
	if _, err := set.All(ctx); err != nil {
		t.Fatal(err)
	}
	be.Proxy.Reset()
	if _, err := set.All(ctx); err != nil {
		t.Fatal(err)
	}
	if n := env.Total(be.Proxy.Counts(), be.Last); n != 4 {
		t.Errorf("All on an unchanged set made %d requests, want N=4 (one 304 per shard)", n)
	}
	be.Proxy.AssertNoList(t, be.Last)
}

// TestRSTORE106Lease: 3 contenders, exactly one holder per epoch.
func TestRSTORE106Lease(t *testing.T) {
	parity.Covers(t, "R-STORE-106")
	be := env.Start(t)
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const ttl = time.Second
	owners := []string{"orch-a", "orch-b", "orch-c"}
	leases := make([]*store.Lease, len(owners))
	for i, o := range owners {
		leases[i] = store.NewLease(b, "ctl/job-reconciler", o, ttl)
	}
	holderOf := map[uint64]string{}
	var mu sync.Mutex
	round := func() (winners []string) {
		var wg sync.WaitGroup
		for i, l := range leases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				held, epoch, err := l.TryAcquire(ctx)
				if env.Red(t, err) {
					return
				}
				if err != nil {
					t.Errorf("%s TryAcquire: %v", owners[i], err)
					return
				}
				if !held {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				winners = append(winners, owners[i])
				if prev, ok := holderOf[epoch]; ok && prev != owners[i] {
					t.Errorf("epoch %d is held by %s and %s", epoch, prev, owners[i])
				}
				holderOf[epoch] = owners[i]
			}()
		}
		wg.Wait()
		return winners
	}
	first := round()
	if t.Failed() {
		return
	}
	if len(first) != 1 {
		t.Fatalf("3 contenders on a free lease: %v hold it, want exactly one", first)
	}
	// The holder renews: same epoch, others stay out.
	for range 3 {
		if w := round(); len(w) != 1 || w[0] != first[0] {
			t.Fatalf("while %s holds the lease, round winners = %v", first[0], w)
		}
	}
	if len(holderOf) != 1 {
		t.Errorf("renewals changed the epoch: %v", holderOf)
	}
	// Takeover only after expires + skew (fleet.skew 5 s) has passed.
	time.Sleep(ttl + 6*time.Second)
	second := round()
	if len(second) != 1 {
		t.Fatalf("after expiry %v hold the lease, want exactly one", second)
	}
	if len(holderOf) != 2 {
		t.Errorf("a takeover must raise the epoch by one: epochs seen %v", holderOf)
	}
	for e := range holderOf {
		if e != 1 && e != 2 {
			t.Errorf("unexpected epoch %d", e)
		}
	}
	be.Proxy.AssertNoList(t, be.Last)
}
