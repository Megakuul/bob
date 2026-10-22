package store

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/dynamitedb"
)

type R struct {
	ID dynamitedb.KeyField       `pk:"r"`
	N  dynamitedb.DataField[int] `cbor:"n,omitempty"`
}

var _ s3.Client

func ok(ctx context.Context, b *dynamitedb.Bucket) {
	_ = dynamitedb.Update(ctx, b, &R{N: dynamitedb.Increment(1)})
	_, _ = dynamitedb.Get(ctx, b, &R{N: dynamitedb.Eq(1)})
	_, _ = dynamitedb.Query(ctx, b, &R{}) // want `dynamitedb.Query outside`
}
