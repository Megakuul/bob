package logstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/lake"
)

// Writer defaults (SPEC §05.10.3 R-LOG-001, §05.11.1 log.*).
const (
	DefaultFlushInterval = 30 * time.Second
	DefaultCommitLines   = 10000
	DefaultCommitBytes   = 2 << 20
	// DefaultMaxPending bounds the uncommitted bytes of one writer. Lines stay in memory until
	// a commit succeeds; a store that refuses commits for a long time must not exhaust the
	// worker's memory, so beyond this the oldest uncommitted lines are dropped and counted.
	DefaultMaxPending = 256 << 20
	// partitionBudget is the commit budget of one partition (commits/s, R-LOG-001).
	partitionBudget = 5
	rowOverhead     = 64 // bytes counted per row on top of its text
	maxBackoff      = 30 * time.Second
)

// WriterConfig configures a Writer.
type WriterConfig struct {
	Client *s3.Client // any client of the storage endpoint; the analytics bucket is a prefix
	Bucket string     // storage.buckets.analytics
	Shards int        // log.shards (4)

	FlushInterval time.Duration // log.flush_interval (30 s)
	CommitLines   int           // 10,000
	CommitBytes   int           // 2 MiB
	MaxPending    int           // 256 MiB
	// ActiveWorkers is the number of workers writing to the same partitions; the interval
	// grows to max(FlushInterval, ActiveWorkers / 5 s) so a partition stays within its budget
	// of 5 commits/s. nil = 1.
	ActiveWorkers func() int
	Now           func() time.Time
	Log           *slog.Logger
}

// Writer batches log lines, package runs and job runs in memory and commits them to their
// lake partitions: on the interval, at CommitLines / CommitBytes, and when asked (package end,
// job end). It never blocks a caller of Add on S3. Lines survive until their batch commits;
// a crash loses only the lines after the last successful commit (R-LOG-001).
type Writer struct {
	cfg    WriterConfig
	client *s3.Client
	err    error

	mu      sync.Mutex
	parts   map[string]*partition
	pending int                        // uncommitted bytes over all partitions
	last    map[string]map[string]int64 // build → package → last committed seq
	kick    chan struct{}

	dropped   atomic.Int64 // lines dropped by the memory bound
	commits   atomic.Int64
	failures  atomic.Int64
	committed atomic.Int64 // lines committed
}

// partition is the uncommitted data of one lake partition.
type partition struct {
	name   string
	lines  []LogLine
	runs   []PackageRun
	jobs   []JobRun
	bytes  int
	since  time.Time // when the oldest uncommitted row arrived
	due    time.Time // next commit attempt after a failure
	fails  int
	urgent bool // a size trigger or an explicit flush asked for a commit
	// inflight is the prefix of lines a running commit sends; bound never drops it.
	inflight int

	commit sync.Mutex // one commit at a time per partition
	bucket *lake.Bucket
}

// NewWriter performs no I/O.
func NewWriter(cfg WriterConfig) *Writer {
	if cfg.Shards < 1 {
		cfg.Shards = DefaultLogShards
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.CommitLines <= 0 {
		cfg.CommitLines = DefaultCommitLines
	}
	if cfg.CommitBytes <= 0 {
		cfg.CommitBytes = DefaultCommitBytes
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = DefaultMaxPending
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	w := &Writer{cfg: cfg, parts: map[string]*partition{}, last: map[string]map[string]int64{}, kick: make(chan struct{}, 1)}
	if cfg.Client == nil {
		w.err = errors.New("logstore: writer has no S3 client")
	} else {
		w.client, w.err = PrefixClient(cfg.Client, cfg.Bucket, nil)
	}
	return w
}

// Stats are counters of a writer (metrics, tests).
type Stats struct {
	Commits, Failures, Committed, Dropped int64
	Pending                               int // uncommitted bytes
}

// Stats returns the writer's counters.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	p := w.pending
	w.mu.Unlock()
	return Stats{Commits: w.commits.Load(), Failures: w.failures.Load(), Committed: w.committed.Load(),
		Dropped: w.dropped.Load(), Pending: p}
}

// Interval is the current commit interval: max(flush_interval, active workers / 5 s).
func (w *Writer) Interval() time.Duration {
	n := 1
	if w.cfg.ActiveWorkers != nil {
		n = max(1, w.cfg.ActiveWorkers())
	}
	return max(w.cfg.FlushInterval, time.Duration(n)*time.Second/partitionBudget)
}

func (w *Writer) part(name string) *partition {
	p := w.parts[name]
	if p == nil {
		p = &partition{name: name}
		w.parts[name] = p
	}
	return p
}

func (w *Writer) signal() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Add queues lines of one build. Text must already be split (SplitText); seq and attempt are
// the caller's (per build and package, SPEC §05.10.2).
func (w *Writer) Add(buildID, drv string, lines ...Line) error {
	name, err := LogPartition(buildID, w.cfg.Shards)
	if err != nil {
		return err
	}
	now := w.cfg.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.part(name)
	if len(p.lines) == 0 && len(p.runs) == 0 && len(p.jobs) == 0 {
		p.since = now
	}
	for _, l := range lines {
		p.lines = append(p.lines, LogLine{BuildID: lake.NewString(buildID), Package: lake.NewString(l.Package),
			Seq: lake.NewInt(l.Seq), Drv: drv, Attempt: int64(l.Attempt), Stream: int64(l.Stream), TS: l.TS, Line: l.Text})
		n := len(l.Text) + len(l.Package) + rowOverhead
		p.bytes += n
		w.pending += n
	}
	if len(p.lines) >= w.cfg.CommitLines || p.bytes >= w.cfg.CommitBytes {
		p.urgent = true
		w.signal()
	}
	w.bound()
	return nil
}

// AddRun queues one package_run row (into the build's run partition).
func (w *Writer) AddRun(r PackageRun) error {
	name, err := RunPartition(r.BuildID.Data)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.part(name)
	if len(p.runs) == 0 {
		p.since = w.cfg.Now()
	}
	p.runs = append(p.runs, r)
	p.bytes += rowOverhead * 4
	w.pending += rowOverhead * 4
	return nil
}

// AddJobRun queues one job_run row of a job finalised at t.
func (w *Writer) AddJobRun(t time.Time, r JobRun) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.part(JobPartition(t))
	if len(p.jobs) == 0 {
		p.since = w.cfg.Now()
	}
	p.jobs = append(p.jobs, r)
	p.bytes += rowOverhead * 2
	w.pending += rowOverhead * 2
}

// bound drops the oldest uncommitted lines of the biggest partition while the writer holds
// more than MaxPending bytes. Called with w.mu held.
func (w *Writer) bound() {
	for w.pending > w.cfg.MaxPending {
		var big *partition
		for _, p := range w.parts {
			if big == nil || p.bytes > big.bytes {
				big = p
			}
		}
		if big == nil || len(big.lines) <= big.inflight {
			return
		}
		n := max(1, (len(big.lines)-big.inflight)/10)
		for _, l := range big.lines[big.inflight : big.inflight+n] {
			b := len(l.Line) + len(l.Package.Data) + rowOverhead
			big.bytes -= b
			w.pending -= b
		}
		big.lines = slices.Delete(big.lines, big.inflight, big.inflight+n)
		w.dropped.Add(int64(n))
		w.cfg.Log.Error("logstore: uncommitted log lines dropped: the analytics bucket refuses commits and the buffer is full",
			"partition", big.name, "lines", n)
	}
}

// Persisted returns, per package of a build, the last seq a successful commit stored
// (JobResult.LogLast).
func (w *Writer) Persisted(buildID string) map[string]int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int64, len(w.last[buildID]))
	for p, s := range w.last[buildID] {
		out[p] = s
	}
	return out
}

// Forget drops what the writer remembers about a build (after its job ended).
func (w *Writer) Forget(buildID string) {
	w.mu.Lock()
	delete(w.last, buildID)
	w.mu.Unlock()
}

// Kick asks for a commit of the build's partitions soon (package end).
func (w *Writer) Kick(buildID string) {
	w.mu.Lock()
	for _, name := range w.partsOf(buildID) {
		if p := w.parts[name]; p != nil {
			p.urgent = true
		}
	}
	w.mu.Unlock()
	w.signal()
}

func (w *Writer) partsOf(buildID string) []string {
	var out []string
	if n, err := LogPartition(buildID, w.cfg.Shards); err == nil {
		out = append(out, n)
	}
	if n, err := RunPartition(buildID); err == nil {
		out = append(out, n)
	}
	return out
}

// Flush commits the build's partitions now (job end) and returns when every row queued
// before the call is committed, or when ctx ends. A failed commit is retried with backoff.
func (w *Writer) Flush(ctx context.Context, buildID string) error {
	if w.err != nil {
		return w.err
	}
	for _, name := range w.partsOf(buildID) {
		if err := w.flushPartition(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// FlushAll commits every partition (shutdown, job runs).
func (w *Writer) FlushAll(ctx context.Context) error {
	w.mu.Lock()
	names := make([]string, 0, len(w.parts))
	for n := range w.parts {
		names = append(names, n)
	}
	w.mu.Unlock()
	var errs []error
	for _, n := range names {
		errs = append(errs, w.flushPartition(ctx, n))
	}
	return errors.Join(errs...)
}

func (w *Writer) flushPartition(ctx context.Context, name string) error {
	w.mu.Lock()
	p := w.parts[name]
	w.mu.Unlock()
	if p == nil {
		return nil
	}
	backoff := 250 * time.Millisecond
	for {
		done, err := w.commitPartition(ctx, p, true)
		if done {
			return nil
		}
		w.cfg.Log.Warn("logstore: commit failed, retrying", "partition", name, "err", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("logstore: flush %s: %w (last error: %w)", name, ctx.Err(), err)
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Second)
	}
}

// Run commits on the interval and on size triggers until ctx ends; then it tries one last
// flush of everything with a short deadline.
func (w *Writer) Run(ctx context.Context) error {
	if w.err != nil {
		return w.err
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = w.FlushAll(fctx)
			return nil
		case <-tick.C:
		case <-w.kick:
		}
		w.commitDue(ctx)
	}
}

// commitDue commits every partition whose interval elapsed or that a trigger marked urgent.
func (w *Writer) commitDue(ctx context.Context) {
	now := w.cfg.Now()
	interval := w.Interval()
	w.mu.Lock()
	var due []*partition
	for _, p := range w.parts {
		if len(p.lines) == 0 && len(p.runs) == 0 && len(p.jobs) == 0 {
			continue
		}
		if now.Before(p.due) {
			continue
		}
		// ± 10 % jitter keeps workers that started together from committing together.
		jit := time.Duration(float64(interval) * (0.9 + 0.2*rand.Float64()))
		if p.urgent || now.Sub(p.since) >= jit {
			due = append(due, p)
		}
	}
	w.mu.Unlock()
	for _, p := range due {
		if ctx.Err() != nil {
			return
		}
		if ok, err := w.commitPartition(ctx, p, false); !ok && err != nil {
			w.cfg.Log.Warn("logstore: commit failed, will retry", "partition", p.name, "err", err)
		}
	}
}

// commitPartition commits what p holds now. done reports that nothing queued before the call
// is left uncommitted. A failure keeps the rows and schedules the next try with backoff
// (unless force, which the caller paces).
func (w *Writer) commitPartition(ctx context.Context, p *partition, force bool) (done bool, err error) {
	p.commit.Lock()
	defer p.commit.Unlock()

	w.mu.Lock()
	lines := slices.Clone(p.lines)
	runs := slices.Clone(p.runs)
	jobs := slices.Clone(p.jobs)
	p.inflight = len(lines)
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		p.inflight = 0
		w.mu.Unlock()
	}()
	if len(lines) == 0 && len(runs) == 0 && len(jobs) == 0 {
		return true, nil
	}

	if p.bucket == nil {
		if p.bucket, err = openPartition(ctx, w.client, p.name); err != nil {
			p.bucket = nil
			w.failed(p, force)
			return false, err
		}
	}
	slices.SortFunc(lines, func(a, b LogLine) int {
		return cmp.Or(strings.Compare(a.BuildID.Data, b.BuildID.Data), strings.Compare(a.Package.Data, b.Package.Data),
			cmp.Compare(a.Seq.Data, b.Seq.Data), cmp.Compare(a.Attempt, b.Attempt))
	})
	if len(lines) > 0 {
		err = commitRows(ctx, p.bucket, lines)
	}
	if err == nil && len(runs) > 0 {
		err = commitRows(ctx, p.bucket, runs)
	}
	if err == nil && len(jobs) > 0 {
		err = commitRows(ctx, p.bucket, jobs)
	}
	if err != nil {
		if !isLockFailure(err) {
			// Ambiguous: the shard may have landed. The batch is retried; readers
			// deduplicate by (build, package, attempt, seq).
			p.bucket = nil // reload the catalog next time
		}
		w.failed(p, force)
		return false, err
	}

	w.commits.Add(1)
	w.committed.Add(int64(len(lines)))
	w.mu.Lock()
	defer w.mu.Unlock()
	// Rows added during the commit are behind the committed prefix.
	sent := 0
	for _, l := range p.lines[:len(lines)] {
		sent += len(l.Line) + len(l.Package.Data) + rowOverhead
		m := w.last[l.BuildID.Data]
		if m == nil {
			m = map[string]int64{}
			w.last[l.BuildID.Data] = m
		}
		if l.Seq.Data > m[l.Package.Data] {
			m[l.Package.Data] = l.Seq.Data
		}
	}
	sent += rowOverhead * (4*len(runs) + 2*len(jobs))
	p.lines = slices.Delete(p.lines, 0, len(lines))
	p.runs = slices.Delete(p.runs, 0, len(runs))
	p.jobs = slices.Delete(p.jobs, 0, len(jobs))
	p.bytes -= sent
	w.pending -= sent
	p.fails, p.due, p.urgent = 0, time.Time{}, false
	p.since = w.cfg.Now()
	return true, nil
}

// failed schedules the next commit of p with exponential backoff.
func (w *Writer) failed(p *partition, force bool) {
	w.failures.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	p.fails++
	if !force {
		d := min(maxBackoff, time.Duration(1<<min(p.fails, 5))*500*time.Millisecond)
		p.due = w.cfg.Now().Add(d)
	}
}

// commitRows writes one batch as one shard with a fresh ingestor: lake's ingestor keeps
// failed shards in a backlog of its own that drops entries silently (S10 F6), so bob keeps the
// rows itself and builds the shard again on a retry.
func commitRows[T any](ctx context.Context, b *lake.Bucket, rows []T) error {
	_, err := guard(func() (struct{}, error) {
		in := lake.NewIngestor[T](b)
		if err := in.Insert(ctx, rows...); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, in.Commit(ctx)
	})
	return err
}
