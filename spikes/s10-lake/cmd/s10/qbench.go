package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"time"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() { experiments["qbench"] = qbench }

func vmHWM() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "?"
}

func qbench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("qbench", flag.ExitOnError)
	ep := endpointFlag(fs)
	bucketName := fs.String("bucket", "s10-bulk", "bucket")
	builds := fs.Int("builds", 1000, "builds in the bucket (must match bulk)")
	npk := fs.Int("pkgs", 50, "packages per build")
	n := fs.Int("n", 15, "samples per query")
	only := fs.String("only", "", "comma list of query names")
	fs.Parse(args)
	cnt := logstore.NewCounter()
	cl := logstore.Client(endpoint(*ep), cnt)
	end := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	specs := buildSpecs(*builds, 90*24*time.Hour, end)
	pkgs := logstore.Pkgs(*npk)
	r := rand.New(rand.NewPCG(9, 9))
	b, err := lake.NewFromClient(ctx, cl, *bucketName, lake.WithMaxGroups(100000))
	if err != nil {
		return err
	}
	sz, sh, _, _ := catalogInfo(ctx, cl, *bucketName)
	fmt.Printf("== qbench lake=%s bucket=%s catalog=%dB shards=%d s3=%s\n", lakeVariant, *bucketName, sz, sh, *ep)

	run := func(name string, f func() (int, error)) {
		if *only != "" && !strings.Contains(","+*only+",", ","+name+",") {
			return
		}
		var ds []time.Duration
		rows := 0
		cnt.Reset()
		var ms0, ms1 runtime.MemStats
		runtime.ReadMemStats(&ms0)
		for range *n {
			t0 := time.Now()
			k, err := f()
			if err != nil {
				fmt.Printf("%-14s error: %v\n", name, err)
				return
			}
			ds = append(ds, time.Since(t0))
			rows += k
		}
		runtime.ReadMemStats(&ms1)
		s := cnt.Snapshot()
		fmt.Printf("%-14s n=%d p50=%-9s p90=%-9s max=%-9s rows/q=%-7d GET.data/q=%.1f down/q=%.1fMB alloc/q=%.0fMB\n", name, *n, ms(pct(ds, .5)), ms(pct(ds, .9)), ms(pct(ds, 1)),
			rows / *n, float64(s["GET.data"])/float64(*n), float64(cnt.BytesDn.Load())/1e6/float64(*n), float64(ms1.TotalAlloc-ms0.TotalAlloc)/1e6/float64(*n))
	}
	pick := func() (string, string) {
		return specs[r.IntN(len(specs))].id, pkgs[r.IntN(len(pkgs))]
	}
	run("catalog_load", func() (int, error) {
		_, err := lake.NewFromClient(ctx, cl, *bucketName)
		return 0, err
	})
	run("build_log", func() (int, error) {
		id, _ := pick()
		rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id))}).Scan(ctx, b)
		logstore.SortLines(rows)
		return len(rows), err
	})
	run("package_log", func() (int, error) {
		id, p := pick()
		rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id)), Package: lake.FilterString(lake.Eq(p))}).Scan(ctx, b)
		logstore.SortLines(rows)
		return len(rows), err
	})
	run("tail_seq", func() (int, error) {
		id, p := pick()
		rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id)), Package: lake.FilterString(lake.Eq(p)), Seq: lake.FilterInt(lake.Gt[int64](1900))}).Scan(ctx, b)
		return len(rows), err
	})
	run("search_build", func() (int, error) {
		id, _ := pick()
		rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id))}).Scan(ctx, b)
		hits := 0
		for _, row := range rows {
			if strings.Contains(row.Line, "warning: unused variable 'cache_7'") {
				hits++
			}
		}
		return hits, err
	})
	run("durations_30d", func() (int, error) {
		rows, err := lake.Query[logstore.PackageRun]().
			Where(logstore.PackageRun{Start: lake.FilterInt(lake.After(end.Add(-30 * 24 * time.Hour)))}).
			GroupBy(logstore.PackageRun{Package: lake.GroupString(lake.Exact()), Start: lake.GroupInt(lake.Date(lake.DateDay))}).
			Aggregate(logstore.PackageRun{DurationMs: lake.AggrInt(lake.Avg[int64]), Lines: lake.AggrInt(lake.Count[int64])}).
			Scan(ctx, b)
		return len(rows), err
	})
	run("durations_all", func() (int, error) {
		rows, err := lake.Query[logstore.PackageRun]().
			GroupBy(logstore.PackageRun{Package: lake.GroupString(lake.Exact()), Start: lake.GroupInt(lake.Date(lake.DateDay))}).
			Aggregate(logstore.PackageRun{DurationMs: lake.AggrInt(lake.Avg[int64]), Lines: lake.AggrInt(lake.Count[int64])}).
			Scan(ctx, b)
		return len(rows), err
	})
	run("failures_7d", func() (int, error) {
		rows, err := lake.Query[logstore.PackageRun]().
			Where(logstore.PackageRun{Status: lake.FilterString(lake.Eq("failed")), Start: lake.FilterInt(lake.After(end.Add(-7 * 24 * time.Hour)))}).
			Scan(ctx, b)
		return len(rows), err
	})
	fmt.Println("peak RSS:", vmHWM())
	return nil
}
