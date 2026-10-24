---
title: Server architecture
section: Server
order: 1
summary: How bob-server works - orchestrators and workers, four S3 buckets, no database and no coordinator.
---

This page explains the parts of a bob server and how they talk to each other. Read it before
you install one.

## What the server does

`bob-server` adds to the `bob` CLI:

- **remote builds** on a fleet of workers (`bob build --remote`, pipelines);
- a **Nix binary cache** of everything the fleet built, per project and private by default;
- an **OCI registry** (pull only) for images built by the fleet;
- **releases**: versioned downloads and installs (`bob install`);
- **CI pipelines** started by GitHub, GitLab and Forgejo webhooks, with commit statuses;
- users, service accounts, roles and a **web UI**.

## The parts

| Part | Runs | Does |
|---|---|---|
| orchestrator | `bob-server orchestrator` | serves the API, the web UI, the cache, the registry and downloads; runs the controllers (build planning, promotion, releases, retention, …) |
| worker | `bob-server worker`, as root | claims jobs, evaluates and builds in an isolated job container, uploads results to a staging area |
| S3 | any S3 service with conditional writes | the only storage: there is no database, broker or coordinator |

`bob-server` is one binary for every role. The `bob` CLI never talks to S3; it talks to an
orchestrator's HTTP API.

## The buckets

| Bucket (default name) | Holds | Orchestrator | Worker |
|---|---|---|---|
| `bob-control` | users, projects, builds, releases, settings, signing keys | read/write | no access |
| `bob-fleet` | fleet configuration, worker records, queues, jobs, heartbeats (ids only) | read/write | read; write only its own prefixes |
| `bob-analytics` | build logs and run history (Parquet tables written with lake) | read/write | read/write |
| `bob-artifacts` | the Nix cache, OCI blobs, release assets, source snapshots, staging | read/write | read; write only `staging/` |

Control and fleet records are stored with dynamitedb, logs with lake. Every state change is
a conditional write, so two orchestrators can never both win the same change.

The split is the security boundary: a worker credential cannot read users, tokens or signing
keys at all. Workers never publish to the cache directly; they stage outputs, and an
orchestrator verifies (re-hashes, scans references) and signs them. See
[Operations and security](12-operations.md).

## High availability without a coordinator

Every orchestrator runs every controller. A controller works only while it holds a short
lease in S3; when an orchestrator dies, another one takes its leases within a few intervals.
Add orchestrators by starting more processes with the same storage configuration behind a
load balancer; remove one by stopping it.

Workers find orchestrators through the fleet bucket and keep a connection to them for wake-ups
and live logs. Idle workers poll their queue rarely (every 30 s by default), so an idle fleet
costs few S3 requests.

## How a remote build flows

1. `bob build --remote` uploads a snapshot of your source (tracked and untracked, not
   ignored files) and creates a build.
2. The build controller plans jobs and queues them per worker class (the platform).
3. A worker claims a job, evaluates `bob.go` in a sandbox, builds, and stages the outputs and
   logs.
4. An orchestrator promotes the staged outputs into the cache, signs them, and tags images.
5. The CLI downloads the results from the cache.

Outputs that `cache.nixos.org` already has are never stored again.

> [!NOTE]
> The harness tests multi-orchestrator races and orchestrator crashes against RustFS. The
> deployment on this documentation's test host used one orchestrator and one worker.

## Next steps

- [Install bob-server](02-install.md).
- [Configuration reference](03-configuration.md).
