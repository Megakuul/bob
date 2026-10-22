// l7-catalog-cache: a reader keeps one lake.Bucket (lake.NewFromClient once) and queries it;
// a writer (its own Bucket, as a worker process has) commits new rows; the reader queries again.
// lake caches the catalog for 60 s (bucket.go catalogCacheDuration, no option), so the
// long-lived reader does not see the commit. Exit 1 = reproduced.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/megakuul/lake"

	"ib16repro/internal/env"
)

type LogLine struct {
	lake.Table `name:"log_line"`
	BuildID    lake.String `parquet:"build_id"`
	Seq        lake.Int    `parquet:"seq"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := env.Bucket(ctx, "l7")
	writer, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	in := lake.NewIngestor[LogLine](writer)
	commit := func(s int64) {
		if err := in.Insert(ctx, LogLine{BuildID: lake.NewString("B1"), Seq: lake.NewInt(s)}); err != nil {
			env.Fatal("%v", err)
		}
		if err := in.Commit(ctx); err != nil {
			env.Fatal("%v", err)
		}
	}
	commit(1)
	reader, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	q := func() int {
		rows, err := lake.Query[LogLine]().Where(LogLine{BuildID: lake.FilterString(lake.Eq("B1"))}).Scan(ctx, reader)
		if err != nil {
			env.Fatal("%v", err)
		}
		return len(rows)
	}
	fmt.Println("reader sees", q(), "row(s)")
	commit(2)
	time.Sleep(2 * time.Second)
	n := q()
	fmt.Println("2 s after the second commit the long-lived reader sees", n, "row(s), want 2")
	if n != 2 {
		fmt.Println("REPRODUCED")
		os.Exit(1)
	}
	fmt.Println("NOT REPRODUCED")
}
