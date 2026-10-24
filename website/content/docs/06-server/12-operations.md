---
title: Operations and security
section: Server
order: 12
summary: Backups, consistency checks, upgrades, the security model of the bucket split, and what to do when a worker host is compromised.
---

This page is for operators: what to back up, how to check and upgrade a server, how its
security boundaries work, and the runbook for a compromised worker.

## Backups

All state is in the four buckets; orchestrators and workers keep nothing that cannot be
recreated (a worker's `BOB_STATE_DIR` is only a cache).

| Bucket | Back up? |
|---|---|
| `bob-control` | yes: users, projects, builds, releases, signing keys |
| `bob-artifacts` | yes for releases (`rel/`) and the cache you do not want to rebuild |
| `bob-analytics` | optional: logs and history |
| `bob-fleet` | no: workers re-register, queues refill |

Use your storage's versioning or replication. A backup of `bob-control` contains password
hashes, token hashes and the signing seeds: protect it like the live bucket.

## Consistency check

`bob-server store fsck` reads every record of the control and fleet buckets and validates it:

```text
$ bob-server store fsck
25 objects checked, 0 problems
```

It uses the orchestrator's storage configuration and changes nothing.

## Upgrades

Replace the binary and restart processes one at a time. Orchestrators share no local state,
so rolling restarts keep the API available when you run more than one behind a load
balancer. Workers finish or abandon their jobs; abandoned jobs are re-queued.

## Security model

The boundaries are structural:

- **Bucket split.** Workers have no access to `bob-control` (users, tokens, signing keys)
  and can write only their fleet prefixes and `staging/` of the artifacts bucket. The shipped
  policies in `deploy/policies/` enforce this; `BOB_STORAGE_WORKER_PROBE_CREDENTIAL` makes the
  orchestrator verify it at start.
- **Promotion.** Only orchestrators publish to the cache. They re-hash every staged output,
  check the NAR structure, scan for undeclared references, and sign with their key. A path
  already published is never replaced.
- **Job isolation.** Jobs run in containers without the worker's credentials; evaluation runs
  in a sandbox without network; builds run unprivileged in Nix's sandbox.
- **Tokens** are stored only as SHA-256 hashes; passwords as argon2id hashes. Secrets are
  redacted from logs.
- **No credential minting.** bob never creates S3 credentials; hosts get them from your
  deployment.

## A compromised worker

Do both steps, in this order:

1. **Revoke the worker** in the web UI (Fleet) or with `FleetService/RevokeWorker`. Its
   tokens are refused within seconds.
2. **Rotate the shared worker S3 credential** in your deployment, then restart the healthy
   workers with the new one.

A compromised worker could read every cached output and source snapshot (private projects
included) and the logs, and could publish wrong outputs for the jobs it claimed. Audit the
promotions of its jobs. The full runbook, with what an attacker can and cannot do, is
`docs/operations/worker-compromise.md` in the bob repository.

## Next steps

- [Troubleshooting](13-troubleshooting.md).
- [Configuration reference](03-configuration.md).
