// Package logstore stores and reads build logs and package runs in the lake (SPEC §05.10,
// ADR 0012, ADR 0019).
//
// Layout: every partition is one lake "bucket" below the analytics bucket (the URL-prefix trick
// of SPEC §05.10.1): log lines of a build live in `log-<yyyymmdd>-<k>`, package runs in
// `run-<yyyymm>`, job runs in `job-<yyyymm>`. lake is used at v0.5.3 (= 045e301, bug L2 fixed);
// the remaining lake bugs L1, L3 and L4 are worked around here (R-LOG-100): no sort tag,
// compaction only through Maintenance with its own lake.Bucket per partition, readers retry a
// query once with a fresh lake.Bucket on NoSuchKey.
//
// Writers (workers) batch lines in memory and commit on an interval, on size and at package and
// job end (Writer, R-LOG-001). A commit error other than an optimistic-lock failure may still
// have landed, so batches are retried and readers deduplicate by (build, package, attempt, seq).
//
// Readers apply the restart rule of ADR 0019: the valid line for seq s of a package is the one
// of attempt max{a : from(a) ≤ s}, where from(a) is the first seq of attempt a (from(1) = 1).
// Feed and Follow merge persisted and live lines into one gap-free, duplicate-free stream per
// follower (R-LOG-101).
package logstore
