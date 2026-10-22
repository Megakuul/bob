package legacy

import (
	"context"

	"github.com/megakuul/dynamitedb"
)

type R struct {
	N dynamitedb.DataField[int] `cbor:"n,omitempty"`
}

func f(ctx context.Context, b *dynamitedb.Bucket) {
	_ = dynamitedb.Update(ctx, b, &R{N: dynamitedb.Eq(1)})
}
