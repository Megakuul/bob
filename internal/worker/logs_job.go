package worker

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
)

// Live buffer caps per job (SPEC §6.5.17): the last 20,000 lines or 8 MiB, whichever is smaller.
const (
	ringLines = 20000
	ringBytes = 8 << 20
	// endedKeep is how long the log of an ended job stays for live subscribers (its last chunk
	// and end markers).
	endedKeep = 30 * time.Second
	// lastSeqTimeout bounds the lake read of a retried job's last persisted seqs.
	lastSeqTimeout = 15 * time.Second
	// maxPartial bounds an unterminated output line held by a package writer.
	maxPartial = 64 << 10
)

// Logs is a worker's log pipeline (SPEC §05.10.2): every line goes to the lake writer (batched
// commits, R-LOG-001) and to the job's in-memory log, which live subscriptions read
// (R-LOG-002). The in-memory log is never blocked by S3 or by subscribers.
type Logs struct {
	W    *logstore.Writer
	R    *logstore.Reader // last persisted seqs of a retried job; nil: a retry starts at seq 1
	Now  func() time.Time
	Log  *slog.Logger
	Wid  string // worker id (package_run rows)
	mu   sync.Mutex
	jobs map[string]*JobLog
}

// NewLogs performs no I/O.
func NewLogs(w *logstore.Writer, r *logstore.Reader, wid string) *Logs {
	return &Logs{W: w, R: r, Wid: wid, Now: time.Now, Log: slog.Default(), jobs: map[string]*JobLog{}}
}

// JobLog is the log of one job attempt on this worker.
type JobLog struct {
	logs    *Logs
	Build   string
	Job     string
	Epoch   int64
	Attempt int32

	mu       sync.Mutex
	next     map[string]int64 // next seq per package
	start    map[string]int64 // seq the package continues at (attempt ≥ 2): last persisted + 1
	restarts []logstore.Restart
	finals   []logstore.Final
	ring     []logstore.Line
	ringBase int64 // absolute index of ring[0]
	ringSize int
	ended    time.Time
	changed  chan struct{}
}

// Start opens the log of a claimed job attempt. For attempt ≥ 2 the packages continue after the
// last persisted seq (prev, the previous attempt's JobResult.LogLast, or else a lake read), and
// each package's first line comes with a LiveRestart.
func (l *Logs) Start(ctx context.Context, buildID, jobID string, epoch int64, attempt int32, prev map[string]int64) (*JobLog, error) {
	if _, err := logstore.BuildTime(buildID); err != nil {
		return nil, err
	}
	j := &JobLog{logs: l, Build: buildID, Job: jobID, Epoch: epoch, Attempt: max(attempt, 1),
		next: map[string]int64{}, start: map[string]int64{}, changed: make(chan struct{})}
	if j.Attempt >= 2 {
		for p, s := range prev {
			j.start[p] = s + 1
		}
		if len(prev) == 0 && l.R != nil {
			rctx, cancel := context.WithTimeout(ctx, lastSeqTimeout)
			last, err := l.R.LastSeqs(rctx, buildID)
			cancel()
			if err != nil {
				// Starting at seq 1 is correct as well: the restart rule then voids the whole
				// previous attempt (from(n) = 1). Only the follower's prefix is lost.
				l.Log.Warn("worker: last persisted log seqs unknown; the retry restarts its packages at seq 1", "job", jobID, "err", err)
			}
			for p, c := range last {
				j.start[p] = c.Seq + 1
			}
		}
	}
	l.mu.Lock()
	l.jobs[jobID] = j
	l.gc()
	l.mu.Unlock()
	return j, nil
}

// gc drops the logs of jobs that ended long enough ago. Called with l.mu held.
func (l *Logs) gc() {
	now := l.Now()
	for id, j := range l.jobs {
		j.mu.Lock()
		old := !j.ended.IsZero() && now.Sub(j.ended) > endedKeep
		j.mu.Unlock()
		if old {
			delete(l.jobs, id)
		}
	}
}

func (l *Logs) job(id string) *JobLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc()
	return l.jobs[id]
}

func (j *JobLog) notify() {
	close(j.changed)
	j.changed = make(chan struct{})
}

// Line records one output line of a package: split into ≤ 4 KiB valid UTF-8 pieces, numbered
// with the package's next seqs, sent to the lake writer and kept in the live ring.
func (j *JobLog) Line(pkg string, stream int32, text string) {
	if pkg == "" || len(pkg) > logstore.MaxPackageBytes {
		return
	}
	now := j.logs.Now().UnixNano()
	parts := logstore.SplitText(text)
	lines := make([]logstore.Line, 0, len(parts))
	j.mu.Lock()
	seq, ok := j.next[pkg]
	if !ok {
		seq = 1
		if s, cont := j.start[pkg]; cont {
			seq = s
		}
		if j.Attempt >= 2 {
			j.restarts = append(j.restarts, logstore.Restart{Package: pkg, Attempt: j.Attempt, FromSeq: seq})
		}
	}
	for _, p := range parts {
		lines = append(lines, logstore.Line{Package: pkg, Seq: seq, Attempt: j.Attempt, Stream: stream, TS: now, Text: p})
		seq++
	}
	j.next[pkg] = seq
	for _, ln := range lines {
		j.ring = append(j.ring, ln)
		j.ringSize += len(ln.Text) + len(ln.Package)
	}
	for len(j.ring) > ringLines || j.ringSize > ringBytes {
		j.ringSize -= len(j.ring[0].Text) + len(j.ring[0].Package)
		j.ring[0] = logstore.Line{}
		j.ring = j.ring[1:]
		j.ringBase++
	}
	j.notify()
	j.mu.Unlock()
	if err := j.logs.W.Add(j.Build, "", lines...); err != nil {
		j.logs.Log.Error("worker: log line not queued", "job", j.Job, "err", err)
	}
}

// Writer returns an io.WriteCloser that turns a process's output into lines of pkg. Close
// flushes an unterminated last line.
func (j *JobLog) Writer(pkg string, stream int32) *LineWriter {
	return &LineWriter{j: j, pkg: pkg, stream: stream}
}

// EndPackage marks the end of a package's output: its end marker for live subscribers, a
// package_run row, and a commit soon (R-LOG-001 "at package end").
func (j *JobLog) EndPackage(pkg string, run *logstore.PackageRun) {
	j.mu.Lock()
	last := j.next[pkg] - 1
	if _, ok := j.next[pkg]; !ok {
		last = 0
	}
	j.finals = append(j.finals, logstore.Final{Package: pkg, LastSeq: last, Attempt: j.Attempt})
	j.notify()
	j.mu.Unlock()
	if run != nil {
		if err := j.logs.W.AddRun(*run); err != nil {
			j.logs.Log.Error("worker: package run not queued", "job", j.Job, "err", err)
		}
	}
	j.logs.W.Kick(j.Build)
}

// End commits everything the job logged (R-LOG-001 "at job end") and returns, per package, the
// last persisted seq for JobResult.LogLast. It waits for the commit until ctx ends; a commit
// that does not land in time leaves the lines in the writer, which keeps retrying.
func (j *JobLog) End(ctx context.Context) (map[string]int64, error) {
	err := j.logs.W.Flush(ctx, j.Build)
	j.mu.Lock()
	j.ended = j.logs.Now()
	pkgs := make([]string, 0, len(j.next))
	for p := range j.next {
		pkgs = append(pkgs, p)
	}
	j.notify()
	j.mu.Unlock()
	persisted := j.logs.W.Persisted(j.Build)
	out := map[string]int64{}
	for _, p := range pkgs {
		if s, ok := persisted[p]; ok {
			out[p] = s
		}
	}
	return out, err
}

// LineWriter splits a byte stream into lines of one package.
type LineWriter struct {
	j       *JobLog
	pkg     string
	stream  int32
	mu      sync.Mutex
	partial []byte
}

// Write never fails: log output must not stop a build.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.partial = append(w.partial, p...)
			if len(w.partial) >= maxPartial { // an endless line: emit what we have
				w.j.Line(w.pkg, w.stream, string(w.partial))
				w.partial = w.partial[:0]
			}
			break
		}
		w.partial = append(w.partial, p[:i]...)
		w.j.Line(w.pkg, w.stream, string(bytes.TrimSuffix(w.partial, []byte{'\r'})))
		w.partial = w.partial[:0]
		p = p[i+1:]
	}
	return n, nil
}

// Close emits an unterminated last line.
func (w *LineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.j.Line(w.pkg, w.stream, string(w.partial))
		w.partial = nil
	}
	return nil
}

// LogConfig is the log.* configuration of a worker process (SPEC §05.11.1).
type LogConfig struct {
	Storage       store.StorageConfig // endpoint, region, analytics bucket
	Wid           string
	FlushInterval time.Duration // log.flush_interval (BOB_LOG_FLUSH_INTERVAL), 0 = 30 s
	Now           func() time.Time
	Log           *slog.Logger
}

// NewProcessLogs builds the log pipeline of a worker process over the analytics bucket (the
// worker credential from the SDK default chain). Run the result as a server.Component (it runs
// the lake writer) and hand it to the job runner (Start) and to every Attach stream (Stream).
func NewProcessLogs(ctx context.Context, cfg LogConfig) (*Logs, error) {
	c, err := s3client.New(ctx, s3client.Options{Endpoint: cfg.Storage.Endpoint, Region: cfg.Storage.Region})
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	w := logstore.NewWriter(logstore.WriterConfig{Client: c, Bucket: cfg.Storage.Analytics,
		FlushInterval: cfg.FlushInterval, Now: now, Log: cfg.Log})
	l := NewLogs(w, logstore.NewReader(c, cfg.Storage.Analytics), cfg.Wid)
	l.Now = now
	if cfg.Log != nil {
		l.Log = cfg.Log
	}
	return l, nil
}

// Name implements server.Component.
func (l *Logs) Name() string { return "log writer" }

// Run implements server.Component: the lake writer's commit loop, with a last flush on stop.
func (l *Logs) Run(ctx context.Context) error { return l.W.Run(ctx) }
