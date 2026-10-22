package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/logstore"
)

// Live pull timing and limits (R-LOG-002, R-LOG-102, SPEC §05.9.1).
const (
	liveRenew      = 5 * time.Second  // subscription renewal
	liveExpiry     = 15 * time.Second // subscription expiry sent to the worker
	liveRate       = 1 << 20          // bytes/s per worker
	liveBurst      = 4 << 20          // burst per worker
	liveMaxRestart = 1000             // restarts or finals per chunk
)

// ErrChunkRejected marks a LiveChunk the orchestrator dropped (hostile or stale worker input).
var ErrChunkRejected = errors.New("live chunk rejected")

// LiveLogs is the orchestrator side of the live log pull (D55, ADR 0019): while a build has
// followers here, it asks the workers that run the build's jobs for their in-memory logs over
// the workers' own Attach streams (LiveSubscribe, renewed every 5 s, expiry 15 s) and feeds the
// validated LiveChunks into the build's logstore.Feed. Orchestrators never connect to workers
// and never to each other. It implements logstore.Live.
//
// A chunk is accepted only on the subscription this orchestrator sent to the worker that the
// job record names as owner, with the record's epoch (read at most a second before by the
// feed), and within the size and rate limits; anything else is dropped and logged as
// `invalid_argument: log line invalid: <package>/<seq>` (R-LOG-102).
type LiveLogs struct {
	Self string // orchestrator id: prefix of subscription ids
	Now  func() time.Time
	Log  *slog.Logger

	mu      sync.Mutex
	workers map[string]*liveWorker
	subs    map[string]*liveSub                    // subscription id → subscription
	byFeed  map[*logstore.Feed]map[string]*liveSub // feed → job → subscription

	accepted atomic.Int64
	rejected atomic.Int64
	bytes    atomic.Int64
}

type liveWorker struct {
	send   func(*workerv1.AttachResponse) bool
	tokens float64
	filled time.Time
}

type liveSub struct {
	id      string
	job     string
	wid     string
	epoch   int64
	feed    *logstore.Feed
	renewed time.Time
}

// NewLiveLogs performs no I/O.
func NewLiveLogs(self string, now func() time.Time, log *slog.Logger) *LiveLogs {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &LiveLogs{Self: self, Now: now, Log: log, workers: map[string]*liveWorker{},
		subs: map[string]*liveSub{}, byFeed: map[*logstore.Feed]map[string]*liveSub{}}
}

// LiveStats are the counters of the live path (metrics, tests).
type LiveStats struct {
	Accepted, Rejected, Bytes int64
	Workers, Subscriptions    int
}

// Stats returns the live path's counters.
func (l *LiveLogs) Stats() LiveStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LiveStats{Accepted: l.accepted.Load(), Rejected: l.rejected.Load(), Bytes: l.bytes.Load(),
		Workers: len(l.workers), Subscriptions: len(l.subs)}
}

// Attached registers the Attach stream of an authenticated worker. send queues one message on
// that stream without blocking (false: the message was dropped). The returned function
// detaches the stream; a newer Attach of the same worker replaces an older one, whose detach
// then does nothing. Subscriptions over a detached stream are re-sent on the next renewal.
func (l *LiveLogs) Attached(wid string, send func(*workerv1.AttachResponse) bool) (detach func()) {
	w := &liveWorker{send: send, tokens: liveBurst, filled: l.Now()}
	l.mu.Lock()
	l.workers[wid] = w
	// A new stream has none of the old stream's subscriptions: send them again.
	for _, s := range l.subs {
		if s.wid == wid {
			s.renewed = time.Time{}
		}
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		if l.workers[wid] == w {
			delete(l.workers, wid)
		}
		l.mu.Unlock()
	}
}

// IsAttached reports whether worker wid has an Attach stream here.
func (l *LiveLogs) IsAttached(wid string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.workers[wid] != nil
}

func (l *LiveLogs) newSubID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return l.Self + "." + hex.EncodeToString(b[:])
}

// Subscribe implements logstore.Live: one subscription per (orchestrator, job), sent to the job
// owner's Attach stream with the cursors the feed already has, renewed every 5 s.
func (l *LiveLogs) Subscribe(_ context.Context, f *logstore.Feed, running []logstore.JobState) map[string]bool {
	now := l.Now()
	live := map[string]bool{}
	var cursors map[string]*workerv1.LiveCursor
	cursorsOf := func() map[string]*workerv1.LiveCursor {
		if cursors == nil {
			cursors = map[string]*workerv1.LiveCursor{}
			for p, c := range f.Cursors() {
				cursors[p] = &workerv1.LiveCursor{Seq: c.Seq, Attempt: c.Attempt}
			}
		}
		return cursors
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	mine := l.byFeed[f]
	if mine == nil {
		mine = map[string]*liveSub{}
		l.byFeed[f] = mine
	}
	want := map[string]bool{}
	for _, j := range running {
		want[j.ID] = true
		s := mine[j.ID]
		if s != nil && (s.wid != j.Owner || s.epoch != j.Epoch) {
			// The job was requeued or taken over: the old owner's chunks are stale now.
			l.drop(s)
			s = nil
		}
		if j.Owner == "" {
			continue
		}
		w := l.workers[j.Owner]
		if w == nil {
			continue // not attached here: the feed reads the lake
		}
		if s == nil {
			s = &liveSub{id: l.newSubID(), job: j.ID, wid: j.Owner, epoch: j.Epoch, feed: f}
			mine[j.ID] = s
			l.subs[s.id] = s
		}
		if now.Sub(s.renewed) >= liveRenew {
			ok := w.send(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveSubscribe{LiveSubscribe: &workerv1.LiveSubscribe{
				SubscriptionId: s.id, JobId: j.ID, Cursors: cursorsOf(), ExpiresUnixMs: now.Add(liveExpiry).UnixMilli()}}})
			if !ok {
				continue // stream full: try again on the next tick
			}
			s.renewed = now
		}
		live[j.ID] = true
	}
	for job, s := range mine {
		if !want[job] {
			l.drop(s)
		}
	}
	return live
}

// drop ends a subscription (LiveUnsubscribe when its worker is attached). Called with l.mu held.
func (l *LiveLogs) drop(s *liveSub) {
	if w := l.workers[s.wid]; w != nil {
		w.send(&workerv1.AttachResponse{Msg: &workerv1.AttachResponse_LiveUnsubscribe{LiveUnsubscribe: &workerv1.LiveUnsubscribe{SubscriptionId: s.id}}})
	}
	delete(l.subs, s.id)
	if m := l.byFeed[s.feed]; m != nil && m[s.job] == s {
		delete(m, s.job)
	}
}

// Unsubscribe implements logstore.Live: the feed's last follower left.
func (l *LiveLogs) Unsubscribe(f *logstore.Feed) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.byFeed[f] {
		l.drop(s)
	}
	delete(l.byFeed, f)
}

// reject counts and logs a dropped chunk with the SPEC's message.
func (l *LiveLogs) reject(wid, job, pkg string, seq int64, why string) error {
	l.rejected.Add(1)
	msg := "invalid_argument: log line invalid: " + pkg + "/" + strconv.FormatInt(seq, 10)
	l.Log.Warn(msg, "worker", wid, "job", job, "reason", why)
	return fmt.Errorf("%w: %s (%s)", ErrChunkRejected, msg, why)
}

// Chunk validates one LiveChunk that worker wid sent on its Attach stream and feeds it. A
// rejected chunk changes nothing (the error is for the caller's log; the stream stays open).
func (l *LiveLogs) Chunk(wid string, c *workerv1.LiveChunk) error {
	if c == nil {
		return nil
	}
	first := func() (string, int64) {
		if len(c.GetLines()) > 0 {
			return c.GetLines()[0].GetPackage(), c.GetLines()[0].GetSeq()
		}
		return "", 0
	}
	fp, fs := first()
	if len(fp) > logstore.MaxPackageBytes {
		fp = fp[:64]
	}
	size := proto.Size(c)
	now := l.Now()

	l.mu.Lock()
	s := l.subs[c.GetSubscriptionId()]
	w := l.workers[wid]
	var feed *logstore.Feed
	why := ""
	switch {
	case s == nil || s.wid != wid || s.job != c.GetJobId():
		why = "no subscription of this worker for this job"
	case c.GetEpoch() != s.epoch:
		why = "stale epoch"
	case len(c.GetLines()) > logstore.MaxChunkLines || size > logstore.MaxChunkBytes ||
		len(c.GetRestarts()) > liveMaxRestart || len(c.GetFinals()) > liveMaxRestart:
		why = "chunk over the size limit"
	case w == nil:
		why = "worker not attached"
	default:
		w.tokens = min(liveBurst, w.tokens+now.Sub(w.filled).Seconds()*liveRate)
		w.filled = now
		if float64(size) > w.tokens {
			why = "over the rate limit of 1 MiB/s"
		} else {
			w.tokens -= float64(size)
			feed = s.feed
		}
	}
	l.mu.Unlock()
	if why != "" {
		return l.reject(wid, c.GetJobId(), fp, fs, why)
	}

	lines := make([]logstore.Line, 0, len(c.GetLines()))
	for _, x := range c.GetLines() {
		if bad := badLine(x); bad != "" {
			p := x.GetPackage()
			if len(p) > logstore.MaxPackageBytes {
				p = p[:64]
			}
			return l.reject(wid, c.GetJobId(), p, x.GetSeq(), bad)
		}
		lines = append(lines, logstore.Line{Package: x.GetPackage(), Seq: x.GetSeq(), Attempt: x.GetAttempt(),
			Stream: x.GetStream(), TS: x.GetTsUnixNano(), Text: x.GetText()})
	}
	rs := make([]logstore.Restart, 0, len(c.GetRestarts()))
	for _, r := range c.GetRestarts() {
		if r.GetAttempt() < 2 || r.GetFromSeq() < 1 || r.GetPackage() == "" || len(r.GetPackage()) > logstore.MaxPackageBytes {
			return l.reject(wid, c.GetJobId(), fp, fs, "invalid restart")
		}
		rs = append(rs, logstore.Restart{Package: r.GetPackage(), Attempt: r.GetAttempt(), FromSeq: r.GetFromSeq()})
	}
	l.accepted.Add(1)
	l.bytes.Add(int64(size))
	feed.AddLive(lines, rs)
	return nil
}

// badLine checks one line of a chunk; "" means valid.
func badLine(x *workerv1.LiveLine) string {
	switch {
	case x.GetPackage() == "" || len(x.GetPackage()) > logstore.MaxPackageBytes:
		return "package label"
	case len(x.GetText()) > logstore.MaxLineBytes:
		return "line over 4 KiB"
	case !utf8.ValidString(x.GetText()) || !utf8.ValidString(x.GetPackage()):
		return "not UTF-8"
	case x.GetSeq() < 1 || x.GetAttempt() < 1:
		return "seq or attempt out of range"
	case x.GetStream() < 0 || x.GetStream() > logstore.StreamBob:
		return "stream"
	}
	return ""
}
