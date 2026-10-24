// Package logstore stores and reads build logs and package runs in the lake (SPEC §05.10,
// ADR 0012, ADR 0019). It is the only package that imports github.com/megakuul/lake
// (docs/dynamitedb-lake-usage.md).
//
// Layout: every partition is one lake "bucket" below the analytics bucket (SPEC §05.10.1):
// log lines of a build live in `log-<yyyymmdd>-<k>`, package runs in `run-<yyyymm>`, job runs
// in `job-<yyyymm>`. lake v0.5.3 is used as documented (tables, Ingestor, Query, Compactor).
// Where a minimal program that follows lake's README/tests fails, the smallest workaround is
// kept with a comment naming the program in plan/progress/I-B16-repro/: PrefixClient
// (partition-prefix), no sort tag (l1-sort), a fresh lake.Bucket per query (l7-catalog-cache,
// l4-nosuchkey) and per compaction (l3-compact-shared).
//
// Writers (workers) queue lines in memory and commit them through one lake.Ingestor per
// partition on an interval, on size and at package and job end (Writer, R-LOG-001). A failed
// commit stays in lake's backlog; an ambiguous one may land twice, so readers deduplicate by
// (build, package, attempt, seq).
//
// Readers apply the restart rule of ADR 0019: the valid line for seq s of a package is the one
// of attempt max{a : from(a) ≤ s}, where from(a) is the first seq of attempt a (from(1) = 1).
// Feed and Follow merge persisted and live lines into one gap-free, duplicate-free stream per
// follower (R-LOG-101).
package logstore
