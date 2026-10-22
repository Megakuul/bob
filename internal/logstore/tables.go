package logstore

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/megakuul/lake"
)

// LogLine is one row of the `log_line` table (S10 §5.1). Only the filter columns are lake
// types; the rest are plain columns with parquet encodings (2.8× smaller files). No sort tag:
// lake bug L1 hangs filtered scans over sorted, overlapping shards (R-LOG-100).
type LogLine struct {
	lake.Table `name:"log_line"`
	BuildID    lake.String `parquet:"build_id"`
	Package    lake.String `parquet:"package"`
	Seq        lake.Int    `parquet:"seq"`
	Drv        string      `parquet:"drv,dict,zstd"`
	Attempt    int64       `parquet:"attempt,zstd"`
	Stream     int64       `parquet:"stream,zstd"`
	TS         int64       `parquet:"ts,delta,zstd"`
	Line       string      `parquet:"line,zstd"`
}

// PackageRun is one row of the `package_run` table: one finished package build (analytics).
type PackageRun struct {
	lake.Table `name:"package_run"`
	BuildID    lake.String `parquet:"build_id"`
	Project    lake.String `parquet:"project"`
	Package    lake.String `parquet:"package"`
	Drv        lake.String `parquet:"drv"`
	Worker     lake.String `parquet:"worker"`
	Status     lake.String `parquet:"status"` // ok | failed | cached | cancelled
	Start      lake.Int    `parquet:"start"`  // unix ns
	End        lake.Int    `parquet:"end"`    // unix ns
	DurationMs lake.Int    `parquet:"duration_ms"`
	Lines      lake.Int    `parquet:"lines"`
}

// JobRun is one row of the `job_run` table: one finalised job (feeds the planner).
type JobRun struct {
	lake.Table `name:"job_run"`
	BuildID    lake.String `parquet:"build_id"`
	JobID      lake.String `parquet:"job_id"`
	Class      string      `parquet:"class,dict,zstd"`
	Worker     string      `parquet:"worker,dict,zstd"`
	Drvs       int64       `parquet:"drvs,zstd"`
	SlotMs     int64       `parquet:"slot_ms,zstd"`
	WallMs     int64       `parquet:"wall_ms,zstd"`
}

// RunInfo describes one finished package build; NewPackageRun turns it into a package_run row,
// so that callers never build lake values (the dbusage lint rule: lake only in logstore).
type RunInfo struct {
	BuildID, Project, Package, Drv, Worker string
	Status                                 string // ok | failed | cached | cancelled
	Start, End                             time.Time
	Lines                                  int64
}

// NewPackageRun returns the package_run row of r.
func NewPackageRun(r RunInfo) PackageRun {
	return PackageRun{
		BuildID: lake.NewString(r.BuildID), Project: lake.NewString(r.Project), Package: lake.NewString(r.Package),
		Drv: lake.NewString(r.Drv), Worker: lake.NewString(r.Worker), Status: lake.NewString(r.Status),
		Start: lake.NewInt(r.Start.UnixNano()), End: lake.NewInt(r.End.UnixNano()),
		DurationMs: lake.NewInt(r.End.Sub(r.Start).Milliseconds()), Lines: lake.NewInt(r.Lines),
	}
}

// NewJobRun returns the job_run row of one finalised job.
func NewJobRun(buildID, jobID, class, worker string, drvs, slotMs, wallMs int64) JobRun {
	return JobRun{BuildID: lake.NewString(buildID), JobID: lake.NewString(jobID), Class: class, Worker: worker,
		Drvs: drvs, SlotMs: slotMs, WallMs: wallMs}
}

// Streams of a line.
const (
	StreamStdout = 1
	StreamStderr = 2
	StreamBob    = 3
)

// Limits of hostile input (R-LOG-102, SPEC §6.5.8).
const (
	MaxLineBytes     = 4 << 10 // one line of text
	MaxChunkLines    = 5000    // lines of one LiveChunk
	MaxChunkBytes    = 4 << 20 // one LiveChunk
	MaxPackageBytes  = 1024    // a package label
	DefaultLimit     = 10000   // GetLog without a limit
	MaxLimit         = 100000  // GetLog limit cap
	MaxSearchHits    = 1000
	MinSearchTerm    = 3
	MaxSearchTerm    = 200
	DefaultLogShards = 4 // log.shards
)

// Line is one log line as readers and followers see it.
type Line struct {
	Package string
	Seq     int64
	Attempt int32
	Stream  int32
	TS      int64 // unix ns
	Text    string
}

// Cursor is the last line a follower has of one package.
type Cursor struct {
	Seq     int64
	Attempt int32
}

// Restart says attempt Attempt of Package starts at FromSeq: lines of older attempts with
// seq ≥ FromSeq are void.
type Restart struct {
	Package string
	Attempt int32
	FromSeq int64
}

// Final is the end marker of one package.
type Final struct {
	Package string
	LastSeq int64
	Attempt int32
}

// Batch is one message to a follower.
type Batch struct {
	Lines    []Line
	Restarts []Restart
	End      []Final
	Done     bool
	Delayed  bool // no live source is reachable: lines come from the lake only
}

// SplitText turns one output line into stored line texts: valid UTF-8 (invalid bytes become
// U+FFFD, protobuf strings must be UTF-8), each at most MaxLineBytes, cut at rune boundaries.
// A longer line becomes several lines (S10 §5.1); an empty line stays one empty line.
func SplitText(s string) []string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) <= MaxLineBytes {
		return []string{s}
	}
	var out []string
	for len(s) > MaxLineBytes {
		cut := MaxLineBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}
