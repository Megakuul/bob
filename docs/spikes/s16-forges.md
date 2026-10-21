# S16 — Forge integration (GitHub, Forgejo, GitLab)

## 1. Question and requirement ids

What does one small forge adapter interface look like, and does each forge support it with its
SDK: verified webhooks (push, PR/MR, tag), commit status reporting, and short-lived checkout
credentials limited to one repository?

Requirements: R-PIPE-001, R-PIPE-002, R-PIPE-003, R-PIPE-007 (D48), R-FW-040, R-FLEET-006,
R-SRV-001, R-SEC-004, R-SEC-005, R-STORE-002 (forge credentials in the control bucket),
BOOTSTRAP §5.4 and §6.8 (recorded webhook payloads for all three forges).

GitLab scope: R-PIPE-001 puts GitLab in v1. R-PIPE-007 says "Forgejo (and GitLab) are tested
live against local instances". So GitLab was run live too, not only replayed.

## 2. What was built

All code is in `spikes/s16-forges/` (module `bobspike/s16`).

| Path | What |
|---|---|
| `forge/forge.go`, `forge/util.go` | Adapter interface, normalized event, sentinel errors, `Secret`, HMAC and event checks |
| `forge/forgejo/` | Forgejo adapter (`codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` v3.0.0) |
| `forge/github/` | GitHub adapter (`github.com/google/go-github/v88` v88.0.0), GitHub App auth |
| `forge/gitlab/` | GitLab adapter (`gitlab.com/gitlab-org/api/client-go` v1.46.0) |
| `forge/contract_test.go` | Contract that all three adapters meet |
| `forge/*/live_test.go` | Live tests (build tag `live`) against local Forgejo and GitLab |
| `fixtures/forgejo/` | 15 real deliveries from Forgejo 16.0.6 (`raw/`) plus 12 named copies, headers + exact body bytes |
| `fixtures/gitlab/live/` | 8 real deliveries from GitLab 19.4.2, signed with a signing token |
| `fixtures/gitlab/sdk-testdata/`, `derived/` | client-go test payloads (Apache-2.0), and https/fork/update variants |
| `fixtures/github/webhooks/` | octokit/webhooks payload examples (MIT) plus 2 derived (`*.derived.json`) |
| `fixtures/github/api/` | Example responses from GitHub's OpenAPI description (installation token, status, installation) |
| `cmd/hookrecv` | Webhook capture receiver |
| `cmd/clonebench` | Mint token → clone like a worker → revoke, with timings |
| `setup-forgejo.sh`, `capture-forgejo.sh` | Start Forgejo (nixpkgs, sqlite, TLS, port 21600), users, repos, tokens; replay the event sequence |
| `gitlab/up.sh`, `gitlab/bootstrap.sh` | Run GitLab 19.4.2 from nixpkgs without NixOS (postgres, redis, gitaly, puma, sidekiq, workhorse with TLS on 21610), root PAT |
| `measure.sh` | Re-runs tests and clone benchmarks on both profiles |

Ports used: 21600 (Forgejo), 21601/21603 (receivers), 21610 (GitLab), 21612 (postgres, socket only),
21613 (GitLab receiver). GitHub was never contacted.

## 3. Results

### 3.1 Tests run

| Test | Forge | Result |
|---|---|---|
| Parse 12 real Forgejo deliveries → expected normalized event | Forgejo 16.0.6 | pass |
| Tampered body / missing header / wrong secret / empty secret → `ErrSignature` | Forgejo | pass |
| `X-Hub-Signature-256` sent by Forgejo equals `X-Forgejo-Signature` | Forgejo | pass |
| Live: hook created with SDK → API commit → delivery → `ParseWebhook` | Forgejo | pass, 2.2–2.7 s push→event |
| Live: `SetStatus` for all 6 bob states, read back via SDK | Forgejo | pass |
| Live: repo-scoped job token clones; other private repo 403; push 403; status write 403 | Forgejo | pass |
| Live: fork PR head `refs/pull/2/head` fetchable from the base repo with the job token | Forgejo | pass |
| Live: shallow fetch of one SHA (`git fetch --depth=1 <sha>`) | Forgejo | pass |
| Live: revoke → clone fails; revoke twice → no error; sweep after "crash" deletes 3 tokens, keeps status token | Forgejo | pass |
| Live: Gitea SDK v0.25.1 against Forgejo 16 (version probe + CreateStatus) | Forgejo | pass |
| GitHub documented signature test vector (`It's a Secret to Everybody`) | GitHub | pass |
| Parse 10 payload examples (push branch, tag create/delete, PR opened/sync/closed/fork, create, release, ping) | GitHub | pass |
| Tampered / missing / SHA-1 only / non-hex signature → `ErrSignature`; signed but `after` not a SHA → `ErrPayload` | GitHub | pass |
| httptest replay of documented API responses: JWT verified with app public key; mint body is `repository_ids=[id]`, `permissions={contents: read}` only; status token `{statuses: write}` minted once and cached; revoke uses the token itself; 6 states → `pending pending success failure error error`; description ≤ 140 runes | GitHub | pass |
| Standard Webhooks reference vector (scheme GitLab documents) | GitLab | pass |
| Parse SDK test payloads + derived (MR update with `oldrev`, fork MR); `http://` clone URL refused; tamper / stale timestamp / missing → errors; legacy `X-Gitlab-Token` only when configured | GitLab | pass |
| Live: hook with `signing_token` → push, branch, MR open, MR update, annotated tag, release → all verified and parsed | GitLab 19.4.2 | pass, ~1.0 s push→event |
| Live: 8 captured deliveries re-verified offline | GitLab | pass |
| Live: `SetStatus` incl. pending→pending, running→running (no error), failure/error→`failed`, cancelled→`canceled` | GitLab | pass |
| Live: deploy token clones; other project refused; push refused; revoke → clone fails | GitLab | pass |
| Live: deploy token with 20 s `expires_at` works before and is refused 5 s after expiry | GitLab | pass |
| Contract: every adapter implements `forge.Adapter`, rejects unsigned and oversize bodies | all | pass |

Commands: `go test ./forge/...` (offline), `go test -tags live -run Live ./forge/forgejo/ ./forge/gitlab/`
(needs the local instances), `./measure.sh`.

### 3.2 Measurements (indicative; other spikes were running; `measure.sh` reruns them)

Small repo = mirror of `github.com/spf13/cobra` (3,533 commits, 3.3 MiB full clone), private,
cloned over HTTPS with the job credential in a one-shot `http.extraHeader`. 7 runs, median (min–max).

| Step | [8c/15G] | [2c/4G] |
|---|---|---|
| Forgejo: mint repo-scoped token (`IssueCheckout`) | 153 ms (140–196) | 156 ms (142–499) |
| Forgejo: revoke token | 142 ms (134–190) | 149 ms (137–662) |
| Forgejo: full clone | 286 ms (262–326) | 396 ms (344–556) |
| Forgejo: `--depth=1 --single-branch` clone (1.2 MiB) | 175 ms (142–190) | 185 ms (138–207) |
| Forgejo: `--filter=blob:none` clone | 386 ms (367–440) | 354 ms (305–659) |
| Forgejo: `SetStatus` | 20–30 ms | not measured |
| GitLab: mint deploy token | 143–147 ms (2 runs) | not measured |
| GitLab: full clone (5 runs) | 623 ms (568–678) | not measured |
| GitLab: depth-1 clone (5 runs) | 537 ms (470–604) | not measured |
| GitLab: `SetStatus` | 175–220 ms (first call 603 ms) | not measured |
| Webhook push → verified event | Forgejo 2.2–2.7 s, GitLab ~1.0 s | — |

Mint and revoke on Forgejo are dominated by the password hash check on basic auth.
Blobless is slower than depth-1 for one build; depth-1 is the right default for a worker.

Footprint of the test instances [8c/15G]: Forgejo 137 MiB RSS, starts in < 1 s. GitLab 3.8 GiB RSS
(puma single mode, sidekiq concurrency 5, gitaly, postgres, redis). GitLab cold start was several
minutes: schema load plus seed, then puma boot took about 4 minutes on this busy host (not timed
precisely).

### 3.3 Findings per forge (each verified unless marked)

**Forgejo 16.0.6**
- Signature: `X-Forgejo-Signature` = hex HMAC-SHA256 of the body. Forgejo also sends GitHub-style
  `X-Hub-Signature-256` and `X-Gitea-*`/`X-Gogs-*` copies. No timestamp header, so replay is
  only limited by de-duplicating `X-Forgejo-Delivery`.
- Event header `X-Forgejo-Event` plus `X-Forgejo-Event-Type`; PR sync arrives as
  `pull_request` / `pull_request_sync` with action `synchronized`. Every tag push also
  sends `create`, every branch delete sends `delete` (no `push` with zero `after`).
- Annotated tag push: `after` is the tag object, `head_commit.id` is the commit.
- Tokens: **no expiry** field. Tokens can be limited to specific repositories
  (`repositories: [{owner,name}]`) with `read:repository` only.
- **Token create/delete require basic auth with the account password**: a token with
  `write:user`, sent as header or as basic-auth password, gets `auth method not allowed` (401).
  Listing works with a token.
- OAuth2 (tested): an OAuth2 app gives 1 h access tokens that clone over HTTPS and respect
  scope (`read:repository` → status write 403), but they are **not limited to a repository**,
  and refresh tokens rotate: the old one is refused after one use.
- Pushing `refs/pull/*` into Forgejo is refused ("deny updating a hidden ref").
- SDK: there is **no Forgejo-owned Go SDK**. `codeberg.org/mvdkleijn/forgejo-sdk` (MIT, a Gitea
  SDK fork) is the community SDK; it works against 16.0.6. Gaps: no `repositories` field on token
  creation (its issue #149), no `created_at` on tokens, no webhook payload types, context set per
  client (`SetContext`) and a version probe in `NewClient` (skip with `SetForgejoVersion`).
  The Gitea SDK also works against Forgejo 16 for statuses. Forgejo's own payload structs are
  GPL-3.0 server code, so the adapter has its own small structs.

**GitHub** (SDK + documented payloads only; unverified against github.com by design)
- go-github provides `ValidateSignature`, `ParseWebHook`, `Apps.CreateInstallationToken` with
  `RepositoryIDs` and `Permissions`, `Apps.RevokeInstallationToken`, `Repositories.CreateStatus`.
  It does not sign the App JWT; the adapter does RS256 with the standard library (30 lines).
- Installation tokens expire after 1 h (documented; the example response has `expires_at`);
  bob can only revoke earlier.
- No timestamp header; replay limited by `X-GitHub-Delivery` de-duplication.

**GitLab 19.4.2**
- Signing token (GitLab ≥ 19.0, GA in 19.1): headers `webhook-id`, `webhook-timestamp`,
  `webhook-signature: v1,<base64>`; HMAC-SHA256 over `{id}.{timestamp}.{body}` with the base64-
  decoded `whsec_` key. The caller supplies the token when creating the hook (bob generates it).
  client-go has no `SigningToken` option; its `NewRequest`/`Do` path sends the documented field.
- Webhooks to loopback need the admin setting `allow_local_requests_from_web_hooks_and_services`
  (test-only; production forges reach bob over the network).
- Deploy tokens: `read_repository`, `expires_at` is a **timestamp** and is enforced to the second.
  Username is `gitlab+deploy-token-N`. Creating them needs Maintainer.
- A project access token (bot user, Maintainer, `api`) is enough for statuses, deploy tokens.
- MR update with new commits carries `oldrev`; metadata edits do not.
- Setting a commit status creates an "external" pipeline in GitLab (seen in sidekiq log).
- Running GitLab outside NixOS needed: a private mount namespace binding `/run/gitlab`
  (the package hard-links config/log/tmp there), postgres in a user namespace (it refuses uid 0),
  `max_locks_per_transaction=1024` for the schema load, a non-dictionary root password, and
  `vendor/gems/sidekiq/bin` on `PATH`.

## 4. Answer

- One small interface (§5.1) covers all three forges. Each adapter is 250–330 lines.
- **Webhooks:** verify before parsing, then normalize. GitHub and Forgejo: HMAC-SHA256 hex.
  GitLab: Standard Webhooks signing token with a 5-minute timestamp window.
- **Statuses:** plain commit statuses on all three forges, with context `bob/<pipeline>`.
- **Checkout credentials:**
  - GitHub: App installation token for one repository id, `contents: read`. GitHub expires it
    after 1 h; bob revokes it when the job ends.
  - GitLab: project deploy token with `read_repository`. GitLab expires it at the exact
    timestamp bob sets; bob also deletes it when the job ends.
  - Forgejo: per-job access token for one repository with `read:repository`. Forgejo has no
    expiry, so bob revokes it at job end and a sweeper revokes leftovers. Minting needs the bot
    account password. This needs an owner decision (§6).
- **Fork PRs:** the worker always fetches `refs/pull/N/head` or `refs/merge-requests/N/head`
  from the linked base repository. The credential covers one repository even for forks
  (verified on Forgejo and GitLab).
- **Release publishing to forges:** not required in v1. R-PIPE-001 lists webhooks, statuses and
  checkout. Releases live in bob's registry (R-REG-004). Release webhooks parse to `ignored`.
- **Test strategy:** offline fixture and replay tests in the fast CI; live Forgejo in the
  harness; live GitLab in the slow suite (owner decision 3).

## 5. SPEC input

### 5.1 Adapter interface **[proposal]** (exact Go, from `forge/forge.go`)

```go
package forge // internal/forge in bob

type Kind string
const ( GitHub Kind = "github"; Forgejo Kind = "forgejo"; GitLab Kind = "gitlab" )

const MaxWebhookBody = 25 << 20 // GitHub's documented payload limit

var (
	ErrSignature   = errors.New("forge: webhook signature missing or invalid") // 401, forge must not retry
	ErrPayload     = errors.New("forge: webhook payload malformed")            // 400
	ErrTooLarge    = errors.New("forge: webhook payload too large")            // 413
	ErrStale       = errors.New("forge: webhook timestamp outside tolerance")  // 401 (GitLab)
	ErrUnsupported = errors.New("forge: operation not supported by this forge")
	ErrAuth        = errors.New("forge: forge rejected bob's credentials")     // integration broken → UI
	ErrNotFound    = errors.New("forge: repository or object not found")
)

type Adapter interface {
	Kind() Kind
	ParseWebhook(h http.Header, body []byte, now time.Time) (Event, error) // no I/O
	SetStatus(ctx context.Context, repo Repo, s Status) error
	IssueCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	RevokeCheckout(ctx context.Context, c Checkout) error           // idempotent
	SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error) // 0,nil where the forge expires tokens
}

type Repo struct {
	Forge Kind; ID int64; FullName, CloneURL, WebURL, DefaultBranch string; Private bool
}
type Status struct { SHA, Context string; State State; Description, TargetURL string }
type State string // pending running success failure error cancelled

type CheckoutRequest struct { Repo Repo; JobID string; TTL time.Duration }
type Checkout struct {
	CloneURL, Username string
	Token        Secret    // never prints, never marshals ("[redacted]")
	ExpiresAt    time.Time
	ForgeExpires bool      // false only for Forgejo
	Handle       string    // opaque, for RevokeCheckout
}
```

One `Adapter` value per forge integration (one credential set), safe for concurrent use,
built from the integration record in the control bucket. Constructors refuse non-https base URLs.

### 5.2 Normalized event schema **[proposal]**

```go
type EventType string // push tag pull_request ping ignored
type Event struct {
	Forge Kind; DeliveryID string; Type EventType; Repo Repo; Sender string
	Push *Push; Tag *Tag; PullRequest *PullRequest   // exactly one for push/tag/pull_request
	IgnoredReason string                             // set when Type == "ignored"
}
type Push struct { Branch, Before, After string; Created, Deleted bool }
type Tag struct { Name, Commit string; Deleted bool } // Commit = peeled commit
type PullRequest struct {
	Number int64; Action PRAction; Title, HeadSHA, HeadBranch, HeadRepo string
	FromFork bool; BaseBranch string; Merged bool
	FetchRef string // ref in the base repo: refs/pull/N/head | refs/merge-requests/N/head
}
type PRAction string // opened synchronized reopened closed other(→ Type ignored)
```

Invariants checked by every adapter after verification (`CheckEvent`): repo id > 0 and name
set; clone URL starts with `https://`; SHAs match `^([0-9a-f]{40}|[0-9a-f]{64})$` (SHA-1 and
SHA-256 object formats); branch and tag names ≤ 255 bytes; PR number > 0.

Example (real Forgejo fork PR, `fixtures/forgejo/pull_request.opened-fork.*`):

```json
{"forge":"forgejo","deliveryID":"…","type":"pull_request",
 "repo":{"forge":"forgejo","id":1,"fullName":"alice/demo","cloneURL":"https://127.0.0.1:21600/alice/demo.git","private":true,…},
 "sender":"mallory",
 "pullRequest":{"number":2,"action":"opened","headSHA":"183c71cb…","headBranch":"evil","headRepo":"mallory/demo",
  "fromFork":true,"baseBranch":"main","merged":false,"fetchRef":"refs/pull/2/head"}}
```

### 5.3 Per-forge mapping table **[proposal]**

| bob | GitHub | Forgejo | GitLab |
|---|---|---|---|
| Event header | `X-GitHub-Event` | `X-Forgejo-Event` (+`-Type`) | `X-Gitlab-Event` |
| Delivery id (dedupe key) | `X-GitHub-Delivery` | `X-Forgejo-Delivery` | `webhook-id` (= `Idempotency-Key`) |
| Signature | `X-Hub-Signature-256: sha256=<hex>` HMAC(body) | `X-Forgejo-Signature: <hex>` HMAC(body) | `webhook-signature: v1,<b64>` HMAC(`id.ts.body`), key = b64 after `whsec_` |
| Timestamp / replay window | none | none | `webhook-timestamp`, ±5 min |
| push (branch) | `push`, `refs/heads/*` | `push`, `refs/heads/*` | `Push Hook` |
| tag | `push`, `refs/tags/*`; commit = `head_commit.id` | same | `Tag Push Hook`; commit = `checkout_sha` |
| PR opened | `pull_request` `opened` | `pull_request` `opened` | `Merge Request Hook` `open` |
| PR new commits | `synchronize` | `synchronized` (type `pull_request_sync`) | `update` with `oldrev` |
| PR reopened / closed | `reopened` / `closed` (+`merged`) | same | `reopen` / `close`, `merge` |
| PR fork | head.repo.id ≠ base.repo.id | head.repo_id ≠ base.repo_id | source_project_id ≠ target_project_id |
| Fetch ref | `refs/pull/N/head` | `refs/pull/N/head` | `refs/merge-requests/IID/head` |
| ignored | create, delete, release, others | create, delete, release, others | Release Hook, others |
| ping | `ping` → `EventPing` | (hook test = push) | (test = push) |
| pending | `pending` | `pending` | `pending` |
| running | `pending` | `pending` | `running` |
| success | `success` | `success` | `success` |
| failure | `failure` | `failure` | `failed` |
| error | `error` | `error` | `failed` |
| cancelled | `error` + "cancelled" | `warning` | `canceled` |
| Description limit | 140 runes | 255 (truncated) | 255 (truncated) |
| bob credential | GitHub App (app id, private key, installation id) | bot account: password + status token | project/group access token, Maintainer, `api` |
| Checkout credential | installation token, `repository_ids:[id]`, `contents:read` | PAT `read:repository`, `repositories:[repo]` | deploy token `read_repository` |
| Clone user name | `x-access-token` | bot login | `gitlab+deploy-token-N` |
| Lifetime | 1 h fixed by GitHub | none → bob revokes + sweeps | `expires_at` = now+TTL, enforced |
| Revoke | `DELETE /installation/token` with the token | `DELETE /users/{bot}/tokens/{id}` (password) | `DELETE /projects/:id/deploy_tokens/:id` |

### 5.4 Webhook security **[proposal]**

1. Endpoint `POST /hooks/{integrationID}`. Look up the integration; unknown id → 404.
2. Read at most `MaxWebhookBody`+1 bytes; more → 413.
3. Verify the signature on the raw bytes before any JSON parsing, with a constant-time compare.
   No secret configured → refuse (never "accept unsigned"). bob generates every secret
   (32 random bytes; GitLab as `whsec_<base64>`). SHA-1 GitHub signatures are not accepted.
   GitLab's plaintext `X-Gitlab-Token` is accepted only if the integration explicitly enables
   it for GitLab < 19.0.
4. GitLab: reject timestamps outside ±5 min (`ErrStale`), checked after the MAC.
5. De-duplicate on `(integrationID, DeliveryID)` with a conditional put in the control bucket.
   A duplicate returns 200 and does nothing. This is the only replay protection for GitHub and
   Forgejo.
6. After verification, treat content as hostile: `CheckEvent` invariants. Also require
   `Event.Repo.ID == project.RepoID` (R-PIPE-002); otherwise 202 and ignore.
7. Pass SHAs and refs to git only as separate arguments after validation, never through a
   shell. An `after` like `--upload-pack=…` is refused (tested).
8. Respond within 10 s: persist the event, then reconcile asynchronously. GitHub documents a
   10 s timeout; Forgejo was configured with `DELIVER_TIMEOUT=5`.
9. Errors: `ErrSignature`/`ErrStale` → 401 with body `invalid signature`. The response never
   says which check failed. The log gets the reason, but no header values.

Error texts **[proposal]**:
- `forge: webhook signature missing or invalid`
- `forge: integration <id>: forge rejected bob's credentials (HTTP 401); re-enter the token in Settings → Forges`
- `forge: integration <id>: repository <fullName> (id <n>) is not linked to project <project>; event ignored`

### 5.5 Token flow **[proposal]**

```
worker holds lease L on job J (project P, repo R)
  → RPC GetCheckout(J, lease proof)                 orchestrator checks the lease is live
  → orchestrator: adapter.IssueCheckout({Repo:R, JobID:J, TTL: lease budget, ≤ 1h})
  → store {J, handle, expiresAt, forgeExpires} in the fleet job record (no token value)
  → return Checkout over the worker's authenticated channel (token encrypted to the worker key)
worker: git init; git -c "http.extraHeader=Authorization: Basic b64(user:token)" fetch --depth=1 <cloneURL> <sha|fetchRef>
        verify HEAD == expected SHA; discard credential (never in .git/config — tested)
job end (any outcome) → orchestrator RevokeCheckout(handle)   (idempotent)
reconciler every 5 min → SweepCheckouts(now − maxJobDuration − 10 min)  (Forgejo; others 0)
lease expiry / requeue → revoke old checkout first, mint a new one for the new holder
```

- Default TTL **[proposal]**: 1 h (GitHub's fixed maximum). The GitLab deploy-token TTL equals the
  job deadline.
- Forgejo token names: `bob-job-<unix-seconds>-<jobID>`. They are unique per user (Forgejo
  requires that), and the sweeper parses the time from the name.
- Statuses use a separate credential that never leaves the orchestrator. GitHub: a cached
  installation token with `statuses: write`. Forgejo: a repo-scoped `write:repository` bot token.
  GitLab: the integration token.

### 5.6 Test strategy **[proposal]**

| Layer | Runs | Content |
|---|---|---|
| Fixture tests | every CI run, offline | Real Forgejo and GitLab deliveries (exact bytes and headers, test secrets alongside). GitHub payload examples (octokit, MIT) and client-go test data (Apache-2.0), plus derived variants, signed by the test. Documented test vectors from GitHub and Standard Webhooks. |
| Replay tests | every CI run, offline | httptest TLS server answering with GitHub's documented example responses. It asserts the SDK's requests: JWT, minimal permissions, a single repository id, status token caching. |
| Contract test | every CI run | All adapters implement `Adapter`, and refuse unsigned and oversize bodies. |
| Live Forgejo | every CI run (harness starts it: < 1 s, 137 MiB) | Hooks, statuses, scoped tokens, revoke, sweep, fork PR ref, round trip. |
| Live GitLab | slow suite (owner decision 3) | Same as Forgejo, plus deploy-token expiry. 3.8 GiB RSS, minutes to start. |
| Fixture refresh | manual (`capture-forgejo.sh`, GitLab live test) | Re-captures fixtures after a forge upgrade. New fixtures are reviewed like goldens. |

All fixtures are protected test data after Gate H. The licences of the third-party fixtures
(MIT octokit, Apache-2.0 client-go) go into ATTRIBUTION.md.

## 6. Decisions

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S16-1 | Commit statuses on all forges; GitHub Checks API not in v1 | One model for three forges (principle 8). Statuses work with an App token and `statuses: write`. |
| S16-2 | Fork PRs build from `refs/pull/N/head` in the base repo with the base-repo credential | The credential stays limited to one repository (R-FLEET-006, principle 5). Verified on Forgejo and GitLab. |
| S16-3 | Release publishing to forges stays out of the interface (ROADMAP) | R-PIPE-001 does not list it; bob's own registry serves releases. |
| S16-4 | `create`/`delete`/release events are `ignored`; tags come from `push` | No duplicate triggers. Every forge sends the same information in push. |
| S16-5 | GitHub App JWT signed with the standard library; no ghinstallation dependency | 30 lines, fewer dependencies (principle 8). |
| S16-6 | GitLab signing token required by default; plaintext secret token only by explicit opt-in | HMAC plus timestamp is structural security (principle 5). GitLab docs advise against secret tokens. |
| S16-7 | Workers fetch with `--depth=1` by SHA or fetch ref, credential in one-shot `http.extraHeader` | Fastest measured mode. The token never lands in `.git/config` (tested). |
| S16-8 | Adapters reject non-https clone/base URLs | Credentials never travel over plain HTTP. |
| S16-9 | GitLab hooks created through client-go's `NewRequest`/`Do` until it has a `SigningToken` field | Still the official SDK. One-time setup call. |
| S16-10 | Forgejo payload structs are local (only the fields bob reads) | The SDK has none. Forgejo's own structs are GPL-3.0 (D16). |

### Owner decisions needed

**O1 — Forgejo checkout credential** (R-FLEET-006 says per repository and short-lived; Forgejo has no token expiry)
- a) **Bot account password** stored encrypted in the control bucket. bob mints a per-job token
  limited to one repository and `read:repository`. It revokes the token at job end, and a
  sweeper revokes leftovers. Meets "per repository". Short-lived only through bob's revoke.
  The bot account cannot use 2FA, because Forgejo needs basic auth for token creation (verified).
- b) **OAuth2 app**: 1 h tokens enforced by Forgejo, and no password. But the tokens cover every
  repository the bot can read. Refresh tokens rotate on each use, so HA orchestrators need a CAS
  on the stored refresh token. A crash between refresh and store loses the grant, and an admin
  has to re-authorize.
- c) **Long-lived read token per project**, limited to the repository, handed to workers. Simple,
  but it violates "short-lived".
- **Recommendation: a)**, with the bot account a collaborator only on linked repositories. Also
  file an upstream Forgejo request for token expiry, which would make the sweeper a fallback.

**O2 — Forgejo SDK** (R-PIPE-007 says "official SDK"; Forgejo has none)
- a) **`codeberg.org/mvdkleijn/forgejo-sdk` v3** (MIT, community, tracks Forgejo). Plain HTTP for
  the one missing call (repo-scoped token creation), with a minimal upstream contribution later.
- b) Gitea's official SDK `code.gitea.io/sdk/gitea` (MIT). Works on Forgejo 16 today, but
  Forgejo-only features (repo-scoped tokens) will never be added.
- c) A thin client generated from Forgejo's swagger. No third-party SDK, but more bob code.
- **Recommendation: a).**

**O3 — GitLab live tests** (R-PIPE-007 says GitLab is tested live; it costs 3.8 GiB and minutes to start)
- a) **Live GitLab in the slow/nightly suite** (`gitlab/up.sh` approach). Fast CI uses the recorded
  live fixtures and offline tests.
- b) Live GitLab in every CI run. Too slow, and does not fit [2c/4G] machines (R-NFR-008).
- c) Recorded payloads only. Cheaper, but does not meet R-PIPE-007.
- **Recommendation: a).**

### Open risks

- GitHub was not exercised live: no App, no account (brief). Unverified on real GitHub: token
  repository restriction, `DELETE /installation/token`, and `X-Hub-Signature-256` on real
  deliveries. They are verified only against the docs and the SDK.
- Forgejo minting needs a password, and basic auth costs about 150 ms per mint (password hash). At
  many jobs per minute that is load on the forge. Not measured at scale.
- Forgejo leaves tokens alive if bob is down for a long time. Worst case is one repository, read only.
- GitLab ≥ 19.0 is needed for the signing token. Older GitLab only has the plaintext token (opt-in).
- No forge sends a webhook timestamp except GitLab. Replay protection depends on bob's delivery-id
  dedupe staying around longer than the forges' retry window. The retention time is not specified yet.
- The GitLab setup outside NixOS is fragile (paths, namespaces, sidekiq PATH). Each GitLab bump
  may break `gitlab/up.sh`.
- Webhook delivery latency (Forgejo 2–3 s) is the forge's queue, not bob. It still counts toward
  "push → build started".

### Still to verify

- Quiet-machine numbers: run `measure.sh` (all timing rows above depend on it). GitLab rows are
  [8c/15G] only.
- GitHub against a real App on a test organization, if the owner allows it (not in this spike).
- Forgejo with 2FA on the bot account (expected to break minting).
- Forgejo webhook behaviour when the receiver is down (retry policy). Not tested.
