package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	lake "github.com/megakuul/lake"

	"bobspike/s10/logstore"
)

func init() { experiments["repro-lost"] = reproLost }

// reproLost: every catalog PUT of one writer gets 412 (as under heavy contention).
// Expected: Commit returns an error. lake HEAD returns nil and the rows are gone.
func reproLost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("repro-lost", flag.ExitOnError)
	ep := endpointFlag(fs)
	fs.Parse(args)
	admin := logstore.Client(endpoint(*ep), nil)
	const bucket = "s10-lost"
	if err := logstore.FreshBucket(ctx, admin, bucket); err != nil {
		return err
	}
	if _, err := lake.NewFromClient(ctx, admin, bucket); err != nil {
		return err
	}
	var rejected atomic.Int64
	cl := logstore.ClientWith(endpoint(*ep), nil, func(next http.RoundTripper) http.RoundTripper {
		return logstore.RTFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "lakedb.catalog") {
				rejected.Add(1)
				body := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>PreconditionFailed</Code><Message>At least one of the pre-conditions you specified did not hold</Message></Error>`
				return &http.Response{StatusCode: 412, Status: "412 Precondition Failed", Header: http.Header{"Content-Type": {"application/xml"}},
					Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
			}
			return next.RoundTrip(r)
		})
	})
	b, err := lake.NewFromClient(ctx, cl, bucket)
	if err != nil {
		return err
	}
	in := lake.NewIngestor[logstore.LogLine](b)
	in.Insert(ctx, logstore.FromPlain(logstore.Plain{BuildID: "B1", Package: "//a:b", Seq: 1, Line: "hello"}))
	cerr := in.Commit(ctx)
	fresh, _ := lake.NewFromClient(ctx, admin, bucket)
	rows, qerr := lake.Query[logstore.LogLine]().Scan(ctx, fresh)
	objs, _ := logstore.ListObjects(ctx, admin, bucket)
	fmt.Printf("catalog PUTs rejected=%d, Commit() err=%v, rows visible=%d (query err=%v), objects in bucket=%d\n", rejected.Load(), cerr, len(rows), qerr, len(objs))
	return nil
}
