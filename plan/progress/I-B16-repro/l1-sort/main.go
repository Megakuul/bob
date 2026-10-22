// l1-sort: a table with a `sort` tag (as in lake's test/integration), two ingestors commit
// shards with overlapping keys, then a filtered Scan (lake.Query().Where().Scan). S10 L1/L1b:
// at 045e301 the scan hung (100 % CPU) or returned wrong rows.
// Exit 0 = no failure (rows correct, no hang), 1 = reproduced.
package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/megakuul/lake"

	"ib16repro/internal/env"
)

type LogLine struct {
	lake.Table `name:"log_line" sort:"build_id:asc,package:asc,seq:asc"`
	BuildID    lake.String `parquet:"build_id"`
	Package    lake.String `parquet:"package"`
	Seq        lake.Int    `parquet:"seq"`
	Attempt    int64       `parquet:"attempt,zstd"`
	Line       string      `parquet:"line,zstd"`
}

func row(b, p string, s int64) LogLine {
	return LogLine{BuildID: lake.NewString(b), Package: lake.NewString(p), Seq: lake.NewInt(s), Attempt: 1,
		Line: fmt.Sprintf("%s %s %d", b, p, s)}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := env.Bucket(ctx, "l1")
	b, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	// Shards with interleaved (overlapping) key ranges, several commits each.
	in := lake.NewIngestor[LogLine](b)
	for c := range 6 {
		for _, p := range []string{"//a", "//b", "//c"} {
			for s := int64(c*3 + 1); s <= int64(c*3+3); s++ {
				if err := in.Insert(ctx, row("B1", p, s)); err != nil {
					env.Fatal("%v", err)
				}
			}
		}
		if err := in.Commit(ctx); err != nil {
			env.Fatal("%v", err)
		}
	}
	failed := false
	check := func(what string) {
		done := make(chan struct{})
		var rows []LogLine
		var qerr error
		go func() {
			defer close(done)
			rows, qerr = lake.Query[LogLine]().Where(LogLine{Package: lake.FilterString(lake.Eq("//b"))}).Scan(ctx, b)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			fmt.Printf("%s: filtered Scan HANGS (> 30 s)\n", what)
			failed = true
			return
		}
		if qerr != nil {
			fmt.Printf("%s: error %v\n", what, qerr)
			failed = true
			return
		}
		var seqs []int64
		for _, r := range rows {
			if r.Package.Data != "//b" {
				fmt.Printf("%s: WRONG ROW %s/%d\n", what, r.Package.Data, r.Seq.Data)
				failed = true
			}
			seqs = append(seqs, r.Seq.Data)
		}
		slices.Sort(seqs)
		if len(seqs) != 18 || seqs[0] != 1 || seqs[17] != 18 {
			fmt.Printf("%s: got seqs %v, want 1..18\n", what, seqs)
			failed = true
			return
		}
		fmt.Printf("%s: ok, %d rows of //b\n", what, len(rows))
	}
	check("6 sorted overlapping shards")
	// compaction of sorted shards, then the same query
	if err := lake.NewCompactor[LogLine](b).Compact(ctx); err != nil {
		env.Fatal("compact: %v", err)
	}
	check("after compaction")
	b2, err := lake.NewFromClient(ctx, env.Client(nil), name)
	if err != nil {
		env.Fatal("%v", err)
	}
	all, err := lake.Query[LogLine]().Scan(ctx, b2)
	if err != nil || len(all) != 54 {
		fmt.Printf("full scan: %d rows, %v (want 54)\n", len(all), err)
		failed = true
	}
	if failed {
		fmt.Println("REPRODUCED")
		os.Exit(1)
	}
	fmt.Println("NOT REPRODUCED")
}
