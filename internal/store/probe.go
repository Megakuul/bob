package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/pkg/bob/diag"
)

// ExitConfig is the exit status of a start-up refusal (EX_CONFIG, Gate H ruling G7).
const ExitConfig = 78

// Refusal is a start-up refusal of bob-server (R-STORE-102, R-SEC-102). bob-server prints
// "✗ <Msg>" and "hint: <Hint>" and exits ExitConfig.
type Refusal struct {
	Code diag.Code // 7007 unsupported backend, 7008 permissive policy
	Msg  string
	Hint string
}

func (r *Refusal) Error() string { return r.Msg }

// ExitCode is ExitConfig.
func (r *Refusal) ExitCode() int { return ExitConfig }

const backendHint = "bob needs an AWS-S3-compatible server with conditional writes, e.g. RustFS ≥ 1.0.3 or AWS S3"

func unsupported(endpoint, reason string) *Refusal {
	return &Refusal{
		Code: diag.Code(7007),
		Msg:  fmt.Sprintf("storage backend %s is not supported: %s", endpoint, reason),
		Hint: backendHint,
	}
}

// errProbeDenied: the credential may not write under _bob/probe/ in this bucket.
var errProbeDenied = errors.New("probe not permitted for this credential")

// staleETag is an ETag no object has.
const staleETag = `"00000000000000000000000000000000"`

// probe runs the seven checks of R-STORE-102 under _bob/probe/<random>/ and removes what it
// wrote. A failed check is a *Refusal; a 403 on the first write is errProbeDenied.
func probe(ctx context.Context, b *Bucket) (err error) {
	r := make([]byte, 8)
	if _, err := rand.Read(r); err != nil {
		return fmt.Errorf("store: probe: %w", err)
	}
	dir := "_bob/probe/" + hex.EncodeToString(r) + "/"
	x := dir + "x"
	no := func(reason string) error { return unsupported(b.endpoint, reason) }
	fail := func(step string, err error) error {
		return fmt.Errorf("store: conformance probe of bucket %s (%s): %w", b.name, step, failed(ctx, err))
	}
	defer func() {
		// Leave nothing behind, whatever happened (best effort; check 6 already deleted x).
		if err != nil {
			_, _ = b.s3.DeleteObject(context.WithoutCancel(ctx), &s3.DeleteObjectInput{Bucket: aws.String(b.name), Key: aws.String(x)})
		}
	}()

	// 1. PUT If-None-Match: * twice -> the second is 412.
	_, err = b.putOnce(ctx, x, []byte("1"), ifAbsent, "")
	if s3client.Status(err) == 403 {
		return errProbeDenied
	}
	if err != nil {
		return fail("conditional create", err)
	}
	switch _, err = b.putOnce(ctx, x, []byte("2"), ifAbsent, ""); {
	case err == nil:
		return no("conditional create (If-None-Match) is ignored")
	case !errors.Is(err, errPrecondition):
		return fail("second conditional create", err)
	}
	// 2. PUT If-Match: <stale> -> 412.
	switch _, err = b.putOnce(ctx, x, []byte("3"), ifMatch, staleETag); {
	case err == nil:
		return no("conditional overwrite (If-Match) is ignored")
	case !errors.Is(err, errPrecondition):
		return fail("stale conditional overwrite", err)
	}
	// 7. every GET returns an ETag; 3. GET If-None-Match: <current ETag> -> 304.
	o, _, err := b.getOnce(ctx, x, "")
	switch {
	case errors.Is(err, errNoETag):
		return no("GET returns no ETag header")
	case err != nil:
		return fail("read", err)
	case string(o.body) != "1":
		return no("a conditional write that failed changed the object")
	}
	switch _, nm, err := b.getOnce(ctx, x, o.etag); {
	case err != nil:
		return fail("conditional read", err)
	case !nm:
		return no("conditional GET (If-None-Match) is ignored")
	}
	// 4. GET of a missing key -> 404 NoSuchKey.
	switch _, _, err := b.getOnce(ctx, dir+"missing", ""); {
	case err == nil:
		return no("a missing key is readable")
	case !errors.Is(err, ErrNotFound):
		return no("a missing key does not return 404 NoSuchKey")
	}
	// 5. DELETE If-Match: <stale> -> 412.
	switch err = b.del(ctx, x, staleETag); {
	case err == nil:
		return no("conditional delete (If-Match) is ignored")
	case !errors.Is(err, errPrecondition):
		return fail("stale conditional delete", err)
	}
	// 6. GET of <dir>/ after deleting <dir>/x -> 404.
	if err = b.del(ctx, x, o.etag); err != nil {
		return fail("delete", err)
	}
	switch _, _, err := b.getOnce(ctx, dir, ""); {
	case err == nil:
		return no("an emptied directory reads as an object")
	case !errors.Is(err, ErrNotFound):
		return fail("read of the emptied directory", err)
	}
	return nil
}
