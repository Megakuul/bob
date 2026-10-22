package worker

import (
	"context"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	workerv1 "github.com/megakuul/bob/internal/gen/bob/worker/v1"
	"github.com/megakuul/bob/internal/logstore"
)

// Live pull limits on the worker side (R-LOG-002, R-LOG-102): one chunk every 250 ms per
// subscription, at most 5,000 lines and 4 MiB per chunk, and at most 1 MiB/s per stream with a
// 4 MiB burst. The worker keeps below the orchestrator's limits itself (90 %, its own clock
// is not the orchestrator's), so its chunks are not dropped; what does not fit waits for the
// next tick (a subscriber that falls behind the ring fills its gap from the lake).
const (
	chunkEvery   = 250 * time.Millisecond
	rateBytes    = 900 << 10
	burstBytes   = 3600 << 10
	maxSubs      = 256 // subscriptions per stream (an orchestrator needs one per followed job)
	subMaxExpiry = time.Minute
)

// LiveStream serves the live-log messages of one Attach stream (one orchestrator): it answers
// LiveSubscribe with LiveChunks every 250 ms until the subscription expires or is cancelled.
// Without a subscription it sends nothing (R-LOG-002: 0 bytes of log data without followers).
type LiveStream struct {
	logs *Logs
	send func(*workerv1.LiveChunk) error

	mu     sync.Mutex
	subs   map[string]*liveSub
	tokens float64
	filled time.Time
}

type liveSub struct {
	id      string
	job     string
	expires time.Time
	cursors map[string]logstore.Cursor
	pos     int64 // next absolute ring index to send; -1 = not started
	rsSent  int   // restarts already sent
	finSent int   // finals already sent
}

// Stream returns the live server of one Attach stream; send writes one AttachRequest with the
// chunk (the Attach client serialises its sends).
func (l *Logs) Stream(send func(*workerv1.LiveChunk) error) *LiveStream {
	return &LiveStream{logs: l, send: send, subs: map[string]*liveSub{}, tokens: burstBytes, filled: l.Now()}
}

// Handle takes the live-log messages of an AttachResponse and reports whether m was one.
func (s *LiveStream) Handle(m *workerv1.AttachResponse) bool {
	switch {
	case m.GetLiveSubscribe() != nil:
		s.subscribe(m.GetLiveSubscribe())
		return true
	case m.GetLiveUnsubscribe() != nil:
		s.mu.Lock()
		delete(s.subs, m.GetLiveUnsubscribe().GetSubscriptionId())
		s.mu.Unlock()
		return true
	}
	return false
}

func (s *LiveStream) subscribe(m *workerv1.LiveSubscribe) {
	id, job := m.GetSubscriptionId(), m.GetJobId()
	if id == "" || len(id) > 128 || job == "" || len(job) > 128 {
		return
	}
	now := s.logs.Now()
	exp := time.UnixMilli(m.GetExpiresUnixMs())
	if exp.After(now.Add(subMaxExpiry)) {
		exp = now.Add(subMaxExpiry)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.subs[id]; sub != nil && sub.job == job { // renewal
		sub.expires = exp
		return
	}
	if len(s.subs) >= maxSubs {
		return
	}
	cur := map[string]logstore.Cursor{}
	for p, c := range m.GetCursors() {
		if len(cur) == 4096 {
			break
		}
		cur[p] = logstore.Cursor{Seq: c.GetSeq(), Attempt: c.GetAttempt()}
	}
	s.subs[id] = &liveSub{id: id, job: job, expires: exp, cursors: cur, pos: -1}
}

// Run sends chunks every 250 ms until ctx ends or a send fails (the stream is gone).
func (s *LiveStream) Run(ctx context.Context) error {
	t := time.NewTicker(chunkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		for _, c := range s.chunks() {
			if err := s.send(c); err != nil {
				return err
			}
		}
	}
}

// chunks builds the chunks of this tick, within the stream's byte budget.
func (s *LiveStream) chunks() []*workerv1.LiveChunk {
	now := s.logs.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = min(burstBytes, s.tokens+now.Sub(s.filled).Seconds()*rateBytes)
	s.filled = now
	var out []*workerv1.LiveChunk
	for id, sub := range s.subs {
		if now.After(sub.expires) {
			delete(s.subs, id)
			continue
		}
		j := s.logs.job(sub.job)
		if j == nil {
			continue
		}
		c, size := j.chunk(sub, int(s.tokens))
		if c == nil {
			continue
		}
		c.SubscriptionId = sub.id
		s.tokens -= float64(size)
		out = append(out, c)
	}
	return out
}

// lineSize is what one line adds to a chunk on the wire (proto field overhead included).
func lineSize(l logstore.Line) int { return len(l.Text) + len(l.Package) + 32 }

// chunk returns the next chunk for sub of at most budget bytes, or nil when there is nothing
// to send. The first chunk starts with the ring lines after the subscriber's cursors.
func (j *JobLog) chunk(sub *liveSub, budget int) (*workerv1.LiveChunk, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := &workerv1.LiveChunk{JobId: j.Job, Epoch: j.Epoch}
	size := 64
	for _, r := range j.restarts[sub.rsSent:] {
		c.Restarts = append(c.Restarts, &workerv1.LiveRestart{Package: r.Package, Attempt: r.Attempt, FromSeq: r.FromSeq})
		size += len(r.Package) + 24
	}
	sub.rsSent = len(j.restarts)
	if sub.pos < j.ringBase { // first chunk, or the subscriber fell behind the ring
		sub.pos = j.ringBase
	}
	limit := min(budget, logstore.MaxChunkBytes)
	end := j.ringBase + int64(len(j.ring))
	for sub.pos < end && len(c.Lines) < logstore.MaxChunkLines {
		l := j.ring[sub.pos-j.ringBase]
		if cur, ok := sub.cursors[l.Package]; ok && l.Seq <= cur.Seq && l.Attempt <= cur.Attempt {
			sub.pos++ // the orchestrator has it
			continue
		}
		n := lineSize(l)
		if size+n > limit {
			break
		}
		c.Lines = append(c.Lines, &workerv1.LiveLine{Package: l.Package, Seq: l.Seq, Attempt: l.Attempt,
			Stream: l.Stream, TsUnixNano: l.TS, Text: l.Text})
		size += n
		sub.pos++
	}
	if sub.pos == end { // end markers only after every line before them was sent
		for _, f := range j.finals[sub.finSent:] {
			c.Finals = append(c.Finals, &workerv1.LiveFinal{Package: f.Package, LastSeq: f.LastSeq, Attempt: f.Attempt})
			size += len(f.Package) + 24
		}
		sub.finSent = len(j.finals)
	}
	if len(c.Lines) == 0 && len(c.Restarts) == 0 && len(c.Finals) == 0 {
		return nil, 0
	}
	return c, max(size, proto.Size(c))
}
