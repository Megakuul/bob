package logstore

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	lake "github.com/megakuul/lake"
)

// LogLine is one line of build output. Table "log_line".
//
// No `sort` tag: with lake HEAD a Where() query over two or more sorted shards whose key
// ranges overlap hangs forever (parquet-go merged-row seek loop, see write-up bug L1).
// Without a sort tag lake concatenates shards instead of merging them. Workers sort each
// flush batch by (build_id, package, seq) before Insert, and readers sort results.
//
// Only columns that lake must filter or sort on are lake types (build_id, package, seq).
// The rest are plain Go fields, because lake.String/lake.Int wrap the value in a group
// {data} and parquet-go refuses compression/encoding tags on groups. Plain fields get
// dictionary + zstd, which halves the file size (probe run, write-up table 1).
type LogLine struct {
	lake.Table `name:"log_line"`

	BuildID lake.String `parquet:"build_id"`      // time-sortable id (ULID-like, 26 chars)
	Package lake.String `parquet:"package"`       // bob package label, e.g. //svc/api:server
	Seq     lake.Int    `parquet:"seq"`           // per (build, package), from 1, gap-free across attempts
	Drv     string      `parquet:"drv,dict,zstd"` // derivation path
	Attempt int64       `parquet:"attempt,zstd"`  // job attempt (retries after worker loss)
	Stream  int64       `parquet:"stream,zstd"`   // 1 stdout, 2 stderr, 3 bob (system messages)
	TS      int64       `parquet:"ts,delta,zstd"` // unix nanoseconds when the worker read the line
	Line    string      `parquet:"line,zstd"`     // one line, capped at 4 KiB
}

// LogLineNaive is the same table with every column as a lake type (for size comparison).
type LogLineNaive struct {
	lake.Table `name:"log_line_naive"`

	BuildID lake.String `parquet:"build_id"`
	Package lake.String `parquet:"package"`
	Seq     lake.Int    `parquet:"seq"`
	Drv     lake.String `parquet:"drv"`
	Attempt lake.Int    `parquet:"attempt"`
	Stream  lake.Int    `parquet:"stream"`
	TS      lake.Int    `parquet:"ts"`
	Line    lake.String `parquet:"line"`
}

// PackageRun is one package build (analytics). Table "package_run".
type PackageRun struct {
	lake.Table `name:"package_run"` // no sort tag, see LogLine

	BuildID    lake.String `parquet:"build_id"`
	Project    lake.String `parquet:"project"`
	Package    lake.String `parquet:"package"`
	Drv        lake.String `parquet:"drv"`
	Worker     lake.String `parquet:"worker"`
	Status     lake.String `parquet:"status"` // ok, failed, cached, cancelled
	Start      lake.Int    `parquet:"start"`  // unix ns
	End        lake.Int    `parquet:"end"`    // unix ns
	DurationMs lake.Int    `parquet:"duration_ms"`
	Lines      lake.Int    `parquet:"lines"`
}

// Plain is the Go-side view of a log line (lake wrappers removed).
type Plain struct {
	BuildID, Package, Drv string
	Attempt, Stream, Seq  int64
	TS                    int64
	Line                  string
}

func ToPlain(l LogLine) Plain {
	return Plain{l.BuildID.Data, l.Package.Data, l.Drv, l.Attempt, l.Stream, l.Seq.Data, l.TS, l.Line}
}

func FromPlain(p Plain) LogLine {
	return LogLine{
		BuildID: lake.NewString(p.BuildID), Package: lake.NewString(p.Package), Seq: lake.NewInt(p.Seq),
		Drv: p.Drv, Attempt: p.Attempt, Stream: p.Stream, TS: p.TS, Line: p.Line,
	}
}

func NaiveFromPlain(p Plain) LogLineNaive {
	return LogLineNaive{
		BuildID: lake.NewString(p.BuildID), Package: lake.NewString(p.Package), Seq: lake.NewInt(p.Seq),
		Drv: lake.NewString(p.Drv), Attempt: lake.NewInt(p.Attempt), Stream: lake.NewInt(p.Stream),
		TS: lake.NewInt(p.TS), Line: lake.NewString(p.Line),
	}
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// BuildID returns a 26-char time-sortable id (48-bit ms time + 80 random bits, Crockford base32).
func BuildID(t time.Time, r *rand.Rand) string {
	var b [26]byte
	ms := uint64(t.UnixMilli())
	for i := 9; i >= 0; i-- {
		b[i] = crockford[ms&31]
		ms >>= 5
	}
	for i := 10; i < 26; i++ {
		b[i] = crockford[r.IntN(32)]
	}
	return string(b[:])
}

var dirs = []string{"src", "lib", "internal", "pkg", "cmd", "vendor/x", "web/src", "core"}
var words = []string{"alloc", "parser", "lexer", "server", "client", "codec", "store", "cache", "graph", "sched", "util", "crypto", "http", "json", "proto", "ring", "queue", "pool"}

// Pkgs returns n package labels.
func Pkgs(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("//%s/%s:%s%d", dirs[i%len(dirs)], words[i%len(words)], words[(i*7)%len(words)], i)
	}
	return out
}

func Drv(r *rand.Rand, name string) string {
	var h [32]byte
	const b32 = "0123456789abcdfghijklmnpqrsvwxyz"
	for i := range h {
		h[i] = b32[r.IntN(32)]
	}
	n := strings.NewReplacer("/", "-", ":", "-").Replace(strings.TrimPrefix(name, "//"))
	return "/nix/store/" + string(h[:]) + "-" + n + ".drv"
}

// Line produces a realistic-looking compiler/build output line (40-160 bytes).
func Line(r *rand.Rand, pkg string, seq int64) string {
	w := words[r.IntN(len(words))]
	d := dirs[r.IntN(len(dirs))]
	switch r.IntN(10) {
	case 0:
		return fmt.Sprintf("[%3d%%] Building C object CMakeFiles/%s.dir/%s/%s_%d.c.o", r.IntN(101), w, d, w, r.IntN(500))
	case 1:
		return fmt.Sprintf("gcc -O2 -g -fPIC -Wall -I%s/include -c %s/%s_%d.c -o build/%s_%d.o", d, d, w, r.IntN(500), w, r.IntN(500))
	case 2:
		return fmt.Sprintf("%s/%s_%d.c:%d:%d: warning: unused variable '%s_%d' [-Wunused-variable]", d, w, r.IntN(500), r.IntN(3000), r.IntN(80), w, r.IntN(99))
	case 3:
		return fmt.Sprintf("   Compiling %s-%s v0.%d.%d", w, words[r.IntN(len(words))], r.IntN(30), r.IntN(10))
	case 4:
		return fmt.Sprintf("ok  \texample.com/%s/%s\t%d.%03ds", d, w, r.IntN(9), r.IntN(1000))
	case 5:
		return fmt.Sprintf("=== RUN   Test%s%s_%d", strings.ToUpper(w[:1]), w[1:], r.IntN(400))
	case 6:
		return fmt.Sprintf("--- PASS: Test%s_%d (0.%02ds)", w, r.IntN(400), r.IntN(100))
	case 7:
		return fmt.Sprintf("ld: linking %s/lib%s.so (%d objects, %d KiB)", d, w, r.IntN(300), r.IntN(90000))
	case 8:
		return fmt.Sprintf("bob: %s step %d/%d: %s", pkg, seq%37, 37, w)
	default:
		return fmt.Sprintf("  CC       %s/%s/%s_%x.o", d, w, w, r.Uint32())
	}
}

// SortLines orders lines by (build, package, attempt, seq).
func SortLines(rows []LogLine) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := &rows[i], &rows[j]
		if a.BuildID.Data != b.BuildID.Data {
			return a.BuildID.Data < b.BuildID.Data
		}
		if a.Package.Data != b.Package.Data {
			return a.Package.Data < b.Package.Data
		}
		return a.Seq.Data < b.Seq.Data
	})
}
