# I-G1: lint rule `dbusage` (tools/lint, docs/dynamitedb-lake-usage.md)

## Done
- tools/lint/dbusage.go (go/ast + go/types), registered in lint.Analyzers (boblint `-dbusage`) and in the lint stage
  (tools/ci/stage_lint.go). Testdata: tools/lint/testdata/dbusage (positive + negative), case in lint_test.go,
  todo_test.go proves the todo mechanism.
- Import rules (test files exempt): dynamitedb only internal/store(+sweep); lake only internal/logstore;
  AWS service/s3 (+/types) only internal/store, cache, promote, logstore, orchestrator, worker (reality + guide;
  orchestrator = fsck/staging controllers, logstore = its lake S3 client).
- Call rules: Create/Put/Update value must be &T{...} (or a builder call result; a plain variable is flagged because
  it may be a fetched record) without filter operators; Get/Query/Delete values without update operators;
  Query only in internal/store/sweep (non-test).
- Temporary allowlist: tools/lint/dbusage.todo (`<import|call> <repo-relative file> <reason>`); matching findings are
  printed as "expected (dbusage.todo)" on stderr and do not fail. Nothing else is skipped.

## Findings on the current tree
- internal/store: NONE. The package imports no dynamitedb at all (raw S3 bypass; only a doc comment mentions it), and
  s3 imports there are allowed, so there are no internal/store lines in dbusage.todo. I-B15's new code is judged in
  full; if it needs a todo line it must remove it again.
- internal/worker/process.go:23 imports github.com/megakuul/lake (builds lake rows directly): violates "lake only in
  internal/logstore". Listed in dbusage.todo as expected; owner must move it behind internal/logstore.
- internal/logstore imports service/s3 in cache.go, compact.go, partition.go, reader.go, writer.go: allowed (reality).

## Notes
- boblint needs CGO_ENABLED=0 in this sandbox (no gcc).
