package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

// Unsorted is LogLine without a sort tag (workaround candidate for the merge hang).
type Unsorted struct {
	lake.Table `name:"log_line_unsorted"`
	BuildID    lake.String `parquet:"build_id"`
	Package    lake.String `parquet:"package"`
	Seq        lake.Int    `parquet:"seq"`
	Line       string      `parquet:"line,zstd"`
}

// Sorted is a log table with a sort tag (the layout lake's compactor is designed for).
type Sorted struct {
	lake.Table `name:"log_line_sorted" sort:"build_id:asc,package:asc,seq:asc"`
	BuildID    lake.String `parquet:"build_id"`
	Package    lake.String `parquet:"package"`
	Seq        lake.Int    `parquet:"seq"`
	Line       string      `parquet:"line,zstd"`
}

func init() { experiments["repro-hang"] = reproHang }

// reproHang: two shards of a table with a sort tag, then a Where that skips >= 2 rows.
func reproHang(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("repro-hang", flag.ExitOnError)
	ep := endpointFlag(fs)
	unsorted := fs.Bool("unsorted", false, "use the table without sort tag")
	shards := fs.Int("shards", 2, "shards")
	gap1 := fs.Bool("gap1", false, "single package, filter every second seq (gap of 1 row between matches)")
	fs.Parse(args)
	cl := logstore.Client(endpoint(*ep), nil)
	if err := logstore.FreshBucket(ctx, cl, "s10-repro"); err != nil {
		return err
	}
	b, err := lake.NewFromClient(ctx, cl, "s10-repro")
	if err != nil {
		return err
	}
	for s := range *shards {
		if *unsorted {
			in := lake.NewIngestor[Unsorted](b)
			for i := range 10 {
				in.Insert(ctx, Unsorted{BuildID: lake.NewString("b1"), Package: lake.NewString(fmt.Sprintf("p%d", i%3)), Seq: lake.NewInt(s*10 + i), Line: "x"})
			}
			if err := in.Commit(ctx); err != nil {
				return err
			}
		} else {
			in := lake.NewIngestor[Sorted](b)
			for i := range 10 {
				pk := fmt.Sprintf("p%d", i%3)
				if *gap1 {
					pk = "p2"
				}
				in.Insert(ctx, Sorted{BuildID: lake.NewString("b1"), Package: lake.NewString(pk), Seq: lake.NewInt(s*10 + i), Line: "x"})
			}
			if err := in.Commit(ctx); err != nil {
				return err
			}
		}
	}
	done := make(chan string, 1)
	go func() {
		t0 := time.Now()
		var n int
		var err error
		if *unsorted {
			var rows []Unsorted
			rows, err = lake.Query[Unsorted]().Where(Unsorted{Package: lake.FilterString(lake.Eq("p2"))}).Scan(ctx, b)
			n = len(rows)
		} else {
			var rows []Sorted
			q := lake.Query[Sorted]().Where(Sorted{Package: lake.FilterString(lake.Eq("p2"))})
			if *gap1 {
				q = lake.Query[Sorted]().Where(Sorted{Seq: lake.FilterInt(lake.In[int64](0, 3, 6, 9, 12, 15, 18))})
			}
			rows, err = q.Scan(ctx, b)
			for _, r := range rows {
				fmt.Print(r.Seq.Data, " ")
			}
			n = len(rows)
		}
		done <- fmt.Sprintf("rows=%d err=%v in %s", n, err, time.Since(t0))
	}()
	select {
	case s := <-done:
		fmt.Println("query returned:", s)
	case <-time.After(10 * time.Second):
		fmt.Println("query HANGS (no result after 10s)")
	}
	return nil
}
