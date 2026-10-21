//go:build !lakepatched

package main

import lake "github.com/megakuul/lake"

const lakeVariant = "HEAD"

func readerOpts() []lake.BucketOption { return nil }

func compactorOpts[T any]() []lake.CompactorOption[T] { return nil }
