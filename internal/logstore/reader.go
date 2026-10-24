package logstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/lake"
)

// Reader reads persisted logs from lake partitions.
type Reader struct {
	client *s3.Client // prefixed with the analytics bucket
	shards int
	err    error // set when the client could not be built
}

// NewReader returns a reader over the analytics bucket. It performs no I/O.
//
// Example:
//
//	r := logstore.NewReader(s3client, "bob-analytics")
//	lines, err := r.Package(ctx, buildID, "//svc/api:server", 0, 10000)
func NewReader(client *s3.Client, analyticsBucket string) *Reader {
	c, err := PrefixClient(client, analyticsBucket)
	return &Reader{client: c, shards: DefaultLogShards, err: err}
}

// ErrBadRequest marks invalid reader arguments (the API answers invalid_argument).
var ErrBadRequest = errors.New("invalid log request")

func checkPackage(pkg string) error {
	if len(pkg) > MaxPackageBytes || strings.ContainsAny(pkg, "\x00\n") {
		return fmt.Errorf("%w: package label", ErrBadRequest)
	}
	return nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

// Package returns the lines of one package after afterSeq, ordered by seq, deduplicated by
// (attempt, seq) and filtered by the restart rule.
func (r *Reader) Package(ctx context.Context, buildID, pkg string, afterSeq int64, limit int) ([]Line, error) {
	if pkg == "" {
		return nil, fmt.Errorf("%w: package is empty", ErrBadRequest)
	}
	ls, _, err := r.read(ctx, buildID, pkg, afterSeq)
	if err != nil {
		return nil, err
	}
	if l := clampLimit(limit); len(ls) > l {
		ls = ls[:l]
	}
	return ls, nil
}

// Build returns all lines of a build ordered by package, seq.
func (r *Reader) Build(ctx context.Context, buildID string, limit int) ([]Line, error) {
	ls, _, err := r.read(ctx, buildID, "", 0)
	if err != nil {
		return nil, err
	}
	if l := clampLimit(limit); len(ls) > l {
		ls = ls[:l]
	}
	return ls, nil
}

// Page returns up to limit lines of a build (pkg "" = every package) after the position
// (afterPkg, afterSeq) in (package, seq) order, and whether more lines follow. It serves
// GetLog: a client continues with the last line it got.
func (r *Reader) Page(ctx context.Context, buildID, pkg string, afterSeq int64, limit int) ([]Line, bool, error) {
	ls, _, err := r.read(ctx, buildID, pkg, afterSeq)
	if err != nil {
		return nil, false, err
	}
	if l := clampLimit(limit); len(ls) > l {
		return ls[:l], true, nil
	}
	return ls, false, nil
}

// Search returns up to MaxSearchHits lines of a build (pkg "" = all packages) that contain term
// (case-sensitive, 3..200 bytes; lake has no substring filter, S10 F5).
func (r *Reader) Search(ctx context.Context, buildID, term, pkg string) ([]Line, bool, error) {
	if len(term) < MinSearchTerm || len(term) > MaxSearchTerm {
		return nil, false, fmt.Errorf("%w: search term must be 3..200 bytes", ErrBadRequest)
	}
	ls, _, err := r.read(ctx, buildID, pkg, 0)
	if err != nil {
		return nil, false, err
	}
	var hits []Line
	for _, l := range ls {
		if strings.Contains(l.Text, term) {
			if len(hits) == MaxSearchHits {
				return hits, true, nil
			}
			hits = append(hits, l)
		}
	}
	return hits, false, nil
}

// Raw returns the settled lines of a build (pkg "" = all packages) and the restarts inferred
// from them; Feed uses it for its lake reads.
func (r *Reader) Raw(ctx context.Context, buildID, pkg string) ([]Line, []Restart, error) {
	return r.read(ctx, buildID, pkg, 0)
}

// LastSeqs returns, per package, the last valid persisted (seq, attempt) of a build: where a
// retried job continues (SPEC §05.10.2).
func (r *Reader) LastSeqs(ctx context.Context, buildID string) (map[string]Cursor, error) {
	ls, _, err := r.read(ctx, buildID, "", 0)
	if err != nil {
		return nil, err
	}
	out := map[string]Cursor{}
	for _, l := range ls {
		if c := out[l.Package]; l.Seq > c.Seq {
			out[l.Package] = Cursor{Seq: l.Seq, Attempt: l.Attempt}
		}
	}
	return out, nil
}

// read queries the build's log partition and settles the rows.
func (r *Reader) read(ctx context.Context, buildID, pkg string, afterSeq int64) ([]Line, []Restart, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	if err := checkPackage(pkg); err != nil {
		return nil, nil, err
	}
	part, err := LogPartition(buildID, r.shards)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	rows, err := r.scan(ctx, part, buildID, pkg, afterSeq)
	if err != nil {
		return nil, nil, fmt.Errorf("logstore: read %s: %w", part, err)
	}
	lines := make([]Line, 0, len(rows))
	for _, row := range rows {
		if row.BuildID.Data != buildID {
			continue
		}
		lines = append(lines, Line{Package: row.Package.Data, Seq: row.Seq.Data, Attempt: int32(row.Attempt),
			Stream: int32(row.Stream), TS: row.TS, Text: row.Line})
	}
	ls, rs := settle(lines)
	return ls, rs, nil
}

// scan runs one filtered query with a fresh lake.Bucket (lake.NewFromClient loads the catalog,
// one small GET). A long-lived Bucket caches its catalog for 60 s, so it does not see the
// commits of other processes (workers), and after a compaction elsewhere it fails with
// NoSuchKey on the deleted input shards: plan/progress/I-B16-repro/l7-catalog-cache and
// l4-nosuchkey (part A). With a fresh Bucket per query neither happens (l4-nosuchkey part B).
func (r *Reader) scan(ctx context.Context, part, buildID, pkg string, afterSeq int64) ([]LogLine, error) {
	b, err := openPartition(ctx, r.client, part)
	if err != nil {
		return nil, err
	}
	return guard(func() ([]LogLine, error) {
		f := LogLine{BuildID: lake.FilterString(lake.Eq(buildID))}
		if pkg != "" {
			f.Package = lake.FilterString(lake.Eq(pkg))
		}
		if afterSeq > 0 {
			f.Seq = lake.FilterInt(lake.Gt(afterSeq))
		}
		rows, err := lake.Query[LogLine]().Where(f).Scan(ctx, b)
		if noTable(err) {
			return nil, nil
		}
		return rows, err
	})
}

// PackageRuns returns the package_run rows of project whose package started in [since, until),
// only those of package label when label is not "" (AnalyticsService/PackageDurations). It
// queries the run-<yyyymm> partition of every month in the range, each with a fresh
// lake.Bucket (R-LOG-100); a month without rows costs one catalog GET.
func (r *Reader) PackageRuns(ctx context.Context, project, label string, since, until time.Time) ([]PackageRun, error) {
	if r.err != nil {
		return nil, r.err
	}
	if err := checkPackage(label); err != nil {
		return nil, err
	}
	if project == "" || len(project) > MaxPackageBytes {
		return nil, fmt.Errorf("%w: project", ErrBadRequest)
	}
	since, until = since.UTC(), until.UTC()
	var out []PackageRun
	for m := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, time.UTC); m.Before(until); m = m.AddDate(0, 1, 0) {
		part := "run-" + m.Format("200601")
		rows, err := r.scanRuns(ctx, part, project, label, since.UnixNano(), until.UnixNano())
		if err != nil {
			return nil, fmt.Errorf("logstore: read %s: %w", part, err)
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (r *Reader) scanRuns(ctx context.Context, part, project, label string, from, to int64) ([]PackageRun, error) {
	b, err := openPartition(ctx, r.client, part)
	if err != nil {
		return nil, err
	}
	return guard(func() ([]PackageRun, error) {
		f := PackageRun{Project: lake.FilterString(lake.Eq(project)), Start: lake.FilterInt(lake.Gte(from), lake.Lt(to))}
		if label != "" {
			f.Package = lake.FilterString(lake.Eq(label))
		}
		rows, err := lake.Query[PackageRun]().Where(f).Scan(ctx, b)
		if noTable(err) {
			return nil, nil
		}
		return rows, err
	})
}
