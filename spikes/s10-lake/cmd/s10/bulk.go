package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() { experiments["bulk"] = bulk }

// buildSpec deterministically describes simulated build i (shared by bulk and qbench).
type buildSpec struct {
	id    string
	start time.Time
}

func buildSpecs(n int, span time.Duration, end time.Time) []buildSpec {
	r := rand.New(rand.NewPCG(42, 42))
	out := make([]buildSpec, n)
	begin := end.Add(-span)
	for i := range n {
		t := begin.Add(time.Duration(float64(span) * float64(i) / float64(n)))
		out[i] = buildSpec{logstore.BuildID(t, r), t}
	}
	return out
}

// bulk loads builds x pkgs x lines log lines plus one package_run row per package build.
// Shards are written at the size lake's compactor would produce (buildsPerShard builds each),
// which is the steady state after compaction.
func bulk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bulk", flag.ExitOnError)
	ep := endpointFlag(fs)
	bucketName := fs.String("bucket", "s10-bulk", "bucket")
	builds := fs.Int("builds", 1000, "builds")
	npk := fs.Int("pkgs", 50, "packages per build")
	lines := fs.Int("lines", 2000, "lines per package")
	perShard := fs.Int("pershard", 6, "builds per shard (6 x 100k lines ~ 40 MB > 32 MB compaction size)")
	par := fs.Int("par", 4, "parallel loaders")
	fs.Parse(args)
	cnt := logstore.NewCounter()
	cl := logstore.Client(endpoint(*ep), cnt)
	if err := logstore.FreshBucket(ctx, cl, *bucketName); err != nil {
		return err
	}
	b, err := lake.NewFromClient(ctx, cl, *bucketName)
	if err != nil {
		return err
	}
	end := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	specs := buildSpecs(*builds, 90*24*time.Hour, end)
	pkgs := logstore.Pkgs(*npk)
	// per-package base duration 5 s .. 10 min
	base := make([]float64, *npk)
	for i := range base {
		base[i] = 5 * math.Pow(120, float64(i)/float64(*npk-1))
	}
	t0 := time.Now()
	var rowsDone, rawBytes atomic.Int64
	var runsMu sync.Mutex
	var runs []logstore.PackageRun
	work := make(chan int)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	for g := range *par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(g), 1234))
			for chunk := range work {
				in := lake.NewIngestor[logstore.LogLine](b)
				for bi := chunk; bi < min(chunk+*perShard, *builds); bi++ {
					sp := specs[bi]
					clock := sp.start
					var batch []logstore.LogLine
					for pi, p := range pkgs {
						drv := logstore.Drv(r, p)
						dur := time.Duration(base[pi] * (0.7 + 0.6*r.Float64()) * float64(time.Second))
						status := "ok"
						if r.IntN(100) < 3 {
							status = "failed"
						}
						st := clock
						for s := int64(1); s <= int64(*lines); s++ {
							line := logstore.Line(r, p, s)
							rawBytes.Add(int64(len(line)))
							stream := int64(1)
							if r.IntN(10) == 0 {
								stream = 2
							}
							batch = append(batch, logstore.FromPlain(logstore.Plain{BuildID: sp.id, Package: p, Drv: drv, Attempt: 1, Stream: stream, Seq: s,
								TS: st.Add(time.Duration(float64(dur) * float64(s) / float64(*lines))).UnixNano(), Line: line}))
						}
						runsMu.Lock()
						runs = append(runs, logstore.PackageRun{BuildID: lake.NewString(sp.id), Project: lake.NewString(fmt.Sprintf("proj%d", bi%5)), Package: lake.NewString(p),
							Drv: lake.NewString(drv), Worker: lake.NewString(fmt.Sprintf("w%02d", r.IntN(20))), Status: lake.NewString(status),
							Start: lake.NewInt(st.UnixNano()), End: lake.NewInt(st.Add(dur).UnixNano()), DurationMs: lake.NewInt(dur.Milliseconds()), Lines: lake.NewInt(*lines)})
						runsMu.Unlock()
						clock = clock.Add(dur / 4) // packages overlap
					}
					if err := in.Insert(ctx, batch...); err != nil {
						firstErr.CompareAndSwap(nil, err)
					}
					rowsDone.Add(int64(len(batch)))
				}
				if err := in.Commit(ctx); err != nil {
					firstErr.CompareAndSwap(nil, err)
				}
				fmt.Printf("\rloaded %d rows (%.0f rows/s)   ", rowsDone.Load(), float64(rowsDone.Load())/time.Since(t0).Seconds())
			}
		}()
	}
	for c := 0; c < *builds; c += *perShard {
		work <- c
	}
	close(work)
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return e.(error)
	}
	fmt.Println()
	// analytics rows: one commit per 100 builds (like periodic small writes, then not compacted)
	ri := lake.NewIngestor[logstore.PackageRun](b)
	for i, run := range runs {
		ri.Insert(ctx, run)
		if (i+1)%(100**npk) == 0 {
			if err := ri.Commit(ctx); err != nil {
				return err
			}
		}
	}
	if err := ri.Commit(ctx); err != nil {
		return err
	}
	el := time.Since(t0)
	objs, _ := logstore.ListObjects(ctx, cl, *bucketName)
	var total int64
	for _, s := range objs {
		total += s
	}
	sz, sh, _, _ := catalogInfo(ctx, cl, *bucketName)
	fmt.Printf("bulk: builds=%d rows=%d raw=%.2fGB stored=%.2fGB (%.1f B/row) objects=%d catalog=%dB shards=%d time=%s S3: %s\n",
		*builds, rowsDone.Load(), float64(rawBytes.Load())/1e9, float64(total)/1e9, float64(total)/float64(rowsDone.Load()), len(objs), sz, sh, el.Round(time.Second), cnt)
	return nil
}
