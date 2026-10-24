package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() {
	experiments["compact-race"] = compactRace
}

// seed writes n small shards of m lines each (one build, 5 packages) and returns the line count.
func seed(ctx context.Context, b *lake.Bucket, n, m int, build string) (int, error) {
	r := rand.New(rand.NewPCG(3, 3))
	seq := map[string]int64{}
	total := 0
	for range n {
		in := lake.NewIngestor[logstore.LogLine](b)
		rows := []logstore.LogLine{}
		for i := range m {
			p := fmt.Sprintf("//p:%d", i%5)
			seq[p]++
			rows = append(rows, logstore.FromPlain(logstore.Plain{BuildID: build, Package: p, Seq: seq[p], Attempt: 1, Stream: 1, TS: time.Now().UnixNano(), Line: logstore.Line(r, p, seq[p])}))
		}
		if err := in.Insert(ctx, rows...); err != nil {
			return 0, err
		}
		if err := in.Commit(ctx); err != nil {
			return 0, err
		}
		total += m
	}
	return total, nil
}

func countRows(ctx context.Context, cl func() *lake.Bucket) (rows, dups int, err error) {
	got, err := lake.Query[logstore.LogLine]().Scan(ctx, cl())
	if err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	for _, g := range got {
		k := fmt.Sprintf("%s|%s|%d", g.BuildID.Data, g.Package.Data, g.Seq.Data)
		if seen[k] {
			dups++
		}
		seen[k] = true
	}
	return len(got), dups, nil
}

func compactRace(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("compact-race", flag.ExitOnError)
	ep := endpointFlag(fs)
	rounds := fs.Int("rounds", 10, "rounds of case A")
	fs.Parse(args)
	admin := logstore.Client(endpoint(*ep), nil)
	const bucket = "s10-race"
	fresh := func() *lake.Bucket {
		b, err := lake.NewFromClient(ctx, admin, bucket)
		if err != nil {
			panic(err)
		}
		return b
	}

	// Case A: two orchestrators (separate lake.Bucket) compact the same shards at once.
	outcomes := map[string]int{}
	var dupTotal, missTotal int
	for range *rounds {
		if err := logstore.FreshBucket(ctx, admin, bucket); err != nil {
			return err
		}
		want, err := seed(ctx, fresh(), 40, 500, "B1")
		if err != nil {
			return err
		}
		ba, bb := fresh(), fresh()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, b := range []*lake.Bucket{ba, bb} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = lake.NewCompactor[logstore.LogLine](b, lake.WithCompactionSize[logstore.LogLine](32_000_000)).Compact(ctx)
			}()
		}
		wg.Wait()
		key := ""
		for _, e := range errs {
			switch {
			case e == nil:
				key += "ok,"
			case errors.Is(e, lake.ErrOptimisticLock):
				key += "lock,"
			case strings.Contains(e.Error(), "NoSuchKey"):
				key += "nosuchkey,"
			default:
				key += "err(" + e.Error() + "),"
			}
		}
		outcomes[key]++
		n, d, err := countRows(ctx, fresh)
		if err != nil {
			return err
		}
		dupTotal += d
		if n-d < want {
			missTotal += want - (n - d)
		}
	}
	fmt.Println("case A (separate buckets, concurrent Compact):")
	for k, v := range outcomes {
		fmt.Printf("  outcomes %-30s x%d\n", k, v)
	}
	fmt.Printf("  duplicates=%d missing=%d over %d rounds\n", dupTotal, missTotal, *rounds)

	// Case B: orchestrator B shares its lake.Bucket between the compactor and something
	// that reloads the catalog (an ingest commit that hits 412, or a query after the 1 min
	// cache). Orchestrator A compacts in between.
	if err := logstore.FreshBucket(ctx, admin, bucket); err != nil {
		return err
	}
	want, err := seed(ctx, fresh(), 20, 500, "B1")
	if err != nil {
		return err
	}
	hit := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	clB := logstore.ClientWith(endpoint(*ep), nil, func(next http.RoundTripper) http.RoundTripper {
		return logstore.RTFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/log_line/") {
				first := false
				once.Do(func() { first = true })
				if first { // pause B's compactor right before it uploads its merged shard
					close(hit)
					<-release
				}
			}
			return next.RoundTrip(r)
		})
	})
	bB, err := lake.NewFromClient(ctx, clB, bucket)
	if err != nil {
		return err
	}
	errB := make(chan error, 1)
	go func() {
		errB <- lake.NewCompactor[logstore.LogLine](bB).Compact(ctx)
	}()
	<-hit
	errA := lake.NewCompactor[logstore.LogLine](fresh()).Compact(ctx)
	// an unrelated ingest on B's bucket: its first catalog PUT gets 412, lake reloads the catalog.
	inB := lake.NewIngestor[logstore.LogLine](bB)
	inB.Insert(ctx, logstore.FromPlain(logstore.Plain{BuildID: "B2", Package: "//x:y", Seq: 1, Line: "other build"}))
	errIn := inB.Commit(ctx)
	close(release)
	eB := <-errB
	n, d, err := countRows(ctx, fresh)
	if err != nil {
		return err
	}
	fmt.Println("case B (shared bucket reloaded during compaction):")
	fmt.Printf("  compactor A err=%v, ingest on B err=%v, compactor B err=%v\n", errA, errIn, eB)
	fmt.Printf("  rows=%d expected=%d duplicates=%d\n", n, want+1, d)

	// Case C: a reader with a cached catalog queries after a compaction deleted the shards.
	if err := logstore.FreshBucket(ctx, admin, bucket); err != nil {
		return err
	}
	if _, err := seed(ctx, fresh(), 10, 200, "B1"); err != nil {
		return err
	}
	reader := fresh()
	q := func() error {
		_, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq("B1"))}).Scan(ctx, reader)
		return err
	}
	e1 := q()
	ec := lake.NewCompactor[logstore.LogLine](fresh()).Compact(ctx)
	e2 := q()
	fmt.Println("case C (reader with cached catalog after compaction):")
	fmt.Printf("  before=%v compaction=%v after=%v\n", e1, ec, e2)
	t0 := time.Now()
	for q() != nil && time.Since(t0) < 2*time.Minute {
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("  reader recovered after %s (catalog cache expiry)\n", time.Since(t0).Round(time.Second))
	return nil
}
