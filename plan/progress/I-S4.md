# I-S4 progress (code steward review M5-M18, read-only)

Output: docs/review/m5-m18-steward.md. No code edited, no git.

## done
- read AGENTS.md, impl-brief, m2-m4-steward (format), execution-plan M5-M18, dynamitedb-lake-usage.
- tools installed outside repo: /var/tmp/I-S4/bin/{staticcheck v0.8.2, deadcode x/tools v0.51.0};
  AST scan /var/tmp/I-S4/flen; raw outputs /var/tmp/I-S4/{sc,dc,flen-pkg,flen-int,doc-*}.txt.
- review written: 16 ranked findings (F1-F16), suggested order, owner items, not-reviewed list.
- COMPLETE.

## DECISIONS
- none (read-only review).

## NEEDS (for the coordinator)
- F1 registrar.go:176 matches a string ("decode record") that nothing produces; route to the
  orchestrator owner with a unit test (garbage wreg/ record -> rejection).
- F3 decisions.md id collisions (A280, A300/A301, A310, duplicate A170-A173) and 21 D-I* ids cited
  in code but missing from decisions.md: coordinator-owned file.
- Owner items: SPEC R-STORE-106 names internal/artifacts (empty package); Rust/OCaml generated-file
  header wording (goldens); empty tools/lint/dbusage.todo mechanism (protected).

## TEST PROBLEM
- none.
