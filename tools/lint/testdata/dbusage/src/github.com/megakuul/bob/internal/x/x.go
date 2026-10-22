package x

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3" // want `import of github.com/aws/aws-sdk-go-v2/service/s3 is not allowed`
	"github.com/megakuul/dynamitedb"          // want `import of github.com/megakuul/dynamitedb is not allowed`
	"github.com/megakuul/lake"                // want `import of github.com/megakuul/lake is not allowed`
)

type R struct {
	ID dynamitedb.KeyField          `pk:"r"`
	N  dynamitedb.DataField[int]    `cbor:"n,omitempty"`
	S  dynamitedb.DataField[string] `cbor:"s,omitempty"`
}

var _ s3.Client
var _ = lake.NewString

func bad(ctx context.Context, b *dynamitedb.Bucket) {
	_ = dynamitedb.Update(ctx, b, &R{ID: dynamitedb.Key("a"), S: dynamitedb.Eq("x")})      // want `filter operator dynamitedb.Eq in dynamitedb.Update`
	_ = dynamitedb.Put(ctx, b, &R{N: dynamitedb.GreaterThan(1)})                           // want `filter operator dynamitedb.GreaterThan in dynamitedb.Put`
	_ = dynamitedb.Create(ctx, b, &R{S: dynamitedb.In("a", "b")})                          // want `filter operator dynamitedb.In in dynamitedb.Create`
	_ = dynamitedb.Update[R](ctx, b, &R{S: dynamitedb.Eq("x")})                            // want `filter operator dynamitedb.Eq in dynamitedb.Update`
	_, _ = dynamitedb.Get(ctx, b, &R{ID: dynamitedb.Key("a"), N: dynamitedb.Increment(1)}) // want `update operator dynamitedb.Increment in dynamitedb.Get`
	_ = dynamitedb.Delete(ctx, b, &R{S: dynamitedb.Set("z")})                              // want `update operator dynamitedb.Set in dynamitedb.Delete`
	_, _ = dynamitedb.Query(ctx, b, &R{S: dynamitedb.Set("z")})                            // want `dynamitedb.Query outside` `update operator dynamitedb.Set in dynamitedb.Query`
	cur, _ := dynamitedb.Get(ctx, b, &R{ID: dynamitedb.Key("a")})
	_ = dynamitedb.Update(ctx, b, cur) // want `takes a fresh composite literal`
}

func good(ctx context.Context, b *dynamitedb.Bucket, build func(string) *R) {
	_ = dynamitedb.Update(ctx, b, &R{ID: dynamitedb.Key("a"), N: dynamitedb.Increment(1), S: dynamitedb.Set("x")})
	_ = dynamitedb.Create(ctx, b, &R{ID: dynamitedb.Key("a")})
	_ = dynamitedb.Put(ctx, b, build("a"))
	_, _ = dynamitedb.Get(ctx, b, &R{ID: dynamitedb.Key("a"), S: dynamitedb.Eq("x")})
	_ = dynamitedb.Delete(ctx, b, &R{N: dynamitedb.GreaterThan(2)})
}
