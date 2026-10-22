package cache

import (
	"context"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
)

// The artifacts bucket is reached only through internal/store (R-STORE-106); these are the
// cache package's names for store.Objects and its types and errors.
type (
	// Objects is the artifacts bucket (store.Objects).
	Objects = store.Objects
	// Object is an open download (store.Object).
	Object = store.Object
	// Info is the metadata of an object (store.Info).
	Info = store.Info
	// Cond is the precondition of a write (store.Cond).
	Cond = store.Cond
)

// Errors of the artifacts bucket. Callers act on them.
var (
	ErrNotFound     = store.ErrObjectNotFound
	ErrPrecondition = store.ErrPrecondition
	ErrRange        = store.ErrRange
	ErrTooLarge     = store.ErrTooLarge
)

// OpenObjects returns bucket behind the S3 endpoint of opts (store.OpenObjects).
func OpenObjects(ctx context.Context, opts s3client.Options, bucket string) (*Objects, error) {
	return store.OpenObjects(ctx, opts, bucket)
}
