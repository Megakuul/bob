package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	lake "github.com/megakuul/lake"
	"github.com/parquet-go/parquet-go"

	"bobspike/s10/logstore"
)

// Note: tags like `parquet:"line,zstd"` on lake.String fields panic in parquet-go
// ("cannot add compression codec to a non-leaf node"), because lake wraps every column
// in a group {data}. Verified in the first probe run; see the write-up.

func init() {
	experiments["probe"] = probe
}

func describe(ctx context.Context, cl *s3.Client, bucket, prefix string) error {
	objs, err := logstore.ListObjects(ctx, cl, bucket)
	if err != nil {
		return err
	}
	for k, sz := range objs {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		o, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(o.Body)
		o.Body.Close()
		f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		fmt.Printf("  %s size=%d rows=%d rowgroups=%d\n", k, sz, f.NumRows(), len(f.RowGroups()))
		md := f.Metadata()
		for _, c := range md.RowGroups[0].Columns {
			fmt.Printf("    col %-14v codec=%-12v enc=%v comp=%d uncomp=%d\n", c.MetaData.PathInSchema, c.MetaData.Codec, c.MetaData.Encoding, c.MetaData.TotalCompressedSize, c.MetaData.TotalUncompressedSize)
		}
	}
	return nil
}

func probe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	ep := endpointFlag(fs)
	n := fs.Int("n", 2000, "lines")
	fs.Parse(args)
	cnt := logstore.NewCounter()
	cl := logstore.Client(endpoint(*ep), cnt)
	if err := logstore.FreshBucket(ctx, cl, "s10-probe"); err != nil {
		return err
	}
	b, err := lake.NewFromClient(ctx, cl, "s10-probe")
	if err != nil {
		return err
	}
	r := rand.New(rand.NewPCG(1, 2))
	pk := logstore.Pkgs(50)
	id := logstore.BuildID(time.Now(), r)
	raw := 0
	in := lake.NewIngestor[logstore.LogLine](b)
	zin := lake.NewIngestor[logstore.LogLineNaive](b)
	now := time.Now().UnixNano()
	drvs := []string{logstore.Drv(r, pk[0]), logstore.Drv(r, pk[1]), logstore.Drv(r, pk[2])}
	for i := range *n {
		p := pk[i%3]
		line := logstore.Line(r, p, int64(i))
		raw += len(line)
		ll := logstore.FromPlain(logstore.Plain{BuildID: id, Package: p, Drv: drvs[i%3], Attempt: 1, Stream: 1 + int64(r.IntN(10)/9), Seq: int64(i/3 + 1), TS: now + int64(i)*1000, Line: line})
		if err := in.Insert(ctx, ll); err != nil {
			return err
		}
		if err := zin.Insert(ctx, logstore.NaiveFromPlain(logstore.ToPlain(ll))); err != nil {
			return err
		}
	}
	cnt.Reset()
	if err := in.Commit(ctx); err != nil {
		return err
	}
	fmt.Println("one commit S3 requests:", cnt.String())
	if err := zin.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("raw line bytes=%d (%.1f B/line)\n", raw, float64(raw)/float64(*n))
	if err := describe(ctx, cl, "s10-probe", "log_line/"); err != nil {
		return err
	}
	if err := describe(ctx, cl, "s10-probe", "log_line_naive/"); err != nil {
		return err
	}
	zrows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{Seq: lake.FilterInt(lake.Eq[int64](5))}).Scan(ctx, b)
	if err != nil {
		return err
	}
	fmt.Printf("rows seq=5: %d first=%+v\n", len(zrows), logstore.ToPlain(zrows[0]))
	objs, _ := logstore.ListObjects(ctx, cl, "s10-probe")
	fmt.Println("catalog size:", objs["lakedb.catalog"])
	o, _ := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("s10-probe"), Key: aws.String("lakedb.catalog")})
	data, _ := io.ReadAll(o.Body)
	fmt.Println(string(data[:min(len(data), 1500)]))

	// Order of Scan results and empty commit behaviour.
	cnt.Reset()
	if err := in.Commit(ctx); err != nil {
		return fmt.Errorf("empty commit: %w", err)
	}
	fmt.Println("empty commit S3 requests:", cnt.String())
	rows, err := lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id)), Package: lake.FilterString(lake.Eq(pk[1]))}).Scan(ctx, b)
	if err != nil {
		return err
	}
	fmt.Println("package rows", len(rows), "first seq", rows[0].Seq.Data, "last seq", rows[len(rows)-1].Seq.Data)
	rows, err = lake.Query[logstore.LogLine]().Where(logstore.LogLine{BuildID: lake.FilterString(lake.Eq(id)), Package: lake.FilterString(lake.Eq(pk[1])), Seq: lake.FilterInt(lake.Gt[int64](600))}).Limit(5).Scan(ctx, b)
	if err != nil {
		return err
	}
	for _, rw := range rows {
		fmt.Println("  tail", rw.Seq.Data, rw.Line)
	}
	return nil
}
