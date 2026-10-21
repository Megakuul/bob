//go:build lakepatched

package main

import (
	"time"

	lake "github.com/megakuul/lake"
)

const lakeVariant = "PATCHED"

func readerOpts() []lake.BucketOption {
	return []lake.BucketOption{lake.WithCatalogCacheDuration(2 * time.Second)}
}

func compactorOpts[T any]() []lake.CompactorOption[T] {
	return []lake.CompactorOption[T]{lake.WithCompactionMaxInput[T](256_000_000)}
}
