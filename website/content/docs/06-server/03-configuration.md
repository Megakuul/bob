---
title: Server configuration
section: Server
order: 3
summary: Every environment variable bob-server reads, with defaults, plus the settings stored in the server.
---

bob-server is configured with environment variables only. This page lists every variable the
server reads; CI checks the list against the source code. Settings that admins change at run
time (retention of unreferenced cache paths, session lengths, signing keys) live in the
server and are edited in the web UI.

A malformed value is an error at start (exit 78, `EX_CONFIG`) for the process settings, and a
warning that keeps the default for the tuning settings marked "warns".

## S3 storage

Used by every mode (orchestrator, worker, `admin`, `store fsck`). S3 credentials come from the
standard AWS variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, …),
the shared config file or instance metadata. Requests use path-style URLs.

| Variable | Default | Meaning |
|---|---|---|
| `BOB_STORAGE_ENDPOINT` | — (required) | S3 endpoint URL, e.g. `https://s3.eu-central-1.amazonaws.com` or `http://127.0.0.1:19000` |
| `BOB_STORAGE_REGION` | `us-east-1` | S3 region |
| `BOB_STORAGE_BUCKETS_CONTROL` | `bob-control` | control bucket |
| `BOB_STORAGE_BUCKETS_FLEET` | `bob-fleet` | fleet bucket |
| `BOB_STORAGE_BUCKETS_ANALYTICS` | `bob-analytics` | logs and analytics bucket |
| `BOB_STORAGE_BUCKETS_ARTIFACTS` | `bob-artifacts` | cache, images, release assets |
| `BOB_STORAGE_WORKER_PROBE_CREDENTIAL` | unset | `<access key>:<secret key>` of the worker identity; orchestrators check at start that it cannot read `bob-control` and refuse to start if it can |

## Process

| Variable | Default | Meaning |
|---|---|---|
| `BOB_SERVER_NAME` | the host name | this process's name (orchestrator id, worker name, signing key prefix) |
| `BOB_STATE_DIR` | none | local state directory; on workers it holds the Nix store and job directories |

## Orchestrator

| Variable | Default | Meaning |
|---|---|---|
| `BOB_SERVER_LISTEN` | `:8443` | address of the main listener (API, UI, cache, registry, downloads, webhooks); plain HTTP |
| `BOB_LISTEN` | — | older name of `BOB_SERVER_LISTEN`, used when that is unset |
| `BOB_SERVER_ADMIN_LISTEN` | `127.0.0.1:9090` | health listener (`/healthz`, `/readyz`); `off` disables it |
| `BOB_SERVER_BASE_URL` | none | public `http(s)://` URL of the server: links, OAuth redirects, the OCI token realm |
| `BOB_MAX_PARALLEL_DOWNLOADS` | `256` | concurrent downloads per orchestrator (cache, registry, releases); beyond it clients get 503 and retry (warns) |
| `BOB_SERVER_OUTBOUND_ALLOW` | none | comma-separated hosts, addresses and CIDR ranges that outgoing forge and webhook requests may reach although they are loopback, private or link-local (refused by default); needed for a forge on a private network |
| `BOB_ADMIN_TOKEN` | unset | a bootstrap admin token for test builds only; a release build of bob-server refuses to start when it is set |

## Worker

| Variable | Default | Meaning |
|---|---|---|
| `BOB_ORCHESTRATOR_ADDRS` | — | comma-separated `http(s)://` URLs of orchestrators to attach to |
| `BOB_WORKER_SLOTS` | half the CPUs, at least 1 | concurrent jobs (1 to 1024) |

## Fleet tuning

Orchestrators publish these to the workers through the fleet bucket. Durations use Go syntax
(`30s`, `5m`); counts are whole numbers.

| Variable | Default | Meaning |
|---|---|---|
| `BOB_FLEET_POLL_INTERVAL` | `30s` | how often an idle worker polls its queue (± 20 %) |
| `BOB_FLEET_HEARTBEAT` | `15s` | worker heartbeat interval |
| `BOB_FLEET_LEASE_TTL` | `60s` | job claim grace and heartbeat expiry |
| `BOB_FLEET_SKEW` | `5s` | allowed clock skew |
| `BOB_FLEET_RESLOT_AFTER` | `60s` | re-queue a job nobody claimed |
| `BOB_FLEET_GONE_AFTER` | `5m` | a worker without heartbeat is `stale` after this |
| `BOB_FLEET_MAX_ATTEMPTS` | `3` | infrastructure retries per job |
| `BOB_FLEET_JOB_SHARDS` | `16` | shards of the active-job index |
| `BOB_FLEET_WORKER_SHARDS` | `4` | shards of the worker index |

## Logs

| Variable | Default | Meaning |
|---|---|---|
| `BOB_LOG_FLUSH_INTERVAL` | `30s` | how often workers commit log batches to the analytics bucket |
| `BOB_LOG_SHARDS` | `4` | log partitions per day |
| `BOB_LIVE_LOG_CHUNK_INTERVAL` | `250ms` | live log chunk cadence while someone follows a build |

## Retention

Durations take a day suffix (`30d`) or Go syntax (`720h`). Invalid values warn and keep the
default.

| Variable | Default | Meaning |
|---|---|---|
| `BOB_RETENTION_LOGS` | `30d` | build logs |
| `BOB_RETENTION_ANALYTICS` | `400d` | run and job history |
| `BOB_RETENTION_BUILDS` | `365d` | build records |
| `BOB_RETENTION_CACHE` | `90d` | cache paths no release and no recent build references; the server setting "unreferenced cache days" overrides it |

Release artifacts are never collected while the release exists.

## Not configurable yet

The specification also names `server.trusted_proxies` (client addresses behind a proxy) and
`worker.min_free` (disk headroom) and a `bob-server.yaml` file. They are not implemented:
there is no variable for them, and bob-server reads no configuration file.

## Next steps

- [Workers](04-workers.md).
- [Operations and security](12-operations.md).
