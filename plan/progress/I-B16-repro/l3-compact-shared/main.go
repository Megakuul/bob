// l3-compact-shared: lake.Compactor commits its merged shards with commitCatalog, which does not
// check that the input shards are still in the catalog. If the compactor's Bucket reloads its
// catalog while the compaction runs (any other use of the same Bucket: an Ingestor commit, a
// query after the 60 s cache), a compaction that another Bucket committed in between is not
// noticed and the rows are stored twice.
//
// Part A (shared Bucket): compactor X pauses after loading its catalog (its HTTP client holds
// the PUT of its merged shard); compactor Y (own Bucket) compacts; an Ingestor on X's Bucket commits one
// row (lake reloads X's catalog); X continues. Part B (plain: a fresh Bucket per Compact, used
// by nothing else): two compactors at once. Exit 1 = reproduced (duplicates).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/megakuul/lake"

	"ib16repro/internal/env"
)

type LogLine struct {
	lake.Table `name:"log_line"`
	BuildID    lake.String `parquet:"build_id"`
	Seq        lake.Int    `parquet:"seq"`
}

// gate holds the first PUT of a .parquet object (the merged shard) until released.
type gate struct {
	first   atomic.Bool
	reached chan struct{}
	release chan struct{}
}

func (g *gate) Do(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".parquet") && r.Header.Get("If-None-Match") == "*" {
		if g.first.CompareAndSwap(false, true) {
			close(g.reached)
			<-g.release
		}
	}
	return http.DefaultClient.Do(r)
}

func fill(ctx context.Context, name string, commits int) {
	b, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	in := lake.NewIngestor[LogLine](b)
	for c := range commits {
		for s := range 10 {
			_ = in.Insert(ctx, LogLine{BuildID: lake.NewString("B1"), Seq: lake.NewInt(int64(c*10 + s + 1))})
		}
		if err := in.Commit(ctx); err != nil {
			env.Fatal("%v", err)
		}
	}
}

func count(ctx context.Context, name string) int {
	b, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	rows, err := lake.Query[LogLine]().Scan(ctx, b)
	if err != nil {
		env.Fatal("%v", err)
	}
	return len(rows)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reproduced := false

	// Part A
	name := env.Bucket(ctx, "l3a")
	fill(ctx, name, 5) // 50 rows
	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	x, err := lake.NewFromClient(ctx, env.Client(g), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	xerr := make(chan error, 1)
	go func() { xerr <- lake.NewCompactor[LogLine](x).Compact(ctx) }()
	<-g.reached
	y, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	if err := lake.NewCompactor[LogLine](y).Compact(ctx); err != nil {
		env.Fatal("Y compact: %v", err)
	}
	in := lake.NewIngestor[LogLine](x) // same Bucket as compactor X
	_ = in.Insert(ctx, LogLine{BuildID: lake.NewString("B1"), Seq: lake.NewInt(51)})
	if err := in.Commit(ctx); err != nil {
		env.Fatal("ingest on X: %v", err)
	}
	close(g.release)
	err = <-xerr
	n := count(ctx, name)
	fmt.Printf("A: shared Bucket: X compact err=%v; table holds %d rows, want 51\n", err, n)
	if n != 51 {
		reproduced = true
	}

	// Part B
	name = env.Bucket(ctx, "l3b")
	fill(ctx, name, 20) // 200 rows
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			b, err := lake.NewFromClient(ctx, env.Client(nil), name)
			if err != nil {
				env.Fatal("%v", err)
			}
			errs[i] = lake.NewCompactor[LogLine](b).Compact(ctx)
		})
	}
	wg.Wait()
	n = count(ctx, name)
	fmt.Printf("B: own Bucket per Compact, 2 at once: errs=%v; table holds %d rows, want 200\n", errs, n)
	if n != 200 {
		reproduced = true
	}
	if reproduced {
		fmt.Println("REPRODUCED")
		os.Exit(1)
	}
	fmt.Println("NOT REPRODUCED")
}
