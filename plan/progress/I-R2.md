# I-R2 (reviewer, M5-M18) — progress

- done: security-focused review; report at docs/review/m5-m18-reviewer.md (F1-F8).
- verified with throw-away tests in /var/tmp/I-R2 (deleted): F1 cross-project poison, F3 skipped
  bypass, F4 unknown-hash cap; snapshot unpack refused the symlink chain input tested.
- by inspection: F2 (checkout/download token minted from worker-writable job record, no SpecSig
  check), F5, F6-F8.
- not covered: pkg ecosystems, pkg/output, pkg/cli, internal/ui, rbac escalation, OIDC, isolation,
  cmd/bob-builder, builder inputs lock: needs a second pass.
- DECISIONS: none. NEEDS: owner decision on F1 fix 2 (binding promoted paths to the evaluation).
- TEST PROBLEM: none.
