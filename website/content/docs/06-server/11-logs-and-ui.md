---
title: Logs and the web UI
section: Server
order: 11
summary: Where build logs live, how to read and follow them, and what the web UI shows.
---

This page covers build logs on the server and the web UI that orchestrators serve.

## Build logs

Workers keep the log lines of running jobs in memory and write them to the analytics bucket
in batches (every `BOB_LOG_FLUSH_INTERVAL`, 30 s by default), as lake tables partitioned by
day. Logs are kept for `BOB_RETENTION_LOGS` (30 days).

Read them with the CLI:

```bash
bob log --build b-01M5PXB03VHB1K6D23P07ER9N7 //cmd/hello:hello      # one package of a server build
bob log --build b-01M5PXB03VHB1K6D23P07ER9N7 -f //cmd/hello:hello   # follow it while it runs
```

While someone follows a running build, the orchestrator pulls fresh lines from the worker
directly, then switches to the stored log; lines are never duplicated. The build page of the
web UI shows the same logs.

> [!NOTE]
> On the documentation host, `bob log --build` ran without errors but printed nothing for the
> trial builds (their steps wrote no output). Following a running build was not tried.

## The web UI

Orchestrators serve the web UI at the base URL. Log in with a local account or an OIDC
provider. Pages:

| Page | Shows |
|---|---|
| Overview | running and failed builds, queue depth |
| Projects, project page | builds, pipelines, releases of a project |
| Build | the jobs, packages, errors and logs of one build; cancel, retry, re-run |
| Release | artifacts, download links, visibility |
| Registry | repositories and tags |
| Cache | cache usage |
| Fleet, worker | workers, their state and jobs; drain, revoke |
| Analytics | build durations and history |
| Access | users, roles, bindings, service accounts, OIDC providers |
| Settings | server settings, signing keys, forge integrations |
| Account | your sessions |

The UI uses only the public API, so everything it does can be scripted.

## Health

The admin listener (`BOB_SERVER_ADMIN_LISTEN`, default `127.0.0.1:9090`) answers `/healthz`
and `/readyz` with `ok`. Use them for load balancers and service managers.

## Next steps

- [Operations and security](12-operations.md).
- [Troubleshooting](13-troubleshooting.md).
