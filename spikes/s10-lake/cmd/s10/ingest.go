package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() {
	experiments["ingest"] = ingest
}

type jobKey struct{ build, pkg string }

type job struct {
	jobKey
	drv     string
	lines   int
	dur     time.Duration
	emitted atomic.Int64
	done    atomic.Bool
}

type samples struct {
	mu sync.Mutex
	d  map[string][]time.Duration
	n  map[string]int64
}

func newSamples() *samples { return &samples{d: map[string][]time.Duration{}, n: map[string]int64{}} }
func (s *samples) add(k string, d time.Duration) {
	s.mu.Lock()
	s.d[k] = append(s.d[k], d)
	s.mu.Unlock()
}
func (s *samples) inc(k string, n int64) {
	s.mu.Lock()
	s.n[k] += n
	s.mu.Unlock()
}

func catalogInfo(ctx context.Context, cl *s3.Client, bucket string) (size int, shards int, smallShards int, minSize int) {
	o, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("lakedb.catalog")})
	if err != nil {
		return -1, -1, -1, 0
	}
	data, _ := io.ReadAll(o.Body)
	o.Body.Close()
	var c struct {
		Tables map[string]struct {
			Shards []struct {
				Size int `json:"size"`
			} `json:"shards"`
		} `json:"tables"`
	}
	json.Unmarshal(data, &c)
	for _, t := range c.Tables {
		shards += len(t.Shards)
		for _, s := range t.Shards {
			if s.Size < minSize {
				smallShards++
			}
		}
	}
	return len(data), shards, smallShards, minSize
}

func ingest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	ep := endpointFlag(fs)
	bucketName := fs.String("bucket", "s10-ingest", "bucket")
	workers := fs.Int("workers", 10, "simulated workers (each its own lake.Bucket + Ingestor)")
	jobsPerWorker := fs.Int("jobs", 2, "concurrent package builds per worker")
	flushEvery := fs.Duration("flush", 2*time.Second, "worker flush interval")
	flushLines := fs.Int("flushlines", 5000, "flush early when this many lines are buffered")
	duration := fs.Duration("dur", 60*time.Second, "ingest duration")
	linesPerPkg := fs.Int("lines", 2000, "lines per package build")
	pkgDurMin := fs.Duration("pkgmin", 10*time.Second, "min package build time")
	pkgDurMax := fs.Duration("pkgmax", 40*time.Second, "max package build time")
	compactors := fs.Int("compactors", 1, "orchestrators running the compactor (own lake.Bucket each)")
	compactEvery := fs.Duration("compact", 15*time.Second, "compaction interval per orchestrator")
	compactSize := fs.Int("compactsize", 32_000_000, "lake WithCompactionSize")
	followers := fs.Int("followers", 4, "UI/CLI followers tailing a running package (fresh catalog per poll)")
	followEvery := fs.Duration("follow", 2*time.Second, "follower poll interval")
	uiReaders := fs.Int("uireaders", 2, "readers with a long-lived lake.Bucket querying finished packages")
	verify := fs.Bool("verify", true, "verify every line exactly once at the end")
	fs.Parse(args)

	wc, cc, rc := logstore.NewCounter(), logstore.NewCounter(), logstore.NewCounter()
	admin := logstore.Client(endpoint(*ep), nil)
	if err := logstore.FreshBucket(ctx, admin, *bucketName); err != nil {
		return err
	}
	// create the catalog once
	if _, err := lake.NewFromClient(ctx, admin, *bucketName); err != nil {
		return err
	}

	r0 := rand.New(rand.NewPCG(7, 7))
	pkgs := logstore.Pkgs(50)
	jobs := make(chan *job, 1<<16)
	var allMu sync.Mutex
	all := map[jobKey]*job{}
	var active sync.Map // jobKey -> *job
	// enqueue enough builds for the duration
	nBuilds := 0
	for nBuilds*50 < (*workers)*(*jobsPerWorker)*int((*duration)/(*pkgDurMin)+2) {
		id := logstore.BuildID(time.Now().Add(time.Duration(nBuilds)*time.Millisecond), r0)
		for _, p := range pkgs {
			j := &job{jobKey: jobKey{id, p}, drv: logstore.Drv(r0, p), lines: *linesPerPkg,
				dur: *pkgDurMin + time.Duration(r0.Int64N(int64(*pkgDurMax-*pkgDurMin)+1))}
			jobs <- j
			all[j.jobKey] = j
		}
		nBuilds++
	}

	st := newSamples()
	runCtx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	var wg sync.WaitGroup
	var rawBytes atomic.Int64
	var lastErr sync.Map
	start := time.Now()

	// ---- workers
	var workerWG sync.WaitGroup
	for w := range *workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			r := rand.New(rand.NewPCG(uint64(w), 99))
			b, err := lake.NewFromClient(ctx, logstore.Client(endpoint(*ep), wc), *bucketName)
			if err != nil {
				lastErr.Store("worker-init", err.Error())
				return
			}
			in := lake.NewIngestor[logstore.LogLine](b)
			var bufMu sync.Mutex
			var buf []logstore.LogLine
			kick := make(chan struct{}, 1)
			flush := func() {
				bufMu.Lock()
				rows := buf
				buf = nil
				bufMu.Unlock()
				t0 := time.Now()
				if len(rows) > 0 {
					logstore.SortLines(rows)
					if err := in.Insert(ctx, rows...); err != nil {
						st.inc("insert_err", 1)
						lastErr.Store("insert", err.Error())
					}
				}
				if err := in.Commit(ctx); err != nil {
					st.inc("commit_err", 1)
					lastErr.Store("commit", err.Error())
				} else if len(rows) > 0 {
					st.inc("commit_ok", 1)
				}
				st.add("commit", time.Since(t0))
			}
			// flusher
			flDone := make(chan struct{})
			go func() {
				defer close(flDone)
				t := time.NewTicker(*flushEvery)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						flush()
					case <-kick:
						flush()
					case <-runCtx.Done():
						flush()        // final flush on clean shutdown
						for range 10 { // drain backlog of failed commits
							bufMu.Lock()
							n := len(buf)
							bufMu.Unlock()
							if n == 0 {
								if err := in.Commit(ctx); err == nil {
									break
								}
							}
							flush()
						}
						return
					}
				}
			}()
			var jwg sync.WaitGroup
			for range *jobsPerWorker {
				jwg.Add(1)
				go func() {
					defer jwg.Done()
					for {
						if runCtx.Err() != nil {
							return
						}
						var j *job
						select {
						case j = <-jobs:
						default:
							return
						}
						active.Store(j.jobKey, j)
						tick := 50 * time.Millisecond
						steps := int(j.dur / tick)
						per := float64(j.lines) / float64(steps)
						acc := 0.0
						for s := 0; s < steps && runCtx.Err() == nil; s++ {
							time.Sleep(tick)
							acc += per
							n := int(acc) - int(j.emitted.Load())
							if s == steps-1 {
								n = j.lines - int(j.emitted.Load())
							}
							now := time.Now().UnixNano()
							rows := make([]logstore.LogLine, 0, n)
							for range n {
								seq := j.emitted.Add(1)
								line := logstore.Line(r, j.pkg, seq)
								rawBytes.Add(int64(len(line)))
								rows = append(rows, logstore.FromPlain(logstore.Plain{BuildID: j.build, Package: j.pkg, Drv: j.drv, Attempt: 1, Stream: 1, Seq: seq, TS: now, Line: line}))
							}
							bufMu.Lock()
							buf = append(buf, rows...)
							over := len(buf) >= *flushLines
							bufMu.Unlock()
							if over {
								select {
								case kick <- struct{}{}:
								default:
								}
							}
						}
						if j.emitted.Load() == int64(j.lines) {
							j.done.Store(true)
						}
						active.Delete(j.jobKey)
					}
				}()
			}
			jwg.Wait()
			<-flDone
		}()
	}

	// ---- compactors
	for o := range *compactors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := lake.NewFromClient(ctx, logstore.Client(endpoint(*ep), cc), *bucketName)
			if err != nil {
				lastErr.Store("compactor-init", err.Error())
				return
			}
			c := lake.NewCompactor[logstore.LogLine](b, append(compactorOpts[logstore.LogLine](), lake.WithCompactionSize[logstore.LogLine](*compactSize))...)
			// stagger
			time.Sleep(time.Duration(o) * (*compactEvery) / time.Duration(*compactors))
			t := time.NewTicker(*compactEvery)
			defer t.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-t.C:
				}
				t0 := time.Now()
				err := c.Compact(ctx)
				d := time.Since(t0)
				switch {
				case err == nil:
					st.inc("compact_ok", 1)
					st.add("compact_ok", d)
				case errors.Is(err, lake.ErrOptimisticLock):
					st.inc("compact_lockfail", 1)
					st.add("compact_lockfail", d)
				default:
					st.inc("compact_err", 1)
					lastErr.Store("compact", err.Error())
				}
			}
		}()
	}

	// ---- followers: fresh catalog per poll (lake has no exported reload; NewFromClient loads it)
	for f := range *followers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl := logstore.Client(endpoint(*ep), rc)
			var cur *job
			var last int64
			var prevPoll time.Time
			r := rand.New(rand.NewPCG(uint64(f), 5))
			t := time.NewTicker(*followEvery)
			defer t.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-t.C:
				}
				if cur == nil || cur.done.Load() && last >= int64(cur.lines) {
					cur, last = nil, 0
					var cands []*job
					active.Range(func(k, v any) bool { cands = append(cands, v.(*job)); return len(cands) < 64 })
					if len(cands) == 0 {
						continue
					}
					cur = cands[r.IntN(len(cands))]
					prevPoll = time.Time{}
				}
				t0 := time.Now()
				pp := prevPoll
				prevPoll = t0
				b, err := lake.NewFromClient(ctx, cl, *bucketName)
				if err != nil {
					st.inc("follow_err", 1)
					lastErr.Store("follow", err.Error())
					continue
				}
				rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{
					BuildID: lake.FilterString(lake.Eq(cur.build)),
					Package: lake.FilterString(lake.Eq(cur.pkg)),
					Seq:     lake.FilterInt(lake.Gt(last)),
				}).Scan(ctx, b)
				now := time.Now()
				st.add("follow_poll", now.Sub(t0))
				if err != nil {
					st.inc("follow_err", 1)
					lastErr.Store("follow", err.Error())
					continue
				}
				sort.Slice(rows, func(i, k int) bool { return rows[i].Seq.Data < rows[k].Seq.Data })
				for _, row := range rows {
					if row.Seq.Data != last+1 {
						st.inc("follow_gap", 1)
					}
					last = row.Seq.Data
					// only lines produced after the previous poll started: their age is the visibility delay
					if !pp.IsZero() && row.TS >= pp.UnixNano() {
						st.add("follow_visible_lag", now.Sub(time.Unix(0, row.TS)))
					}
				}
			}
		}()
	}

	// ---- UI readers: long-lived bucket (lake caches the catalog for 1 minute)
	for u := range *uiReaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := lake.NewFromClient(ctx, logstore.Client(endpoint(*ep), rc), *bucketName, readerOpts()...)
			if err != nil {
				return
			}
			r := rand.New(rand.NewPCG(uint64(u), 11))
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-t.C:
				}
				allMu.Lock()
				var cands []*job
				for _, j := range all {
					if j.done.Load() {
						cands = append(cands, j)
						if len(cands) > 200 {
							break
						}
					}
				}
				allMu.Unlock()
				if len(cands) == 0 {
					continue
				}
				j := cands[r.IntN(len(cands))]
				t0 := time.Now()
				_, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{
					BuildID: lake.FilterString(lake.Eq(j.build)),
					Package: lake.FilterString(lake.Eq(j.pkg)),
				}).Scan(ctx, b)
				st.add("ui_query", time.Since(t0))
				if err != nil {
					st.inc("ui_err", 1)
					if strings.Contains(err.Error(), "NoSuchKey") {
						st.inc("ui_err_nosuchkey", 1)
					}
					lastErr.Store("ui", err.Error())
				} else {
					st.inc("ui_ok", 1)
				}
			}
		}()
	}

	// ---- monitor
	type mon struct {
		T                           float64
		CatalogBytes, Shards, Small int
	}
	var mons []mon
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
			}
			sz, sh, small, _ := catalogInfo(ctx, admin, *bucketName)
			_ = small
			mons = append(mons, mon{time.Since(start).Seconds(), sz, sh, 0})
		}
	}()

	workerWG.Wait()
	wg.Wait()
	elapsed := time.Since(start)

	// totals
	var emitted int64
	for _, j := range all {
		emitted += j.emitted.Load()
	}
	fmt.Printf("== ingest lake=%s: workers=%d jobs/worker=%d flush=%s flushlines=%d dur=%s compactors=%d every=%s size=%d followers=%d uireaders=%d s3=%s\n",
		lakeVariant, *workers, *jobsPerWorker, *flushEvery, *flushLines, *duration, *compactors, *compactEvery, *compactSize, *followers, *uiReaders, *ep)
	fmt.Printf("elapsed=%s lines=%d (%.0f lines/s) raw=%.1fMB\n", elapsed.Round(time.Millisecond), emitted, float64(emitted)/elapsed.Seconds(), float64(rawBytes.Load())/1e6)
	fmt.Printf("worker S3: %s\n", wc)
	fmt.Printf("compactor S3: %s\n", cc)
	fmt.Printf("reader S3: %s\n", rc)
	keys := []string{}
	for k := range st.n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("count %s=%d\n", k, st.n[k])
	}
	dk := []string{}
	for k := range st.d {
		dk = append(dk, k)
	}
	sort.Strings(dk)
	for _, k := range dk {
		d := st.d[k]
		fmt.Printf("latency %-20s n=%-6d p50=%-9s p90=%-9s p99=%-9s max=%s\n", k, len(d), ms(pct(d, .5)), ms(pct(d, .9)), ms(pct(d, .99)), ms(pct(d, 1)))
	}
	for _, m := range mons {
		{
			fmt.Printf("t=%3.0fs catalog=%dB shards=%d\n", m.T, m.CatalogBytes, m.Shards)
		}
	}
	lastErr.Range(func(k, v any) bool { fmt.Printf("last error %s: %v\n", k, v); return true })

	objs, err := logstore.ListObjects(ctx, admin, *bucketName)
	if err != nil {
		return err
	}
	var objBytes int64
	for _, s := range objs {
		objBytes += s
	}
	sz, sh, _, _ := catalogInfo(ctx, admin, *bucketName)
	fmt.Printf("end: objects=%d bytes=%.1fMB catalog=%dB catalog_shards=%d orphans=%d\n", len(objs), float64(objBytes)/1e6, sz, sh, len(objs)-1-sh)

	if !*verify {
		return nil
	}
	// final compaction (single compactor) then verify exactly-once
	b, err := lake.NewFromClient(ctx, admin, *bucketName)
	if err != nil {
		return err
	}
	rows, err := lake.Query[logstore.LogLine]().Scan(ctx, b)
	if err != nil {
		return fmt.Errorf("verify scan: %w", err)
	}
	seen := map[jobKey]map[int64]int{}
	for _, row := range rows {
		k := jobKey{row.BuildID.Data, row.Package.Data}
		if seen[k] == nil {
			seen[k] = map[int64]int{}
		}
		seen[k][row.Seq.Data]++
	}
	var dup, missing, extra int64
	for k, j := range all {
		n := j.emitted.Load()
		for s := int64(1); s <= n; s++ {
			switch c := seen[k][s]; {
			case c == 0:
				missing++
			case c > 1:
				dup += int64(c - 1)
			}
		}
		for s := range seen[k] {
			if s > n {
				extra++
			}
		}
	}
	fmt.Printf("verify: rows=%d emitted=%d missing=%d duplicates=%d extra=%d\n", len(rows), emitted, missing, dup, extra)
	return nil
}
