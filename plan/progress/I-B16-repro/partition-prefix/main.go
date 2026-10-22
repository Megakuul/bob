// partition-prefix: bob needs one lake catalog per partition (`log-<day>-<k>`, `run-<month>`,
// `job-<month>`, SPEC §05.10.1) inside the one analytics bucket, so that retention can drop a
// partition (lake has no delete API) and each partition's catalog has its own commit budget.
// lake addresses only `<bucket>/lakedb.catalog` and `<bucket>/<table>/<uuid>.parquet`
// (bucket.go, no key prefix option). This checks whether the plain call
// lake.NewFromClient(ctx, client, "<analytics>/<partition>") gives such a partition.
// Exit 1 = reproduced (the plain call cannot address a partition).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/lake"

	"ib16repro/internal/env"
)

type LogLine struct {
	lake.Table `name:"log_line"`
	BuildID    lake.String `parquet:"build_id"`
	Seq        lake.Int    `parquet:"seq"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	analytics := env.Bucket(ctx, "pp")
	part := analytics + "/log-20261022-0"
	b, err := lake.NewFromClient(ctx, env.Client(nil), part)
	if err == nil {
		in := lake.NewIngestor[LogLine](b)
		_ = in.Insert(ctx, LogLine{BuildID: lake.NewString("B1"), Seq: lake.NewInt(1)})
		err = in.Commit(ctx)
	}
	if err != nil {
		fmt.Printf("lake.NewFromClient/Commit with bucket %q: %v\n", part, err)
		fmt.Println("REPRODUCED")
		os.Exit(1)
	}
	key := "log-20261022-0/lakedb.catalog"
	if _, err := env.Client(nil).HeadObject(ctx, &s3.HeadObjectInput{Bucket: &analytics, Key: &key}); err != nil {
		fmt.Printf("commit succeeded but %s/%s does not exist: %v\nREPRODUCED\n", analytics, key, err)
		os.Exit(1)
	}
	fmt.Println("NOT REPRODUCED: the plain call stores the partition under the analytics bucket")
}
