# ADR 0018 — Forge adapters

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Date:** 2026-10-21
- **Requirements:** R-PIPE-001, R-PIPE-002, R-PIPE-003, R-PIPE-007 (D48), R-FLEET-006, R-SEC-004, R-SEC-005
- **Evidence:** `docs/spikes/s16-forges.md`, code in `spikes/s16-forges/`

## Context

bob reacts to forge webhooks, reports commit statuses, and gives workers checkout credentials,
for GitHub, Forgejo and GitLab. D48 asks for one small interface and the official SDK for each
forge. Forgejo is tested live. GitHub is tested with recorded payloads.

## Decision

1. **One interface** `forge.Adapter`: `Kind`, `ParseWebhook` (pure, verifies first), `LookupRepo`, `EnsureWebhook`,
   `SetStatus`, `IssueCheckout`, `RevokeCheckout`, `SweepCheckouts`. It returns a normalized
   `Event` (push / tag / pull_request / ping / ignored). Exact types are in the write-up §5.1–5.2.
2. **SDKs:** GitHub uses `google/go-github` (v88), authenticated as a GitHub App. GitLab uses
   `gitlab.com/gitlab-org/api/client-go`. Forgejo uses `codeberg.org/mvdkleijn/forgejo-sdk` v3,
   pending owner decision O2: Forgejo has no SDK of its own.
3. **Webhook security:**
   - Verify on the raw body before parsing, and refuse when no secret is configured.
   - GitHub and Forgejo: HMAC-SHA256 hex.
   - GitLab: the signing token (Standard Webhooks) with a ±5 min window.
   - De-duplicate by delivery id with a conditional write.
   - Check the event invariants and that the event's repository id is the linked one.
4. **Statuses:** commit statuses with context `bob/<pipeline>` on all forges. The state mapping
   is in the write-up §5.3.
5. **Checkout credentials:** one repository, read only, minted per job, revoked at job end.
   - GitHub: installation token with `repository_ids` and `contents: read`. GitHub expires it
     after 1 h.
   - GitLab: deploy token with `read_repository` and `expires_at` = the job deadline (enforced
     to the second).
   - Forgejo: token with `read:repository` for the one repository. It has no expiry, so bob
     revokes it and sweeps leftovers. Minting needs the bot account password (owner decision D62).
6. **Fork PRs** are fetched from the base repository's PR/MR head ref, so the credential never
   covers the fork.
7. **Tests:**
   - Real captured Forgejo and GitLab deliveries plus documented GitHub payloads run offline in
     every CI run.
   - A GitHub API replay server checks what the SDK sends.
   - Live Forgejo runs in the harness. Live GitLab runs in the slow suite (owner decision O3).

## Consequences

- Adding a forge means one package that implements eight methods (see Owner amendments), plus fixtures.
- Forgejo checkout tokens depend on bob's revoke and sweep, and bob stores a bot password for
  Forgejo integrations.
- Two SDK gaps are worked around with documented plain requests:
  - Forgejo SDK: repository-scoped token creation.
  - client-go: the hook `signing_token` field.
- GitLab < 19.0 works only with an explicit opt-in to the plaintext secret token.

## Alternatives considered

- GitHub Checks API instead of statuses: richer, but GitHub only. Deferred.
- Forgejo OAuth2 tokens (1 h, enforced by Forgejo): not limited to one repository, and rotating
  refresh tokens are fragile with HA orchestrators. This is option O1-b.
- Long-lived per-project read tokens: violate "short-lived" (R-FLEET-006).

## Owner amendments

- **D62** (O1 a): Forgejo checkout uses the stored bot password to mint a per-job token limited
  to one repository with `read:repository`; bob revokes it at job end and a sweeper (every
  5 min) revokes leftovers by the timestamp in the token name. Option b (OAuth2) and c
  (long-lived token) are rejected.
- **D57**: the worker requests checkout credentials only with the token it obtained through
  the fleet bucket (`WorkerControlService/GetCheckoutCredential`, SPEC §05.9.1, worker token + PoP, lease and epoch
  checked); the checkout token is HPKE-sealed to the worker key.
- **A12 / A13** confirm O2 a (community forgejo-sdk v3) and O3 a (live GitLab in the slow suite).
- SPEC change beyond the owner decisions: the interface grows from six to **eight methods**,
  adding `LookupRepo` (repo id for R-PIPE-002 linking) and `EnsureWebhook` (hooks with
  bob-generated secrets, required for GitLab signing tokens). GitHub returns `ErrUnsupported`
  for `EnsureWebhook` (App-level hooks). Exact types: SPEC §06.9.1.
