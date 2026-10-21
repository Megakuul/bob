package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"time"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() { experiments["retention"] = retention }

// retention drops log shards whose newest build is older than -keep, and package_run
// shards whose newest start is older than -keepruns, then checks queries.
func retention(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("retention", flag.ExitOnError)
	ep := endpointFlag(fs)
	bucketName := fs.String("bucket", "s10-bulk", "bucket")
	builds := fs.Int("builds", 1000, "builds (must match bulk)")
	keep := fs.Duration("keep", 60*24*time.Hour, "log retention")
	fs.Parse(args)
	cnt := logstore.NewCounter()
	cl := logstore.Client(endpoint(*ep), cnt)
	end := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	specs := buildSpecs(*builds, 90*24*time.Hour, end)
	cutoff := logstore.BuildID(end.Add(-*keep), rand.New(rand.NewPCG(0, 0)))
	cutoff = cutoff[:10] // time prefix only: ids sort by time first
	before, _ := logstore.ListObjects(ctx, cl, *bucketName)
	t0 := time.Now()
	res, err := logstore.DropShards(ctx, cl, *bucketName, "log_line", func(_ string, _ map[string]int64, maxStr map[string]string) bool {
		return maxStr["build_id"] < cutoff
	})
	el := time.Since(t0)
	if err != nil {
		return err
	}
	after, _ := logstore.ListObjects(ctx, cl, *bucketName)
	fmt.Printf("retention keep=%s: dropped=%d shards (%.2f GB) kept=%d in %s; objects %d -> %d; S3: %s\n",
		*keep, res.Dropped, float64(res.Bytes)/1e9, res.Kept, el.Round(time.Millisecond), len(before), len(after), cnt)
	b, err := lake.NewFromClient(ctx, cl, *bucketName)
	if err != nil {
		return err
	}
	for _, i := range []int{0, *builds / 3, *builds - 1} {
		rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(specs[i].id))}).Scan(ctx, b)
		fmt.Printf("  build %d (%s, age %s): rows=%d err=%v\n", i, specs[i].id, end.Sub(specs[i].start).Round(time.Hour), len(rows), err)
	}
	return nil
}
