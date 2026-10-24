# STATUS (updated 2026-10-24, coordinator)

Resume here. Branch `bob-v1`, local commits only (never push without asking).

## Done (committed)
Engine (M2-M7), ecosystems Go/cc/Rust/npm/OCaml/proto/connect/graphql/openapi (M8-M11), outputs OCI/tarball/AppImage/release (M12), server: store on dynamitedb, logs on lake, fleet, orchestrator, workers, promotion (A149 kinds claim), cache, registry (per-project tag visibility), releases, forge adapters + pipelines on devstack, identity/RBAC, web UI embedded and served, bob builds itself (`bob pipeline run ci`, bootstrap.sh), budgets benchmarks (26/28 rows), no-op build performance, read-only build inputs, harness corrections 1-88, reviewer + steward passes (docs/review/m5-m18-*).

## Running at time of writing
I-B45 (tests/nix, eco/golang, eco/gen triage), I-B46 (security fixes F1-F8 from reviewer), I-B47 (steward refactors).

## Open work, by class
1. Security: finish I-B46; second review pass of the unreviewed packages (ecosystems, outputs, cli, RBAC/OIDC, forge intake, builder lock); bob exec/dev/shims run store binaries as store owner.
2. Full green `go run ./tools/ci`: acceptance can't run as the stage runs it on this host (disk/tmp, 30 min package timeouts): needs a quiet bigger host or sequential runner; parity floor re-measure (last 60/252 passing vs floor 14, before ~15 fix units); tests/core ~35/87 at last run.
3. Missing features still red: see plan/progress/I-B27.md table, I-B31/I-B35/I-B40/I-B41; npm/opam/crates registry snapshots; zsh/fish/protoc-gen-es not installed on host; TestRFW008 / bob check on unlocked fixtures (RULE).
4. Quiet-host budget re-measurement (all timing rows measured under load); B-038, B-083 not measurable yet; B-080, B-087 failing.
5. M17 leftovers: TestPIPE003 (500 ms timing from git push), TestPIPE006 (cold 5 min), TestGitLabLive not run; M19 hardening/v1, `bob install` e2e vs real server, `bob pipeline run --remote`.
6. Owner items: goldens (about 30, out/golden-candidates/), R-OUT-007 size budget (12 MB vs 14.9 MB), new diag code 1027 + golden codes.golden, R-NIX-007 drv-cache key amendment, R-ARCH-005 bootstrap wording (A158), generated header wording Rust/OCaml, dbusage.todo, ROADMAP.md refresh, open test problems.
