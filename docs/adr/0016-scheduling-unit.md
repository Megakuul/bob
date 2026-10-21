# ADR 0016 — Scheduling unit: closure job by default, ready-set batches when it pays

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Date:** 2026-10-21
- **Requirements:** R-FLEET-003/004/005, R-SRV-004, R-REG-002, R-NFR-004/005/007/008
- **Evidence:** `docs/spikes/s14-sched.md`, code in `spikes/s14-sched/`

## Context

R-FLEET-005 leaves open whether a job is a whole target closure on one worker or a single
derivation. Jobs live in dynamitedb (S9: ~12 requests per job, ~30 claims/s on RustFS);
outputs reach other workers only through staging → promotion (S12); every job runs in its own
overlay `/nix` (S13).

## Decision

1. A **job realises a set of ≤ 64 derivations on one worker** with Nix's local scheduler.
2. Default: **one closure job per build** (`evalbuild`: evaluation and build in the same job,
   drvs never leave the worker).
3. The build controller **splits** a build into batch jobs of ready derivations only when it
   predicts ≥ 60 s and ≥ 30 % gain with the idle eligible workers (planner in the write-up
   §5.1). Batch size ≈ ready drvs / idle workers, ≤ 64 drvs, ≤ 60 estimated slot-s, affinity
   to the worker holding most inputs.
4. **Promotion per job.** Dependencies between jobs pass only through promoted (signed) paths,
   never through `staging/`.
5. Static chunking of the graph is rejected (measured slower than both pure options).
6. One record per derivation is rejected (0.6 s overhead per drv, claim-rate ceiling,
   3–7× S3 requests).

## Consequences

- Small and medium builds cost the same as option a: one job, promotion once.
- Large wide builds use the fleet (model: go200 1.8×, rust129 1.5×, wide2000 13× at 100
  workers), and a worker death costs one batch instead of the whole build.
- Outputs of a split build that later fails may already be published (private by visibility).
- The planner needs per-drv duration history (lake analytics).
- If the owner chooses a only, the job schema stays the same, so splitting can be added later
  without a protocol change.

## Owner amendments

- **D53**: option c accepted as written.
- **D54**: S14 rule 5.4.1 ("push notification") is the `Wake{class, slot}` message on the
  worker's Attach stream. Polling is slow (30 s) and serves as the fallback.
- **D60**: the job record moves from `live/<platform>/j/<jobID>` to `job/<jobID>`, with a
  sequence-log queue per class (SPEC §05.4.1). The S14 fields are kept in the signed `JobSpec`.
  Affinity (`pw`/`pt`) only orders wake-ups and never restricts claims, because a claim
  restriction could strand a slot (SPEC R-FLEET-011).
