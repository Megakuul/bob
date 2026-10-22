# Runbook: compromised worker host

Requirement R-SEC-101 (SPEC §05.2.2). Decisions: D13 (bucket split), D38 (workers use the
analytics bucket directly), D57 (bob never mints S3 credentials; every further token comes
through S3). Design record: ADR 0013.

Use this when a worker host is suspected to be controlled by someone other than the operator
(escaped build, stolen host image, leaked worker S3 key, unexpected process on the host).

Summary: **revoke the worker id first (seconds), then rotate the shared worker S3 credential
(minutes), then audit, then re-register on clean hosts.** Revoking alone does not remove the
S3 access of the host; rotating alone does not stop an already issued token. Do both.

Names below use the default buckets: `bob-control`, `bob-fleet`, `bob-analytics`,
`bob-artifacts` (configuration `storage.*`, SPEC §05.11.1).

## 1. What a compromised worker can and cannot do

Source: SPEC §05.2.1 bucket table, §05.2.3 policy table, `deploy/policies/worker.json`,
`deploy/policies/control-bucket.json`. This is also the checklist for the damage assessment.

| Resource | Worker (host root, holds the shared worker S3 credential) |
|---|---|
| `bob-control` | **Nothing.** The worker policy has an explicit Deny `s3:*` on the bucket and `/*`, and the bucket policy denies everyone except `orchestrator-bob`. It cannot read users, password hashes, sessions, tokens, role bindings, projects, forge integrations, build and release records, sealed secrets, the orchestrator signing seeds (`okey/<kid>`) or the OCI HMAC keys. It cannot change admin data. |
| `bob-fleet` read | Everything (`s3:GetObject` on `/*`): fleet config, worker records, queue slots, job records, heartbeats. These hold ids only (R-SEC-003), no user data. It can read the sealed token blobs of other workers (`worker/<wid>.ct`), which are encrypted to each worker's own key and useless without that key. |
| `bob-fleet` write | `PutObject` only on `wreg/*`, `wrs/*`, `wrt/*`, `whb/*`, `job/*`, `qs/*`, `qh/*`. It can: register new worker ids, write heartbeats (including forged ones for other wids, which the reconciler rejects by signature), claim or tamper with job and queue records. It cannot write `worker/`, `fleet/`, `orch/`, `ctl/`, `qt/`, `jact/`, `widx/` (so it cannot approve a worker, change fleet config, add itself as an orchestrator or edit the active-job index). It cannot delete anything (`s3:DeleteObject` is not granted). |
| `bob-analytics` | Read, write and delete (D38). It can read all build logs and run tables, and corrupt or delete logs and history. |
| `bob-artifacts` read | Everything (`s3:GetObject` on `/*`): cached Nix outputs, OCI blobs, release assets, source snapshots (`src/`). Source snapshots and unreleased outputs are therefore readable. |
| `bob-artifacts` write | `staging/*` only. Nothing under `nix/`, `oci/`, `rel/`, `src/` (written by orchestrators), so it cannot publish a poisoned cache path directly. Promotion from staging is done by the orchestrator, which verifies the job result against the job record. A worker that owns a job can still stage wrong outputs for that job; see the audit section. |
| Worker token (`bwt1`) and PoP key of that worker id | Yes, for that wid only: `Attach` and `GetCheckoutCredential`. TTL 1 h, rotated at half life. A checkout credential is issued only for a job the worker holds (R-FLEET-006), for one repository, `contents: read`, revoked at job end. A token used with another worker's key fails (`proof of possession invalid`). |
| Other workers' jobs | It can read their job records but cannot subscribe to or cancel their jobs over Attach (R-FLEET-002). |
| Orchestrator API, RBAC, users | No principal: workers are not part of RBAC and only reach `bob.worker.v1.WorkerControlService`. |

Not covered by the bucket split (read this as the residual risk):

- Source code of repositories whose jobs ran on the host, the checkout credentials of jobs
  running at the time, the contents of the host's persistent Nix store, and any secret that was
  placed into a build by a user.
- Everything in `bob-analytics` and the readable parts of `bob-artifacts`.
- The S3 credential itself: it is a shared credential (all workers use the same worker
  identity), so it is valid from anywhere until rotated.

Build isolation (R-FLEET-008: job container without worker credentials, builds as non-root,
no access to IMDS or loopback) is the primary boundary; the bucket split is the emergency
boundary. If the host was compromised through a build escape, treat that as a bug in bob and
report it.

## 2. Detect

Any one of these is enough to start the runbook.

1. Host signals: unexpected processes, a changed `/usr/bin/bob`, cgroup or mount anomalies,
   outbound traffic that is not S3, an orchestrator endpoint or the forge.
2. Fleet signals (UI fleet page or `bob-server` metrics):
   - `bob_fleet_forged_results_total` increases: a job result signed by a non-owner was ignored;
   - a worker id that starts many registrations (`worker registration rejected: <reason>` in
     the orchestrator log);
   - a `JobSpec` with a bad signature (the worker marks the slot `stale`);
   - jobs completing implausibly fast or with outputs that differ from a rebuild
     (non-determinism checks, `bob build --remote` vs local).
3. S3 provider signals: access logs of the worker credential from addresses outside the fleet,
   or any request of the worker credential against `bob-control` (all return 403, so every
   such request is an attacker probe or a misconfigured host).
4. Start-up self-check: `bob-server` exits 78 with
   `✗ control bucket is readable with the worker credential` when `storage.worker_probe_credential`
   is configured and the control bucket is not protected (R-SEC-102). If this fires, fix the
   bucket policy before anything else.

Identify the worker id(s): in the UI fleet page, or with the RPC `FleetService/ListWorkers`. The wid is the key suffix of `worker/<wid>` and `whb/<wid>` in the
fleet bucket. A host that restarted has a new wid; revoke every wid that the host used.

## 3. Contain: revoke the worker id (target: under 1 minute)

```
bob-server worker revoke <wid>        # or: UI > Fleet > worker > Revoke
# RPC: bob.fleet.v1.FleetService/RevokeWorker (needs the global fleet permission)
```

What this does (SPEC §05.6.3 worker state machine, §6.5.9 `RevokeWorker`): the admin RPC CASes
the fleet record `worker/<wid>` to `st=revoked`, `g`+1 and clears the sealed token `ct`. The
fleet bucket record is the single source of truth, so this works from any orchestrator.

- Token and PoP of that wid are refused by every orchestrator within **10 s** (record cache
  limit, R-NFR-107). Attach streams of the wid are closed and `GetCheckoutCredential` returns
  `unauthenticated`.
- Jobs owned by the wid return to the queue: the job reconciler sees `worker/<wid>.st = revoked`
  and requeues them (SPEC §05.6.1, leases of revoked workers expire immediately). Results later written for such a job by the revoked host are ignored (owner and
  epoch no longer match).
- Per-job forge checkout tokens issued to the worker are revoked at job end by the requeue and,
  at the latest, by the `forge-sweeper` within 60 s plus clock skew. For jobs that were
  holding a checkout, revoke the token on the forge by hand if the repository is sensitive
  (forge UI, the token is named `bob-<jobID>-<timestamp>`).
- The worker process exits when it reads `revoked`. A new process creates new keys and a new
  wid; it cannot reuse the old one.

Verify:

```
# UI fleet page, or RPC FleetService/GetWorker: state revoked, generation incremented
```

Revoked records are garbage collected after 7 days (registration records `wreg/<wid>` and
heartbeats too). Keep the record during the investigation: **do not wait for the GC; copy
`worker/<wid>`, `wreg/<wid>`, `whb/<wid>` (readable with an orchestrator credential) into the
incident folder.**

Revocation by itself does not stop the host from using the S3 credential. A host that lost
its wid can still register a *new* wid with the shared credential (registration needs no admin
step, D13'), and can still read `bob-fleet`, `bob-analytics`, `bob-artifacts` and write the
prefixes above. Continue with step 4 at once. If you must slow the host down while you prepare
the new credential, stop the machine or cut its network: that is the only immediate measure for
the S3 access, bob has no kill switch for the credential itself (D57).

## 4. Rotate the shared worker S3 credential (target: under 15 minutes)

bob never mints or rotates S3 credentials (R-SEC-100); this step happens in the deployment.
All workers share one identity (`worker-bob` in the harness stack), so rotation affects the
whole fleet. The order matters:

1. **Create the new credential** for the same worker identity/policy. The policy must be
   `deploy/policies/worker.json` unchanged. Examples:
   - AWS IAM: `aws iam create-access-key --user-name <worker-user>`; for role-based fleets
     (IMDSv2, web identity) detach the compromised instance profile or role session instead
     (`aws iam put-role-policy` with an explicit deny, or revoke older sessions) and give the
     replacement hosts a new instance profile.
   - Other S3 backends: create a new access key for the worker user, with the identical policy.
2. **Roll the new credential out to the healthy workers** (secret store, systemd credential,
   environment of the unit, instance profile) and restart them. Workers re-register with new
   keys and a new wid after a restart (zero-setup registration, <= 5 s p99, R-FLEET-001).
   Do not restart all at once if the queue must keep draining: drain first
   (UI, or `FleetService/DrainWorker`), restart, undrain.
3. **Delete the old credential** (`aws iam delete-access-key --access-key-id <old>`). From this
   moment the compromised host has no S3 access. Confirm in the provider's access log that the
   old key id gets 403/InvalidAccessKeyId.
4. If the orchestrator is configured with `storage.worker_probe_credential`, replace it with
   the new worker credential in the orchestrator configuration; the start-up self-check
   (R-SEC-102) then verifies on the next orchestrator start that the new credential still gets
   403 on `bob-control`. Restart orchestrators one at a time.
5. Check that the control bucket policy (`deploy/policies/control-bucket.json`) is still
   attached and that only the orchestrator principal (`orchestrator-bob`) is excluded.

If the **orchestrator** credential might also have been exposed (for example the host also ran an
orchestrator, which is not a supported layout), this runbook is not enough: rotate the
orchestrator credential, the signing keys (UI Signing keys > rotate; `okey/<kid>`) and the
sealed secrets as well, and revoke all sessions and tokens. Compromise of a worker alone never
exposes these, because of the control bucket Deny.

## 5. Audit

Use an orchestrator credential or the UI. All times are UTC.

1. **Time window.** Start = the last point the host was known good (for example the last
   deployment); end = the revoke time. The fleet record gives the wid history:
   `wreg/<wid>` (registration time), `whb/<wid>` (last heartbeat), `worker/<wid>` (state).
2. **Jobs the worker ran.** The lake `job-<yyyymm>` partition, table `job_run` has one row per
   finalised job with `Worker` (wid), `BuildID`, `JobID`, class, duration. Query it for the wid
   and window. Jobs still in `job/<jobID>` with `o=<wid>` were running at revocation.
3. **Packages it built.** `run-<yyyymm>`, table `package_run`: one row per finished package build
   (BuildID, Project, Package, Drv, Worker, Status, start and end). These are the outputs the worker produced.
4. **Logs.** `log-<yyyymmdd>-<k>`, table `log_line`, `k = fnv32a(buildID) mod 4`, for each build id
   found above. Look for lines the build tooling could not have produced. Also look for log
   lines of builds the wid never owned: workers can write to the whole analytics bucket (D38),
   so log lines and run rows of *any* build in the window are untrusted if written under the
   compromised credential. Cross-check against the S3 provider access log (key id and
   source address) where available.
5. **Poisoned outputs.** The orchestrator promotes staged outputs after verifying the job result
   against the job record, so the risk is limited to outputs of jobs the worker owned. For
   each package build in step 3, rebuild on a clean worker and compare store paths. A
   different store path for a deterministic package means the output is tampered:
   remove the cache entries (`nix/narinfo/<hash>.narinfo`, `nix/nar/<hash>.nar.zst`,
   `nix/ls/<hash>.ls`, and `nix/vis/<hash>.json` using an orchestrator credential), and
   re-run or retract any release that references them. Releases are immutable and never
   collected, so a poisoned release must be superseded by a new version and announced.
6. **Forged state.** `bob_fleet_forged_results_total` and the orchestrator log list rejected
   results and registrations. Check `fleet/cfg`, `orch/*`, `ctl/*` for unexpected content (the
   worker could not write them; a difference points to a second incident).
7. **Secrets in the blast radius.** List repositories whose jobs ran on the host in the window.
   Rotate every secret that was passed to those builds and any deploy key that was cloned
   with a checkout credential. Forge tokens are one repository, read only, and revoked at job
   end, so usually nothing is left, but verify on the forge.
8. **Record** the findings in the incident report: wid(s), window, jobs, packages, removed cache
   entries, rotated credentials, time of revoke and rotation.

## 6. Re-register

1. Rebuild the host from a clean image; never reuse the compromised disk, in particular not the
   persistent store `/var/lib/bob/worker/nix` (it would feed the new worker poisoned
   paths; `require-sigs` does not protect local, unsigned paths).
2. Start `bob-server worker` as root (R-FLEET-008) with the **new** credential. It generates a
   fresh key pair, registers (`wreg/<wid>`, `wrs/reg`), and the registrar approves it. No admin
   step is needed (R-FLEET-001): the worker is `active` within a few seconds.
3. Verify in the UI or with `FleetService/ListWorkers` that the new wid is `active`, has the
   expected platform, capabilities and labels, and that the old wid stays `revoked`.
4. Run one known build (`bob build --remote //...` on a sample project) and compare the output
   hash with a previous build.
5. After the 7-day GC the revoked records disappear; keep the incident copies from step 3.

## 7. Checklist

```
[ ] wid(s) identified; records copied
[ ] bob-server worker revoke <wid>        (token + PoP refused <= 10 s)
[ ] host isolated (network cut or powered off)
[ ] new worker S3 credential created, rolled out, old credential deleted
[ ] worker_probe_credential updated; orchestrators restarted one by one
[ ] control-bucket policy still attached
[ ] audit: job_run, package_run, log_line; poisoned cache entries removed
[ ] secrets of affected repositories rotated
[ ] clean host registered, test build compared
```

## Testing this runbook (manual check of R-SEC-101)

The manual acceptance runs the steps on the harness's local deployment (`tools/devstack`:
RustFS, users `worker-bob` and `orchestrator-bob` with the shipped policies, HARNESS §12):

1. Start the stack with two workers, run a build, confirm both workers are `active`.
2. Step 3 on one wid. Expect: its token/PoP refused within 10 s, its job requeued and finished
   by the other worker.
3. Step 4: create a second key for `worker-bob`, restart the workers with it, delete the old
   key. Expect the old key to get 403 on every operation, including the `bob-fleet` reads.
4. Step 5: run the lake queries for the revoked wid.
5. Step 6: start a new worker; expect `active` within 5 s.

Sign the check in the parity manual ledger (HARNESS §11) with the date and the commit.
