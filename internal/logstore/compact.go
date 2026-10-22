package logstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/lake"

	"github.com/megakuul/bob/internal/store/s3client"
)

// Compaction rules (R-LOG-100, S10 §5.3, F7).
const (
	MaintenanceInterval = 30 * time.Second
	// compactMinAge: a partition whose time range began less than 10 min ago is not compacted
	// (its shards belong to builds that just started and are read by followers).
	compactMinAge = 10 * time.Minute
	// compactMaxIdle: only partitions written within the last 2 days are compacted; older ones
	// were compacted while they were written to.
	compactMaxIdle = 48 * time.Hour
	// CompactionSize is the target shard size. S10 §3.5: 8 MB shards keep a package query at
	// ~50 ms and a follow-from-lake read small; the catalog stays small through retention.
	CompactionSize = 8 << 20
)

// Maintenance is the work of the `lake-maintenance` controller (every 30 s, leader only,
// SPEC §05.11): it compacts the recent partitions. Each partition gets a lake.Bucket that
// nothing else uses (lake bug L3: a Bucket that reloads its catalog during a compaction
// duplicates the merged rows). It is not safe for concurrent use: one controller runs it.
type Maintenance struct {
	client  *s3.Client // prefixed with the analytics bucket
	err     error
	shards  int
	now     func() time.Time
	buckets map[string]*lake.Bucket
}

// NewMaintenance performs no I/O.
func NewMaintenance(client *s3.Client, analyticsBucket string, now func() time.Time) *Maintenance {
	c, err := PrefixClient(client, analyticsBucket, nil)
	if now == nil {
		now = time.Now
	}
	return &Maintenance{client: c, err: err, shards: DefaultLogShards, now: now, buckets: map[string]*lake.Bucket{}}
}

// Candidates are the partitions a pass at now considers: the log partitions of the last two
// days and today, and the run/job partitions of this and the previous month, minus those whose
// time range began less than 10 min ago.
func (m *Maintenance) Candidates(now time.Time) []string {
	now = now.UTC()
	var out []string
	for d := 2; d >= 0; d-- {
		day := time.Date(now.Year(), now.Month(), now.Day()-d, 0, 0, 0, 0, time.UTC)
		if now.Sub(day) < compactMinAge {
			continue
		}
		for k := range m.shards {
			out = append(out, "log-"+day.Format("20060102")+"-"+strconv.Itoa(k))
		}
	}
	for mo := 1; mo >= 0; mo-- {
		month := time.Date(now.Year(), now.Month()-time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
		if now.Sub(month) < compactMinAge {
			continue
		}
		out = append(out, "run-"+month.Format("200601"), "job-"+month.Format("200601"))
	}
	return out
}

// Pass compacts each candidate partition that was written within the last 2 days. It returns
// the partitions it compacted.
func (m *Maintenance) Pass(ctx context.Context) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	now := m.now()
	var done []string
	var errs []error
	for _, p := range m.Candidates(now) {
		written, ok, err := m.lastWrite(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok || now.Sub(written) > compactMaxIdle {
			continue
		}
		if err := m.compact(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			delete(m.buckets, p) // start over with a fresh catalog next time
			continue
		}
		done = append(done, p)
	}
	return done, errors.Join(errs...)
}

// lastWrite is the time of the partition catalog's last change (every commit rewrites it);
// ok is false when the partition does not exist. A HEAD, so a pass never creates partitions.
func (m *Maintenance) lastWrite(ctx context.Context, p string) (time.Time, bool, error) {
	key := "lakedb.catalog"
	out, err := m.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &p, Key: &key})
	if err != nil {
		if s3client.Status(err) == http.StatusNotFound {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	if out.LastModified == nil {
		return m.now(), true, nil
	}
	return *out.LastModified, true, nil
}

func (m *Maintenance) compact(ctx context.Context, p string) error {
	b := m.buckets[p]
	if b == nil {
		var err error
		if b, err = openPartition(ctx, m.client, p); err != nil {
			return err
		}
		m.buckets[p] = b
	}
	switch {
	case strings.HasPrefix(p, "log-"):
		return compactTable[LogLine](ctx, b)
	case strings.HasPrefix(p, "run-"):
		return compactTable[PackageRun](ctx, b)
	case strings.HasPrefix(p, "job-"):
		return compactTable[JobRun](ctx, b)
	}
	return nil
}

// compactTable runs lake's compactor on one table; a table that does not exist yet is fine.
func compactTable[T any](ctx context.Context, b *lake.Bucket) error {
	_, err := guard(func() (struct{}, error) {
		return struct{}{}, lake.NewCompactor[T](b, lake.WithCompactionSize[T](CompactionSize)).Compact(ctx)
	})
	return err
}
