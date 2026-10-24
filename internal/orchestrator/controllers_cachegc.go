package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/store"
)

// CacheGC is the cache part of the retention controller (§05.11 retention.cache, §06
// R-STORE-150): it walks the promotion logs promo/<day> (no LIST) and removes the served
// paths that no release and no recent promotion references.
//
// Reference rule, per promotion log entry older than the retention (and older than
// cacheGCGrace, so a promotion that appended its entry and has not written the narinfo yet is
// never touched):
//   - kept when its path is in the closure (narinfo references, transitively) of a path of a
//     release that is not deleted, or of a path promoted within the retention, or when its
//     visibility document lists a public release: the entry moves to today's log, so the
//     path is judged again one retention period later (or at the next run with 0 days);
//   - dropped without touching objects when the narinfo was written well after the entry (the
//     path was collected and promoted again: the newer entry decides);
//   - else collected: narinfo first (the path is no longer served), then listing, NAR and
//     visibility document; the entry last, so a crash is finished by the next run.
//
// Releases' rel/ assets are never touched here (DeleteRelease removes them).
type CacheGC struct {
	Control *store.Bucket
	Objects *store.Objects  // artifacts bucket
	Vis     *cache.VisStore // nix/vis documents
	// Retained returns the store hashes of every release that is not deleted
	// (release.Releases.Retained).
	Retained func(ctx context.Context) (map[string]bool, error)
	Now      func() time.Time // the orchestrator clock (re-appended entries)
	Log      *slog.Logger

	// refs memoises narinfo references: a served narinfo never changes.
	refs map[string][]string
}

// cacheGCState is the walk position, kept in ctl/retention.cu with the other walks.
type cacheGCState struct {
	From string            `cbor:"f,omitempty"` // first day (yyyymmdd) that may still hold entries
	Seq  map[string]uint64 `cbor:"s,omitempty"` // day -> first slot not judged yet (open days)
	Last time.Time         `cbor:"t,omitzero"`  // last full run (runs are daily unless retention is 0)
}

const (
	// cacheGCGrace: entries younger than this are never judged (promotion appends the entry
	// before it writes the narinfo).
	cacheGCGrace = 10 * time.Minute
	// cacheGCLookback is how far back the first run looks for promotion logs.
	cacheGCLookback = 400 * 24 * time.Hour
	// cacheGCEvery is the run period when the retention is not 0: every run reads the
	// promotion logs of the whole retention period and the closures of their paths.
	cacheGCEvery = 24 * time.Hour
	// cacheGCMaxSlots bounds the slots one run reads; the next run continues.
	cacheGCMaxSlots = 200_000
	// cacheGCMaxRefs bounds the memoised narinfo references.
	cacheGCMaxRefs = 1_000_000
)

const dayFormat = "20060102"

func (g *CacheGC) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

func (g *CacheGC) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// promoSlot is one promotion log entry.
type promoSlot struct {
	day time.Time
	seq uint64
	e   *store.PromoEntry
}

// dayWalk is what one run read of one day's log.
type dayWalk struct {
	at    time.Time
	slots []promoSlot
	end   uint64 // first slot after the ones read
	done  bool   // end is the end of the log (not the read bound)
}

// Collect runs one cache GC at run time now with cutoff before (now − retention). It returns
// how many paths it removed.
func (g *CacheGC) Collect(ctx context.Context, now, before time.Time, st *cacheGCState) (int, error) {
	if g.Control == nil || g.Objects == nil || g.Vis == nil {
		return 0, errors.New("orchestrator: cache GC needs the control bucket, the artifacts bucket and the visibility documents")
	}
	now, before = now.UTC(), before.UTC()
	zero := !before.Before(now) // retention 0: collect at every run
	if !zero && !st.Last.IsZero() && !now.Before(st.Last) && now.Sub(st.Last) < cacheGCEvery {
		return 0, nil
	}
	cutoff := before
	if c := now.Add(-cacheGCGrace); c.Before(cutoff) {
		cutoff = c
	}
	today := day(now)
	from := today.Add(-cacheGCLookback)
	if d, err := time.Parse(dayFormat, st.From); err == nil && d.After(from) {
		from = d
	}
	if st.Seq == nil {
		st.Seq = map[string]uint64{}
	}
	// 1. Read every entry not judged yet, from the first open day to today. The days are
	// probed in parallel first: most of them are empty (the first run looks back 400 days).
	var days []string
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(dayFormat))
	}
	var mu sync.Mutex
	tails := map[string]uint64{}
	var probeErr error
	forEach(days, func(k string) {
		d, _ := time.Parse(dayFormat, k)
		tail, err := g.probeDay(ctx, d, max(st.Seq[k], 1))
		mu.Lock()
		defer mu.Unlock()
		tails[k] = tail
		if err != nil {
			probeErr = err
		}
	})
	if probeErr != nil {
		return 0, probeErr
	}
	var walks []dayWalk
	var due []promoSlot
	recent := map[string]bool{}
	budget := cacheGCMaxSlots
	for d := from; !d.After(today) && budget > 0; d = d.AddDate(0, 0, 1) {
		k := d.Format(dayFormat)
		w, err := g.readDay(ctx, d, max(st.Seq[k], 1), tails[k], budget)
		if err != nil {
			return 0, err
		}
		budget -= len(w.slots)
		walks = append(walks, w)
		for _, s := range w.slots {
			if s.e.At.Value().Before(cutoff) {
				due = append(due, s)
			} else {
				recent[s.e.StoreHash.Value()] = true
			}
		}
	}
	// 2. Judge the due entries against the closure of the releases and the recent promotions.
	judged := map[promoKey]bool{}
	removed := 0
	if len(due) > 0 {
		keep := map[string]bool{}
		if g.Retained != nil {
			rel, err := g.Retained(ctx)
			if err != nil {
				return 0, fmt.Errorf("cache GC: releases: %w", err)
			}
			for h := range rel {
				keep[h] = true
			}
		}
		for h := range recent {
			keep[h] = true
		}
		if err := g.closure(ctx, keep); err != nil {
			return 0, err
		}
		for _, s := range due {
			if ctx.Err() != nil {
				return removed, ctx.Err()
			}
			gone, err := g.judge(ctx, s, keep)
			if err != nil {
				g.log().Warn("cache GC: entry not judged; next run", "day", s.day.Format(dayFormat), "seq", s.seq, "err", err.Error())
				continue
			}
			if gone {
				removed++
			}
			judged[promoKey{s.day, s.seq}] = true
		}
	}
	// 3. Each day's position moves to its first entry not judged; a day before yesterday whose
	// entries are all judged is closed (no promotion appends to it any more).
	open := true
	for _, w := range walks {
		if len(w.slots) == 0 && !w.done {
			break // not read (budget)
		}
		d := w.day()
		k := d.Format(dayFormat)
		next := w.end
		for _, s := range w.slots {
			if !judged[promoKey{s.day, s.seq}] {
				next = s.seq
				break
			}
		}
		if open && w.done && next == w.end && d.Before(today.AddDate(0, 0, -1)) {
			delete(st.Seq, k)
			st.From = d.AddDate(0, 0, 1).Format(dayFormat)
			continue
		}
		open = false
		st.Seq[k] = next
	}
	if !zero {
		st.Last = now
	}
	if removed > 0 {
		g.log().Info(fmt.Sprintf("cache GC removed %d paths", removed), "cutoff", cutoff)
	}
	return removed, nil
}

type promoKey struct {
	day time.Time
	seq uint64
}

func (w dayWalk) day() time.Time {
	if len(w.slots) > 0 {
		return w.slots[0].day
	}
	return w.at
}

// day is the UTC day of t.
func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// probeDay returns the tail hint of day d's log, or 0 when the log has nothing at or after
// slot seq (an empty day costs two GETs: the tail hint and slot seq).
func (g *CacheGC) probeDay(ctx context.Context, d time.Time, seq uint64) (uint64, error) {
	log := store.PromoLog(g.Control, d)
	tail, err := log.Tail(ctx)
	if err != nil || tail > seq {
		return tail, err
	}
	// The tail hint is raised after the slot is written (best effort): read slot seq.
	_, err = log.Read(ctx, seq)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	}
	return seq + 1, nil
}

// readDay reads the entries of day d from slot seq on, at most limit of them. Deleted slots
// below the tail hint are holes, not the end; tail 0 is an empty day.
func (g *CacheGC) readDay(ctx context.Context, d time.Time, seq, tail uint64, limit int) (dayWalk, error) {
	w := dayWalk{at: d}
	if tail == 0 {
		w.end, w.done = seq, true
		return w, nil
	}
	log := store.PromoLog(g.Control, d)
	for ; len(w.slots) < limit; seq++ {
		e, err := log.Read(ctx, seq)
		if errors.Is(err, store.ErrNotFound) {
			if seq >= tail {
				w.end, w.done = seq, true
				return w, nil
			}
			continue // a judged (deleted) slot
		}
		if err != nil {
			return w, err
		}
		w.slots = append(w.slots, promoSlot{day: d, seq: seq, e: e})
	}
	w.end = seq
	return w, nil
}

// closure adds to keep every served path that a path in keep references, transitively.
func (g *CacheGC) closure(ctx context.Context, keep map[string]bool) error {
	queue := make([]string, 0, len(keep))
	for h := range keep {
		queue = append(queue, h)
	}
	for len(queue) > 0 {
		h := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		refs, err := g.references(ctx, h)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if !keep[r] {
				keep[r] = true
				queue = append(queue, r)
			}
		}
	}
	return nil
}

// references returns the store hashes the served narinfo of h references (none when h is
// not served).
func (g *CacheGC) references(ctx context.Context, h string) ([]string, error) {
	if refs, ok := g.refs[h]; ok {
		return refs, nil
	}
	if !cache.ValidHash(h) {
		return nil, nil
	}
	body, _, err := g.Objects.Read(ctx, cache.NarInfoKey(h), cache.MaxNarInfo)
	if errors.Is(err, store.ErrObjectNotFound) {
		return nil, nil // not served (yet or any more): nothing to follow, nothing to memoise
	}
	if err != nil {
		return nil, fmt.Errorf("cache GC: narinfo %s: %w", h, err)
	}
	ni, err := cache.ParseNarInfo(body)
	if err != nil {
		return nil, fmt.Errorf("cache GC: narinfo %s is damaged: %w", h, err)
	}
	var refs []string
	for _, r := range ni.References {
		if rh := cache.HashOf(r); rh != h && cache.ValidHash(rh) {
			refs = append(refs, rh)
		}
	}
	if g.refs == nil || len(g.refs) >= cacheGCMaxRefs {
		g.refs = map[string][]string{}
	}
	g.refs[h] = refs
	return refs, nil
}

// judge applies the reference rule to one due entry and removes the entry. gone reports that
// the path was collected.
func (g *CacheGC) judge(ctx context.Context, s promoSlot, keep map[string]bool) (gone bool, err error) {
	h := s.e.StoreHash.Value()
	switch {
	case !cache.ValidHash(h):
		// not a store hash: nothing to collect, the entry is dropped
	case keep[h]:
		err = g.carry(ctx, s)
	default:
		g.Vis.Invalidate(h)
		vis, verr := g.Vis.Get(ctx, h)
		if verr != nil {
			return false, verr
		}
		if vis != nil && len(vis.Public) > 0 {
			err = g.carry(ctx, s)
			break
		}
		info, herr := g.Objects.Head(ctx, cache.NarInfoKey(h))
		switch {
		case herr == nil && info.LastModified.After(s.e.At.Value().Add(cacheGCGrace)):
			// collected before and promoted again since: the newer entry decides
		case herr == nil || errors.Is(herr, store.ErrObjectNotFound):
			if err = g.remove(ctx, h); err == nil {
				gone = herr == nil
			}
		default:
			err = herr
		}
	}
	if err != nil {
		return false, err
	}
	if err := store.Delete(ctx, g.Control, s.e); err != nil && !errors.Is(err, store.ErrNotFound) {
		return gone, err
	}
	return gone, nil
}

// carry moves a kept entry to today's log: the path is judged again one retention later.
func (g *CacheGC) carry(ctx context.Context, s promoSlot) error {
	now := g.now()
	_, err := store.PromoLog(g.Control, now).Append(ctx, &store.PromoEntry{StoreHash: store.Set(s.e.StoreHash.Value()),
		Project: store.Set(s.e.Project.Value()), Build: store.Set(s.e.Build.Value()), At: store.Set(now)})
	return err
}

// remove deletes a path's objects, the narinfo first: a crash leaves objects that nothing
// serves, and the entry (deleted last) makes the next run finish the work.
func (g *CacheGC) remove(ctx context.Context, h string) error {
	for _, k := range []string{cache.NarInfoKey(h), cache.ListingKey(h), cache.NarKey(h), cache.NixVisPrefix + h + ".json"} {
		if err := g.Objects.Delete(ctx, k); err != nil {
			return fmt.Errorf("cache GC: delete %s: %w", k, err)
		}
	}
	g.Vis.Invalidate(h)
	delete(g.refs, h)
	return nil
}
