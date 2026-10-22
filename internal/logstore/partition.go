package logstore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/megakuul/lake"
)

// crockford is the lower-case Crockford base32 alphabet of build ids (SPEC §6.1).
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// ErrBadID is returned for a build id that is not a 26-char lower-case Crockford id.
var ErrBadID = errors.New("invalid build id")

// BuildTime is the creation time encoded in a build id (48-bit ms in the first 10 chars).
func BuildTime(buildID string) (time.Time, error) {
	if len(buildID) != 26 {
		return time.Time{}, fmt.Errorf("%w %q", ErrBadID, buildID)
	}
	var ms uint64
	for i := range len(buildID) {
		v := strings.IndexByte(crockford, buildID[i])
		if v < 0 {
			return time.Time{}, fmt.Errorf("%w %q", ErrBadID, buildID)
		}
		if i < 10 {
			ms = ms<<5 | uint64(v)
		}
	}
	if ms >= 1<<48 {
		return time.Time{}, fmt.Errorf("%w %q", ErrBadID, buildID)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// LogPartition is the partition of a build's lines: `log-<yyyymmdd>-<k>`, the UTC day of the
// build id's time and k = fnv32a(buildID) mod shards.
func LogPartition(buildID string, shards int) (string, error) {
	t, err := BuildTime(buildID)
	if err != nil {
		return "", err
	}
	if shards < 1 {
		shards = DefaultLogShards
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(buildID))
	return fmt.Sprintf("log-%s-%d", t.Format("20060102"), h.Sum32()%uint32(shards)), nil
}

// RunPartition is the partition of a build's package runs: `run-<yyyymm>` of the build time.
func RunPartition(buildID string) (string, error) {
	t, err := BuildTime(buildID)
	if err != nil {
		return "", err
	}
	return "run-" + t.Format("200601"), nil
}

// JobPartition is the partition of job runs finalised at t: `job-<yyyymm>`.
func JobPartition(t time.Time) string { return "job-" + t.UTC().Format("200601") }

// PartitionEnd is the end of a partition's time range: the next UTC day of `log-<yyyymmdd>-<k>`,
// the next month of `run-<yyyymm>` and `job-<yyyymm>`. ok is false for any other name.
func PartitionEnd(name string) (end time.Time, ok bool) {
	class, rest, found := strings.Cut(name, "-")
	if !found {
		return time.Time{}, false
	}
	switch class {
	case "log":
		day, k, found := strings.Cut(rest, "-")
		if !found || k == "" || strings.Trim(k, "0123456789") != "" {
			return time.Time{}, false
		}
		t, err := time.Parse("20060102", day)
		if err != nil {
			return time.Time{}, false
		}
		return t.AddDate(0, 0, 1), true
	case "run", "job":
		t, err := time.Parse("200601", rest)
		if err != nil {
			return time.Time{}, false
		}
		return t.AddDate(0, 1, 0), true
	}
	return time.Time{}, false
}

// PrefixClient returns a client whose base endpoint is <endpoint>/<bucket>, so that lake's
// "bucket" argument becomes a key prefix inside the analytics bucket (path-style addressing,
// SPEC §05.10.1, verified V3/V4). The client keeps base's credentials, region and HTTP client;
// wrap, when not nil, replaces its HTTP client (the shard cache).
func PrefixClient(base *s3.Client, bucket string, wrap func(s3.HTTPClient) s3.HTTPClient) (*s3.Client, error) {
	o := base.Options()
	if o.BaseEndpoint == nil || *o.BaseEndpoint == "" {
		return nil, errors.New("logstore: the S3 client has no base endpoint")
	}
	if strings.ContainsAny(bucket, "/?#") || bucket == "" {
		return nil, fmt.Errorf("logstore: invalid analytics bucket %q", bucket)
	}
	ep, err := url.JoinPath(*o.BaseEndpoint, bucket)
	if err != nil {
		return nil, fmt.Errorf("logstore: endpoint: %w", err)
	}
	return s3.New(o, func(so *s3.Options) {
		so.BaseEndpoint = &ep
		so.UsePathStyle = true
		if wrap != nil {
			so.HTTPClient = wrap(so.HTTPClient)
		}
	}), nil
}

// openPartition loads (or creates) the lake catalog of one partition.
func openPartition(ctx context.Context, c *s3.Client, partition string) (*lake.Bucket, error) {
	return guard(func() (*lake.Bucket, error) { return lake.NewFromClient(ctx, c, partition) })
}

// guard runs a lake call and turns a panic into an error: lake panics on misuse (invalid
// filter types, sort specs), and a server never crashes on it (AGENTS §5.3).
func guard[T any](fn func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			slog.Error("logstore: recovered lake panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("logstore: lake panicked: %v", r)
		}
	}()
	return fn()
}

// isNoSuchKey reports a missing object: a shard compacted away under a reader's cached
// catalog (lake bug L4).
func isNoSuchKey(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == "NoSuchKey"
}

// isLockFailure reports lake's optimistic-lock failure: the commit did not land.
func isLockFailure(err error) bool { return errors.Is(err, lake.ErrOptimisticLock) }

// noTable reports a query on a partition without the table yet (nothing committed).
func noTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not exist") && strings.Contains(err.Error(), "table '")
}
