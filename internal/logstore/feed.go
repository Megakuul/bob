package logstore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/safe"
)

// Follow timing (R-LOG-101, ADR 0019 decision 4).
const (
	gapToLake    = time.Second     // a follower's gap older than this asks for a lake read
	liveSilence  = 6 * time.Second // no live chunk for this long: degraded
	degradedPoll = 5 * time.Second // lake read interval while degraded
	minLakeGap   = time.Second     // lake reads of one feed are at least this far apart
	feedTick     = time.Second     // job state / subscription renewal check
	delayedBeat  = time.Second     // a degraded follower hears from the server at least this often
	feedCap      = 200000          // lines one feed keeps (the oldest are evicted)
	maxBatch     = MaxChunkLines   // lines per follower message
	followWait   = 500 * time.Millisecond
)

// JobState is what a feed needs to know about one job of a build.
type JobState struct {
	ID       string
	Owner    string // worker id while claimed or running
	Epoch    int64
	Terminal bool
}

// JobsFunc returns the jobs of a build and whether the build itself is terminal.
type JobsFunc func(ctx context.Context, buildID string) (jobs []JobState, buildDone bool, err error)

// Live is the live path of one orchestrator (internal/orchestrator): it pulls the in-memory
// logs of running jobs from their workers over the workers' Attach streams (R-LOG-002).
type Live interface {
	// Subscribe makes the owners of the running jobs stream their logs into f (subscribing or
	// renewing as needed) and returns the ids of the jobs that have a live source here now.
	Subscribe(ctx context.Context, f *Feed, running []JobState) map[string]bool
	// Unsubscribe ends every subscription of f (its last follower left).
	Unsubscribe(f *Feed)
}

// Sources are what an orchestrator merges for its followers: the lake, the live path and the
// job states. One Feed per build is shared by all followers of that build on this
// orchestrator, and so are its lake reads (ADR 0019 decision 4).
type Sources struct {
	Lake *Reader
	Live Live // nil: lake only
	Jobs JobsFunc
	Now  func() time.Time
	Log  *slog.Logger

	mu    sync.Mutex
	feeds map[string]*Feed
}

// NewSources performs no I/O.
func NewSources(lake *Reader, live Live, jobs JobsFunc) *Sources {
	return &Sources{Lake: lake, Live: live, Jobs: jobs, Now: time.Now, Log: slog.Default(), feeds: map[string]*Feed{}}
}

// Feeds is the number of builds that have followers here now.
func (s *Sources) Feeds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.feeds)
}

// Feed is the per-build buffer of one orchestrator: an append-only, capped arrival log of
// lines from live chunks and lake reads, deduplicated by (package, attempt, seq). Followers
// read it by offset, each with its own merger.
type Feed struct {
	build string
	src   *Sources

	mu       sync.Mutex
	base     int64 // absolute offset of ents[0]
	ents     []Line
	seen     map[lineKey]struct{}
	restarts map[string]map[int32]int64
	ch       chan struct{} // closed and replaced on every change
	lastLive time.Time
	delayed  bool
	complete bool // the build is terminal and a lake read made after that is in the buffer
	wantLake bool
	refs     int
	cancel   context.CancelFunc
	done     <-chan error
	wake     chan struct{}
}

// Build is the build id of the feed.
func (f *Feed) Build() string { return f.build }

func (s *Sources) acquire(buildID string) *Feed {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.feeds[buildID]; f != nil {
		f.mu.Lock()
		f.refs++
		f.mu.Unlock()
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Feed{build: buildID, src: s, seen: map[lineKey]struct{}{}, restarts: map[string]map[int32]int64{},
		ch: make(chan struct{}), refs: 1, cancel: cancel, wake: make(chan struct{}, 1),
		lastLive: s.now()}
	s.feeds[buildID] = f
	f.done = safe.Go(func() error { f.run(ctx); return nil })
	return f
}

func (s *Sources) release(f *Feed) {
	s.mu.Lock()
	f.mu.Lock()
	f.refs--
	last := f.refs == 0
	f.mu.Unlock()
	if last {
		delete(s.feeds, f.build)
	}
	s.mu.Unlock()
	if last {
		f.cancel()
		if err := <-f.done; err != nil {
			s.log().Error("logstore: feed crashed", "build", f.build, "err", err)
		}
		if s.Live != nil {
			s.Live.Unsubscribe(f)
		}
	}
}

func (s *Sources) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sources) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (f *Feed) notify() {
	close(f.ch)
	f.ch = make(chan struct{})
}

// AddLive adds what a validated live chunk carried.
func (f *Feed) AddLive(lines []Line, restarts []Restart) {
	f.add(lines, restarts, true)
}

func (f *Feed) add(lines []Line, restarts []Restart, live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range restarts {
		if r.Attempt < 2 || r.FromSeq < 1 {
			continue
		}
		m := f.restarts[r.Package]
		if m == nil {
			m = map[int32]int64{}
			f.restarts[r.Package] = m
		}
		if old, ok := m[r.Attempt]; !ok || r.FromSeq < old {
			m[r.Attempt] = r.FromSeq
			n++
		}
	}
	for _, l := range lines {
		k := lineKey{l.Package, l.Attempt, l.Seq}
		if _, ok := f.seen[k]; ok {
			continue
		}
		f.seen[k] = struct{}{}
		f.ents = append(f.ents, l)
		n++
	}
	if live {
		f.lastLive = f.src.now()
	}
	if over := len(f.ents) - feedCap; over > 0 {
		for _, l := range f.ents[:over] {
			delete(f.seen, lineKey{l.Package, l.Attempt, l.Seq})
		}
		f.ents = append([]Line(nil), f.ents[over:]...)
		f.base += int64(over)
	}
	if n > 0 {
		f.notify()
	}
}

// Cursors is the high-water mark of the buffer per package: the last line of the valid attempt
// up to which the buffer has every line. A subscription asks the worker for what follows.
func (f *Feed) Cursors() map[string]Cursor {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := newMerger(nil, f.src.now)
	for p, rs := range f.restarts {
		for a, s := range rs {
			m.restart(Restart{Package: p, Attempt: a, FromSeq: s})
		}
	}
	for _, l := range f.ents {
		m.offer(l)
	}
	m.drain()
	out := make(map[string]Cursor, len(m.cur))
	for p, c := range m.cur {
		out[p] = c
	}
	return out
}

// askLake asks the feed for a lake read (a follower has a gap).
func (f *Feed) askLake() {
	f.mu.Lock()
	f.wantLake = true
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

type snapshot struct {
	lines    []Line
	next     int64
	restarts []Restart
	ch       chan struct{}
	delayed  bool
	complete bool
	evicted  bool
}

func (f *Feed) read(off int64) snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := snapshot{ch: f.ch, delayed: f.delayed, complete: f.complete}
	if off < f.base {
		s.evicted = off > 0
		off = f.base
	}
	s.lines = append([]Line(nil), f.ents[off-f.base:]...)
	s.next = f.base + int64(len(f.ents))
	for p, m := range f.restarts {
		for a, from := range m {
			s.restarts = append(s.restarts, Restart{Package: p, Attempt: a, FromSeq: from})
		}
	}
	return s
}

// run keeps the feed's sources up to date while it has followers: the job states every
// second, the live subscriptions, and the lake reads (first read, follower gaps, degraded
// mode every 5 s, and the final read after the build ended).
func (f *Feed) run(ctx context.Context) {
	var lastLake, subscribedAt time.Time
	t := time.NewTicker(feedTick)
	defer t.Stop()
	for {
		now := f.src.now()
		jobs, buildDone, err := f.src.Jobs(ctx, f.build)
		if err != nil && ctx.Err() == nil {
			f.src.log().Warn("logstore: feed job states", "build", f.build, "err", err)
		}
		var running []JobState
		for _, j := range jobs {
			if !j.Terminal {
				running = append(running, j)
			}
		}
		finished := err == nil && buildDone && len(running) == 0
		live := map[string]bool{}
		if f.src.Live != nil && len(running) > 0 {
			live = f.src.Live.Subscribe(ctx, f, running)
			if subscribedAt.IsZero() {
				subscribedAt = now
			}
		} else {
			subscribedAt = time.Time{}
		}
		attached := true
		for _, j := range running {
			attached = attached && live[j.ID]
		}

		f.mu.Lock()
		silent := !subscribedAt.IsZero() && now.Sub(subscribedAt) > liveSilence && now.Sub(f.lastLive) > liveSilence
		delayed := len(running) > 0 && (!attached || silent)
		if delayed != f.delayed {
			f.delayed = delayed
			f.notify()
		}
		want, complete := f.wantLake, f.complete
		f.mu.Unlock()

		since := now.Sub(lastLake)
		read := lastLake.IsZero() || (want && since >= minLakeGap) || (delayed && since >= degradedPoll) ||
			(finished && !complete)
		if read && err == nil {
			f.mu.Lock()
			f.wantLake = false
			f.mu.Unlock()
			lines, rs, lerr := f.src.Lake.Raw(ctx, f.build, "")
			if lerr != nil {
				if ctx.Err() == nil {
					f.src.log().Warn("logstore: feed lake read", "build", f.build, "err", lerr)
				}
			} else {
				lastLake = now
				f.add(lines, rs, false)
				if finished {
					f.mu.Lock()
					f.complete = true
					f.notify()
					f.mu.Unlock()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-f.wake:
		}
	}
}

// Follow merges persisted and live lines for one follower and calls emit in order until
// ctx ends or every package has its final marker.
func Follow(ctx context.Context, src *Sources, buildID string, cur map[string]Cursor, emit func(Batch) error) error {
	return FollowPackage(ctx, src, buildID, "", cur, emit)
}

// FollowPackage is Follow restricted to one package (pkg "" = every package).
func FollowPackage(ctx context.Context, src *Sources, buildID, pkg string, cur map[string]Cursor, emit func(Batch) error) error {
	if _, err := BuildTime(buildID); err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if err := checkPackage(pkg); err != nil {
		return err
	}
	f := src.acquire(buildID)
	defer src.release(f)
	m := newMerger(cur, src.now)
	var off int64
	var lastEmit time.Time
	timer := time.NewTimer(followWait)
	defer timer.Stop()
	for {
		s := f.read(off)
		off = s.next
		if s.evicted {
			f.askLake()
		}
		for _, r := range s.restarts {
			if pkg == "" || r.Package == pkg {
				m.restart(r)
			}
		}
		for _, l := range s.lines {
			if pkg == "" || l.Package == pkg {
				m.offer(l)
			}
		}
		m.drain()
		lines, restarts := m.take()
		done := s.complete && len(m.pending) == 0
		now := src.now()
		if len(lines) > 0 || len(restarts) > 0 || done || (s.delayed && now.Sub(lastEmit) >= delayedBeat) {
			if err := emitBatches(Batch{Lines: lines, Restarts: restarts, Delayed: s.delayed}, done, m, emit); err != nil {
				return err
			}
			lastEmit = now
		}
		if done {
			return nil
		}
		if m.gapOlderThan(gapToLake) {
			f.askLake()
		}
		timer.Reset(followWait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ch:
		case <-timer.C:
		}
	}
}

// emitBatches sends b in messages of at most maxBatch lines; the restarts go with the first,
// the end markers and done with the last.
func emitBatches(b Batch, done bool, m *merger, emit func(Batch) error) error {
	lines := b.Lines
	first := true
	for first || len(lines) > 0 {
		n := min(len(lines), maxBatch)
		out := Batch{Lines: lines[:n], Delayed: b.Delayed}
		if first {
			out.Restarts = b.Restarts
		}
		lines = lines[n:]
		if len(lines) == 0 && done {
			out.End, out.Done = m.finals(), true
		}
		if err := emit(out); err != nil {
			return err
		}
		first = false
	}
	return nil
}
