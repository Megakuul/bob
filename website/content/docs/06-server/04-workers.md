---
title: Workers
section: Server
order: 4
summary: Add, size, drain and revoke workers; how worker classes and job isolation work.
---

This page covers running a fleet of workers: adding them, how jobs are matched to them, and
taking them out of service.

## Add a worker

Start `bob-server worker` on a new host with the worker S3 identity and the orchestrator
addresses (see [Install](02-install.md#start-a-worker)). There are no registration tokens:
a worker writes a registration with a fresh key pair to the fleet bucket, and the
`registrar` controller of an orchestrator approves it within a few seconds:

```text
time=… level=INFO msg="worker registered" mode=worker wid=wqg5i47ta7ktek5qdvodo5ym5b5 platform=aarch64-linux slots=2
```

Being able to write to the fleet bucket's registration prefix is what makes a host a
worker, so protect the worker S3 credential like a password. A restarted worker process gets
a new key pair and a new worker id.

## Classes

Jobs are queued per class. A worker's class is its platform: `x86_64-linux` or
`aarch64-linux`. A worker claims only jobs of its class; a job whose class has no worker waits
in the queue. Run workers of both architectures to build both platforms natively.

> [!NOTE]
> Capabilities and labels on workers (for example "has a GPU") are part of the design, but
> there is no setting for them yet: every worker of a platform is in the same class.

## Size

`BOB_WORKER_SLOTS` sets how many jobs a worker runs at once (default half the CPUs). Each job
runs in its own container with cgroup limits. The worker keeps a Nix store in
`BOB_STATE_DIR`; outputs that the server's cache or `cache.nixos.org` already have are
substituted rather than rebuilt.

## Isolation

Each job runs in a container without the worker's S3 credentials: the evaluation of
`bob.go` in a sandbox without network, the builds as unprivileged users in Nix's sandbox. The
job container talks to the worker only through a pipe. A host that cannot provide this
(cgroup v2 delegation, Landlock ABI 4, user namespaces) is refused with `BOB-E-7010`.

## Drain and revoke

In the web UI (Fleet page) or through the API (`FleetService`):

| Action | Effect |
|---|---|
| drain | the worker finishes its running jobs and takes no new ones |
| undrain | it takes jobs again |
| revoke | the worker id is refused within seconds; its tokens stop working; the process exits |

A stopped worker becomes `stale` after `BOB_FLEET_GONE_AFTER` (5 minutes); its jobs are
re-queued by the heartbeat rule and its record is removed after a day.

If a worker host may be compromised, revoke it **and** rotate the shared worker S3
credential; see [Operations and security](12-operations.md#a-compromised-worker).

## Next steps

- [Users and access](05-users-and-access.md).
- [CI and remote builds](07-ci-and-remote-builds.md).
