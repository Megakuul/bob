// l4-nosuchkey: a query whose catalog was loaded before a compaction (by another process,
// with its own Bucket) committed. Compact deletes its input shards at once (compactor.go), so
// the query GETs a deleted object and fails with NoSuchKey.
//
// Part A: reader Bucket opened, compaction, query on that Bucket (catalog cached 60 s).
// Part B: plain usage with a fresh Bucket per query (lake.NewFromClient then Scan) in a loop
// while another goroutine compacts repeatedly: the window between catalog GET and shard GET.
// Exit 1 = reproduced.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/megakuul/lake"

	"ib16repro/internal/env"
)

type LogLine struct {
	lake.Table `name:"log_line"`
	BuildID    lake.String `parquet:"build_id"`
	Seq        lake.Int    `parquet:"seq"`
	Line       string      `parquet:"line,zstd"`
}

func fill(ctx context.Context, b *lake.Bucket, from, commits, per int) {
	in := lake.NewIngestor[LogLine](b)
	for c := range commits {
		for s := range per {
			seq := int64(from + c*per + s)
			if err := in.Insert(ctx, LogLine{BuildID: lake.NewString("B1"), Seq: lake.NewInt(seq), Line: fmt.Sprint("line ", seq)}); err != nil {
				env.Fatal("%v", err)
			}
		}
		if err := in.Commit(ctx); err != nil {
			env.Fatal("%v", err)
		}
	}
}

func query(ctx context.Context, b *lake.Bucket) (int, error) {
	rows, err := lake.Query[LogLine]().Where(LogLine{BuildID: lake.FilterString(lake.Eq("B1"))}).Scan(ctx, b)
	return len(rows), err
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	reproduced := false

	// Part A
	name := env.Bucket(ctx, "l4a")
	w, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	fill(ctx, w, 1, 10, 100)
	reader, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	if n, err := query(ctx, reader); err != nil || n != 1000 {
		env.Fatal("first query %d %v", n, err)
	}
	comp, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	if err := lake.NewCompactor[LogLine](comp).Compact(ctx); err != nil {
		env.Fatal("compact: %v", err)
	}
	n, err := query(ctx, reader)
	_, nsk := errors.AsType[*types.NoSuchKey](err)
	fmt.Printf("A: query on a Bucket opened before the compaction: %d rows, err=%v (NoSuchKey=%v)\n", n, err, nsk)
	if err != nil {
		reproduced = true
	}

	// Part B
	name = env.Bucket(ctx, "l4b")
	w, err = lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	var queries, failures atomic.Int64
	next := 1
	fill(ctx, w, next, 20, 50)
	next += 1000
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				b, err := lake.NewFromClient(ctx, env.Client(nil), name)
				if err != nil {
					env.Fatal("%v", err)
				}
				_, err = query(ctx, b)
				queries.Add(1)
				if err != nil {
					failures.Add(1)
					if failures.Load() == 1 {
						fmt.Println("B: first failure:", err)
					}
				}
			}
		})
	}
	for range 15 {
		fill(ctx, w, next, 20, 50) // 20 new small shards
		next += 1000
		c, err := lake.NewFromClient(ctx, env.Client(nil), name)
		if err != nil {
			env.Fatal("%v", err)
		}
		if err := lake.NewCompactor[LogLine](c).Compact(ctx); err != nil {
			fmt.Println("B: compact:", err)
		}
	}
	stop.Store(true)
	wg.Wait()
	fmt.Printf("B: fresh Bucket per query during 15 compactions: %d queries, %d failed\n", queries.Load(), failures.Load())
	if failures.Load() > 0 {
		reproduced = true
	}
	if reproduced {
		fmt.Println("REPRODUCED")
		os.Exit(1)
	}
	fmt.Println("NOT REPRODUCED")
}
