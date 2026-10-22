package sweep

import (
	"context"

	"github.com/megakuul/dynamitedb"
)

type R struct {
	ID dynamitedb.KeyField `pk:"r"`
}

func list(ctx context.Context, b *dynamitedb.Bucket) {
	_, _ = dynamitedb.Query(ctx, b, &R{})
}
