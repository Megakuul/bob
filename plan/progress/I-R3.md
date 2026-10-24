# I-R3 progress (reviewer pass 2, read-only)

- started 2026-10-24; scope: rbac, auth, forge webhook, isolation, bob-builder+inputs lock,
  unpackers, pkg/cli, pkg/output, pkg/release, ecosystem generator injection.
- report: docs/review/m5-m18-reviewer-2.md
- checkpoint 1: rbac/auth/oidc read (findings: GLOBAL grant paths w/o escalation check,
  require_mapping default false, OIDC login CSRF); forge intake read (GitHub/Forgejo replay via
  unsigned delivery id); isolation read; inputs lock verified with throw-away Nix tests (flat
  file input, nested userns remount/unmount: all refused). fetch + builder unpackers read: sound.
- next: materialise, srcs, golang zip, pkg/cli, output, release, generators.
- DONE 2026-10-24: report docs/review/m5-m18-reviewer-2.md (G1-G9). Verified with throw-away
  tests: G4 (shim version injection), input lock holds (flat-file input, nested-userns remount).
  Private copy /var/tmp/I-R3 deleted. No repository file written besides the report and this file.
- NEEDS (for the coordinator): fixes G1 (internal/auth), G2 (internal/auth + proto), G3
  (internal/forge/integration), G4/G7/G9 (pkg/cli), G5 (internal/auth); owner decisions G1, G6.
